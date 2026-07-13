package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a Jira API client.
type Client struct {
	baseURL    string
	username   string
	apiToken   string
	platform   string
	httpClient *http.Client
	retryOpts  RetryOptions // Retry config for doRequest; overridable in tests
}

// NewClient creates a new Jira client.
func NewClient(baseURL, username, apiToken, platform string) *Client {
	baseURL = strings.TrimSuffix(baseURL, "/")
	return &Client{
		baseURL:   baseURL,
		username:  username,
		apiToken:  apiToken,
		platform:  platform,
		retryOpts: DefaultRetryOptions(),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// apiPath returns the correct API path based on platform.
func (c *Client) apiPath() string {
	if c.platform == PlatformCloud {
		return "/rest/api/3"
	}
	return "/rest/api/2"
}

// doRequest performs an HTTP request to the Jira API with automatic retry on
// transient errors (429 with Retry-After honored). The request body is
// buffered once before the retry loop so it can be replayed on each attempt.
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
	}

	return WithRetryVoid(ctx, func() error {
		var bodyReader io.Reader
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}

		url := c.baseURL + c.apiPath() + path
		req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}

		auth := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.apiToken))
		req.Header.Set("Authorization", "Basic "+auth)
		req.Header.Set("Accept", "application/json")
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to execute request: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			msg := string(respBody)
			switch resp.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				return &AuthError{StatusCode: resp.StatusCode, Message: msg}
			case http.StatusTooManyRequests:
				return &RateLimitError{RetryAfter: parseRetryAfterHeader(resp.Header), Message: msg}
			}
			return fmt.Errorf("API error (status %d): %s", resp.StatusCode, msg)
		}

		if result != nil && len(respBody) > 0 {
			if err := json.Unmarshal(respBody, result); err != nil {
				return fmt.Errorf("failed to parse response: %w", err)
			}
		}

		return nil
	}, c.retryOpts)
}

// GetIssue fetches an issue by key (e.g., "PROJ-42").
func (c *Client) GetIssue(ctx context.Context, issueKey string) (*Issue, error) {
	path := fmt.Sprintf("/issue/%s", issueKey)
	var issue Issue
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

// IssueCreateResponse is the response from POST /issue.
type IssueCreateResponse struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Self string `json:"self"`
}

// descriptionBody renders text as the platform-appropriate description
// payload: ADF for Cloud, plain string for Server (mirrors AddComment).
func (c *Client) descriptionBody(text string) interface{} {
	if c.platform == PlatformCloud {
		return map[string]interface{}{
			"type":    "doc",
			"version": 1,
			"content": []map[string]interface{}{
				{
					"type": "paragraph",
					"content": []map[string]interface{}{
						{"type": "text", "text": text},
					},
				},
			},
		}
	}
	return text
}

