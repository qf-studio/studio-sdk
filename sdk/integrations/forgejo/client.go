package forgejo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a Forgejo/Gitea REST API v1 client.
type Client struct {
	token      string
	baseURL    string
	owner      string
	repo       string
	httpClient *http.Client
}

// ClientOption configures the Client.
type ClientOption func(*Client)

// WithHTTPClient overrides the default HTTP client.
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// NewClient creates a Forgejo client for an "owner/repo" repository at baseURL.
func NewClient(token, ownerRepo, baseURL string, opts ...ClientOption) (*Client, error) {
	parts := strings.Split(strings.Trim(ownerRepo, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("forgejo: project must be owner/repo, got %q", ownerRepo)
	}
	trimmedBase := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmedBase == "" {
		return nil, errors.New("forgejo: baseURL must not be empty")
	}

	c := &Client{
		token:   token,
		baseURL: trimmedBase,
		owner:   parts[0],
		repo:    parts[1],
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

type apiError struct {
	statusCode int
	body       string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("forgejo API error (status %d): %s", e.statusCode, e.body)
}

func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	if body != nil {
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
		return &apiError{statusCode: resp.StatusCode, body: string(respBody)}
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("failed to parse response: %w", err)
		}
	}
	return nil
}

func (c *Client) repoPath(suffix string) string {
	return fmt.Sprintf("/api/v1/repos/%s/%s%s", url.PathEscape(c.owner), url.PathEscape(c.repo), suffix)
}

// GetRepo fetches basic repository metadata (useful for connectivity/auth checks).
func (c *Client) GetRepo(ctx context.Context) (*Repository, error) {
	var repo Repository
	if err := c.doRequest(ctx, http.MethodGet, c.repoPath(""), nil, &repo); err != nil {
		return nil, err
	}
	return &repo, nil
}

// CreatePR implements executor.PRCreator by opening a Forgejo pull request.
// If an open PR already exists for sourceBranch (409 Conflict), it recovers
// and returns the existing PR's HTML URL.
func (c *Client) CreatePR(ctx context.Context, sourceBranch, targetBranch, title, body string) (string, error) {
	req := &CreatePRRequest{
		Title: title,
		Body:  body,
		Head:  sourceBranch,
		Base:  targetBranch,
	}
	var pr PullRequest
	err := c.doRequest(ctx, http.MethodPost, c.repoPath("/pulls"), req, &pr)
	if err == nil {
		return pr.HTMLURL, nil
	}

	var ae *apiError
	if errors.As(err, &ae) && ae.statusCode == http.StatusConflict {
		if existingURL, findErr := c.findOpenPRURL(ctx, sourceBranch); findErr == nil && existingURL != "" {
			return existingURL, nil
		}
	}
	return "", fmt.Errorf("Forgejo PR creation failed: %w", err)
}

func (c *Client) findOpenPRURL(ctx context.Context, sourceBranch string) (string, error) {
	var prs []PullRequest
	if err := c.doRequest(ctx, http.MethodGet, c.repoPath("/pulls?state=open&limit=50"), nil, &prs); err != nil {
		return "", err
	}
	for _, pr := range prs {
		if pr.Head != nil && (pr.Head.Ref == sourceBranch || pr.Head.Label == sourceBranch) {
			return pr.HTMLURL, nil
		}
	}
	return "", nil
}