// CreateIssue creates a new issue in projectKey with the given summary,
// description, labels, and issue type name (e.g. "Task", "Bug"). It returns
// the created issue's id/key; callers needing the full issue should follow up
// with GetIssue.
func (c *Client) CreateIssue(ctx context.Context, projectKey, issueType, summary, description string, labels []string) (*IssueCreateResponse, error) {
	fields := map[string]interface{}{
		"project":   map[string]string{"key": projectKey},
		"issuetype": map[string]string{"name": issueType},
		"summary":   summary,
	}
	if description != "" {
		fields["description"] = c.descriptionBody(description)
	}
	if len(labels) > 0 {
		fields["labels"] = labels
	}
	reqBody := map[string]interface{}{"fields": fields}

	var created IssueCreateResponse
	if err := c.doRequest(ctx, http.MethodPost, "/issue", reqBody, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

// UpdateFields applies a partial field patch to an issue via PUT /issue/{key}.
// fields is a map of Jira field names (e.g. "summary", "description",
// "labels") to their new values; description values are rendered through
// descriptionBody so callers pass plain text regardless of platform.
func (c *Client) UpdateFields(ctx context.Context, issueKey string, fields map[string]interface{}) error {
	patched := make(map[string]interface{}, len(fields))
	for k, v := range fields {
		if k == "description" {
			if s, ok := v.(string); ok {
				patched[k] = c.descriptionBody(s)
				continue
			}
		}
		patched[k] = v
	}

	path := fmt.Sprintf("/issue/%s", issueKey)
	reqBody := map[string]interface{}{"fields": patched}
	return c.doRequest(ctx, http.MethodPut, path, reqBody, nil)
}

// AddComment adds a comment to an issue.
//
// Jira Cloud uses ADF (Atlassian Document Format); Server uses plain text.
func (c *Client) AddComment(ctx context.Context, issueKey, body string) (*Comment, error) {
	path := fmt.Sprintf("/issue/%s/comment", issueKey)

	var reqBody interface{}
	if c.platform == PlatformCloud {
		reqBody = map[string]interface{}{
			"body": map[string]interface{}{
				"type":    "doc",
				"version": 1,
				"content": []map[string]interface{}{
					{
						"type": "paragraph",
						"content": []map[string]interface{}{
							{
								"type": "text",
								"text": body,
							},
						},
					},
				},
			},
		}
	} else {
		reqBody = map[string]string{"body": body}
	}

	var comment Comment
	if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &comment); err != nil {
		return nil, err
	}
	return &comment, nil
}

// rawComment is a comment with its body left as raw JSON: Cloud renders body
// as an ADF object, Server as a plain string, and idempotency-marker lookups
// only need to substring-match the raw bytes, not decode either shape.
type rawComment struct {
	ID   string          `json:"id"`
	Body json.RawMessage `json:"body"`
}

type commentsListResponse struct {
	Comments []rawComment `json:"comments"`
}

// GetComments lists an issue's comments with bodies left undecoded, for
// idempotency-marker scanning (see sync.go AddComment).
func (c *Client) GetComments(ctx context.Context, issueKey string) ([]rawComment, error) {
	path := fmt.Sprintf("/issue/%s/comment", issueKey)
	var resp commentsListResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Comments, nil
}

// GetTransitions fetches available transitions for an issue.
func (c *Client) GetTransitions(ctx context.Context, issueKey string) ([]Transition, error) {
	path := fmt.Sprintf("/issue/%s/transitions", issueKey)
	var resp TransitionsResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Transitions, nil
}

// TransitionIssue performs a workflow transition on an issue.
func (c *Client) TransitionIssue(ctx context.Context, issueKey, transitionID string) error {
	path := fmt.Sprintf("/issue/%s/transitions", issueKey)
	reqBody := map[string]interface{}{
		"transition": map[string]string{
			"id": transitionID,
		},
	}
	return c.doRequest(ctx, http.MethodPost, path, reqBody, nil)
}

// TransitionIssueTo finds and performs a transition to the specified status.
func (c *Client) TransitionIssueTo(ctx context.Context, issueKey, statusName string) error {
	transitions, err := c.GetTransitions(ctx, issueKey)
	if err != nil {
		return fmt.Errorf("failed to get transitions: %w", err)
	}

	for _, t := range transitions {
		if strings.EqualFold(t.To.Name, statusName) || strings.EqualFold(t.Name, statusName) {
			return c.TransitionIssue(ctx, issueKey, t.ID)
		}
	}

	return fmt.Errorf("no transition found to status: %s", statusName)
}

// AddRemoteLink adds a remote link to an issue (for PR linking).
func (c *Client) AddRemoteLink(ctx context.Context, issueKey string, link *RemoteLink) error {
	path := fmt.Sprintf("/issue/%s/remotelink", issueKey)
	return c.doRequest(ctx, http.MethodPost, path, link, nil)
}

// AddPRLink adds a GitHub PR link to an issue.
func (c *Client) AddPRLink(ctx context.Context, issueKey, prURL, prTitle string) error {
	link := &RemoteLink{
		GlobalID: fmt.Sprintf("github-pr-%s", prURL),
		Object: RemoteLinkObject{
			URL:     prURL,
			Title:   prTitle,
			Summary: "Pull Request created by Pilot",
			Icon: &RemoteLinkIcon{
				URL16x16: "https://github.githubassets.com/favicon.ico",
				Title:    "GitHub",
			},
		},
	}
	return c.AddRemoteLink(ctx, issueKey, link)
}

// GetProject fetches project info.
func (c *Client) GetProject(ctx context.Context, projectKey string) (*Project, error) {
	path := fmt.Sprintf("/project/%s", projectKey)
	var project Project
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &project); err != nil {
		return nil, err
	}
	return &project, nil
}

// SearchResponse represents the response from the Jira search API.
//
// Cloud's POST /rest/api/3/search/jql and Server's GET /rest/api/2/search share
// most fields; nextPageToken/isLast are Cloud-only.
type SearchResponse struct {
	Issues        []*Issue `json:"issues"`
	Total         int      `json:"total,omitempty"`
	StartAt       int      `json:"startAt,omitempty"`
	MaxResults    int      `json:"maxResults,omitempty"`
	NextPageToken string   `json:"nextPageToken,omitempty"`
	IsLast        bool     `json:"isLast,omitempty"`
}

// SearchIssues searches for issues using JQL.
//
// Cloud uses POST /rest/api/3/search/jql (legacy /search removed May 2025,
// see Atlassian changelog CHANGE-2046). Server/DC uses GET /rest/api/2/search.
func (c *Client) SearchIssues(ctx context.Context, jql string, maxResults int) ([]*Issue, error) {
	if maxResults <= 0 {
		maxResults = 50
	}

	if c.platform == PlatformCloud {
		reqBody := map[string]interface{}{
			"jql":        jql,
			"maxResults": maxResults,
			"fields":     []string{"*all"},
		}
		var resp SearchResponse
		if err := c.doRequest(ctx, http.MethodPost, "/search/jql", reqBody, &resp); err != nil {
			return nil, err
		}
		return resp.Issues, nil
	}

	path := fmt.Sprintf("/search?jql=%s&maxResults=%d", strings.ReplaceAll(jql, " ", "+"), maxResults)
	var resp SearchResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Issues, nil
}

// SearchIssuesPaged searches for issues using JQL, paged by startAt/maxResults
// (used by SyncClient's exhaustive delta/full listings, which need a stable
// numeric cursor rather than Cloud's opaque nextPageToken).
func (c *Client) SearchIssuesPaged(ctx context.Context, jql string, startAt, maxResults int) (*SearchResponse, error) {
	if maxResults <= 0 {
		maxResults = 50
	}

	if c.platform == PlatformCloud {
		reqBody := map[string]interface{}{
			"jql":        jql,
			"startAt":    startAt,
			"maxResults": maxResults,
			"fields":     []string{"*all"},
		}
		var resp SearchResponse
		if err := c.doRequest(ctx, http.MethodPost, "/search/jql", reqBody, &resp); err != nil {
			return nil, err
		}
		resp.StartAt = startAt
		return &resp, nil
	}

	path := fmt.Sprintf("/search?jql=%s&startAt=%d&maxResults=%d", strings.ReplaceAll(jql, " ", "+"), startAt, maxResults)
	var resp SearchResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AddLabel adds a label to an issue.
func (c *Client) AddLabel(ctx context.Context, issueKey, label string) error {
	path := fmt.Sprintf("/issue/%s", issueKey)
	reqBody := map[string]interface{}{
		"update": map[string]interface{}{
			"labels": []map[string]interface{}{
				{"add": label},
			},
		},
	}
	return c.doRequest(ctx, http.MethodPut, path, reqBody, nil)
}

// RemoveLabel removes a label from an issue.
func (c *Client) RemoveLabel(ctx context.Context, issueKey, label string) error {
	path := fmt.Sprintf("/issue/%s", issueKey)
	reqBody := map[string]interface{}{
		"update": map[string]interface{}{
			"labels": []map[string]interface{}{
				{"remove": label},
			},
		},
	}
	return c.doRequest(ctx, http.MethodPut, path, reqBody, nil)
}

// HasLabel checks if an issue has a specific label (case-insensitive).
func (c *Client) HasLabel(issue *Issue, label string) bool {
	for _, l := range issue.Fields.Labels {
		if strings.EqualFold(l, label) {
			return true
		}
	}
	return false
}
