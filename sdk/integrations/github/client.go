package github

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

const (
	githubAPIURL     = "https://api.github.com"
	githubGraphQLURL = "https://api.github.com/graphql"
)

// GraphQLRequest is a GitHub GraphQL API request body.
type GraphQLRequest struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

// GraphQLResponse is a GitHub GraphQL API response envelope.
type GraphQLResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []GraphQLError  `json:"errors,omitempty"`
}

// GraphQLError is a single error from a GraphQL response.
type GraphQLError struct {
	Message string        `json:"message"`
	Type    string        `json:"type,omitempty"`
	Path    []interface{} `json:"path,omitempty"`
}

// String renders a GraphQLError with its type/path when present, for diagnostics.
func (e GraphQLError) String() string {
	s := e.Message
	if e.Type != "" {
		s = e.Type + ": " + s
	}
	if len(e.Path) > 0 {
		parts := make([]string, len(e.Path))
		for i, p := range e.Path {
			parts[i] = fmt.Sprintf("%v", p)
		}
		s += " (path: " + strings.Join(parts, ".") + ")"
	}
	return s
}

// PartialGraphQLError is returned by ExecuteGraphQLTolerant when the response
// contains only tolerable per-node errors (NOT_FOUND, FORBIDDEN). Data has been
// unmarshalled into result; callers can inspect the dropped-node details via Errors.
type PartialGraphQLError struct {
	Errors []GraphQLError
}

func (e *PartialGraphQLError) Error() string {
	msgs := make([]string, len(e.Errors))
	for i, ge := range e.Errors {
		msgs[i] = ge.String()
	}
	return "graphql partial error: " + strings.Join(msgs, "; ")
}

// isTolerable reports whether a GraphQL error type is a per-node access error
// safe to skip on a partial board page. Only NOT_FOUND and FORBIDDEN qualify;
// empty Type, RATE_LIMITED, auth, and syntax errors are all fatal.
func isTolerable(errType string) bool {
	return errType == "NOT_FOUND" || errType == "FORBIDDEN"
}

// TokenFunc resolves the bearer token to use for a request. It is invoked
// once per request attempt (including retries), so a rotated token — e.g. a
// GitHub App installation token, which expires hourly — is picked up without
// restarting the client. The context passed is the caller's request context.
type TokenFunc func(ctx context.Context) (string, error)

// Client is a GitHub API client
type Client struct {
	token           string    // static token set by NewClient/NewClientWithBaseURL; unused when tokenFunc is set
	tokenFunc       TokenFunc // per-request token resolver set by NewClientWithTokenFunc; nil for static-token clients
	invalidateToken func()    // optional hook run once on a 401 before a single fresh-resolve retry
	httpClient      *http.Client
	baseURL         string       // For testing - defaults to githubAPIURL
	retryOpts       RetryOptions // Retry config for doRequest; overridable in tests
}

// ClientOption configures a Client created via NewClientWithTokenFunc.
type ClientOption func(*Client)

// WithTokenInvalidate registers a hook that runs once when a request fails
// with 401 Unauthorized, before the client re-resolves the token via
// TokenFunc and retries the request exactly one more time. Use it to evict a
// cached/expired token (e.g. a GitHub App installation token invalidated
// out-of-band) so the retry is guaranteed a fresh value rather than the same
// stale one TokenFunc might otherwise return from its own cache.
func WithTokenInvalidate(fn func()) ClientOption {
	return func(c *Client) {
		c.invalidateToken = fn
	}
}

// WithClientBaseURL overrides the default API base URL (for testing).
func WithClientBaseURL(baseURL string) ClientOption {
	return func(c *Client) {
		c.baseURL = baseURL
	}
}

// NewClient creates a new GitHub client for a fixed, one-shot token. Suitable
// for short-lived processes or PATs; for long-lived daemons under GitHub App
// auth (installation tokens expire hourly) use NewClientWithTokenFunc instead.
func NewClient(token string) *Client {
	return &Client{
		token:     token,
		baseURL:   githubAPIURL,
		retryOpts: DefaultRetryOptions(),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// NewClientWithBaseURL creates a new GitHub client with a custom base URL (for testing).
// Retry is disabled by default so unit tests fail fast; set client.retryOpts to enable.
func NewClientWithBaseURL(token, baseURL string) *Client {
	return &Client{
		token:     token,
		baseURL:   baseURL,
		retryOpts: RetryOptions{MaxRetries: 0},
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// NewClientWithTokenFunc creates a GitHub client that resolves its bearer
// token per request via fn, instead of storing a fixed token at construction.
// This is the constructor long-lived daemons should use under GitHub App
// auth: fn is called inside the retry loop on every attempt, so a token
// rotated between attempts (or between requests over the client's lifetime)
// is picked up automatically. Pair with WithTokenInvalidate to evict a
// cached/expired token on a 401 before the single fresh-resolve retry.
func NewClientWithTokenFunc(fn TokenFunc, opts ...ClientOption) *Client {
	c := &Client{
		tokenFunc: fn,
		baseURL:   githubAPIURL,
		retryOpts: DefaultRetryOptions(),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// resolveToken returns the token to use for the current request attempt:
// the result of tokenFunc(ctx) when the client was built via
// NewClientWithTokenFunc, or the fixed token from NewClient/NewClientWithBaseURL
// otherwise.
func (c *Client) resolveToken(ctx context.Context) (string, error) {
	if c.tokenFunc != nil {
		return c.tokenFunc(ctx)
	}
	return c.token, nil
}

// withAuthRetry runs op once. If op fails with *AuthError (401) and the
// client has an invalidation hook configured via WithTokenInvalidate, it
// invokes the hook and retries op exactly one more time so the retry is
// guaranteed to re-resolve the token rather than replay the same stale value.
// Without an invalidation hook (or for non-auth errors), op's result is
// returned as-is — a bare 401 is not retried, matching isRetryableError's
// "dead token, retrying cannot help" classification.
func (c *Client) withAuthRetry(op func() error) error {
	err := op()
	if err == nil || c.invalidateToken == nil {
		return err
	}
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		return err
	}
	c.invalidateToken()
	return op()
}

// Issue represents a GitHub issue
type Issue struct {
	ID          int64     `json:"id"`
	NodeID      string    `json:"node_id"` // GraphQL global node ID
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	Labels      []Label   `json:"labels"`
	Assignee    *User     `json:"assignee"`
	Assignees   []User    `json:"assignees"`
	User        User      `json:"user"` // Issue author
	HTMLURL     string    `json:"html_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	PullRequest *struct{} `json:"pull_request,omitempty"` // Non-nil when item is a PR (GitHub Issues API returns both)
}

// Label represents a GitHub label
type Label struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
}

// User represents a GitHub user
type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Email string `json:"email,omitempty"`
}

// Repository represents a GitHub repository
type Repository struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Owner    User   `json:"owner"`
	HTMLURL  string `json:"html_url"`
	CloneURL string `json:"clone_url"`
	SSHURL   string `json:"ssh_url"`
}

// Comment represents a GitHub issue comment
type Comment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	User      User      `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// doRequest performs an HTTP request to the GitHub API with automatic retry on
// transient errors (429, 5xx, network failures). The request body is buffered
// once before the retry loop so it can be replayed on each attempt.
func (c *Client) doRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
	}

	return c.withAuthRetry(func() error {
		return WithRetryVoid(ctx, func() error {
			token, err := c.resolveToken(ctx)
			if err != nil {
				return fmt.Errorf("resolve token: %w", err)
			}

			var bodyReader io.Reader
			if bodyBytes != nil {
				bodyReader = bytes.NewReader(bodyBytes)
			}

			req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
			if err != nil {
				return fmt.Errorf("failed to create request: %w", err)
			}

			req.Header.Set("Accept", "application/vnd.github+json")
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
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
				if resp.StatusCode == http.StatusUnauthorized {
					return &AuthError{Message: msg}
				}
				if resp.StatusCode == http.StatusTooManyRequests {
					return &RateLimitError{
						StatusCode: http.StatusTooManyRequests,
						RetryAfter: parseRetryAfterHeader(resp.Header),
						Message:    msg,
					}
				}
				if resp.StatusCode == http.StatusForbidden {
					msgLower := strings.ToLower(msg)
					isRateLimit := resp.Header.Get("X-RateLimit-Remaining") == "0" ||
						strings.Contains(msgLower, "secondary rate limit") ||
						strings.Contains(msgLower, "rate limit exceeded")
					if isRateLimit {
						return &RateLimitError{
							StatusCode: http.StatusForbidden,
							RetryAfter: parseRetryAfterHeader(resp.Header),
							Message:    msg,
						}
					}
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
	})
}

// GetIssue fetches an issue by owner, repo, and number
func (c *Client) GetIssue(ctx context.Context, owner, repo string, number int) (*Issue, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number)
	var issue Issue
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

// ListIssueComments returns comments on an issue or PR.
// Intentionally single-page (no per_page/page params): callers use this for
// small comment threads on individual issues/PRs, not board-scale listing.
func (c *Client) ListIssueComments(ctx context.Context, owner, repo string, number int) ([]*Comment, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number)
	var comments []*Comment
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &comments); err != nil {
		return nil, err
	}
	return comments, nil
}

// AddComment adds a comment to an issue
func (c *Client) AddComment(ctx context.Context, owner, repo string, number int, body string) (*Comment, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number)
	reqBody := map[string]string{"body": body}
	var comment Comment
	if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &comment); err != nil {
		return nil, err
	}
	return &comment, nil
}

// AddLabels adds labels to an issue
func (c *Client) AddLabels(ctx context.Context, owner, repo string, number int, labels []string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels", owner, repo, number)
	reqBody := map[string][]string{"labels": labels}
	return c.doRequest(ctx, http.MethodPost, path, reqBody, nil)
}

// RemoveLabel removes a label from an issue
func (c *Client) RemoveLabel(ctx context.Context, owner, repo string, number int, label string) error {
	// GitHub API is case-sensitive for label names in URL path, normalize to lowercase
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/labels/%s", owner, repo, number, strings.ToLower(label))
	err := c.doRequest(ctx, http.MethodDelete, path, nil, nil)
	// 404 is OK - label might not exist
	if err != nil && err.Error() != "API error (status 404): " {
		return err
	}
	return nil
}

// UpdateIssueState updates an issue's state (open/closed)
func (c *Client) UpdateIssueState(ctx context.Context, owner, repo string, number int, state string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number)
	reqBody := map[string]string{"state": state}
	return c.doRequest(ctx, http.MethodPatch, path, reqBody, nil)
}

// GetRepository fetches repository info
func (c *Client) GetRepository(ctx context.Context, owner, repo string) (*Repository, error) {
	path := fmt.Sprintf("/repos/%s/%s", owner, repo)
	var repository Repository
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &repository); err != nil {
		return nil, err
	}
	return &repository, nil
}

// CreateCommitStatus creates a status for a specific commit SHA
func (c *Client) CreateCommitStatus(ctx context.Context, owner, repo, sha string, status *CommitStatus) (*CommitStatus, error) {
	path := fmt.Sprintf("/repos/%s/%s/statuses/%s", owner, repo, sha)
	var result CommitStatus
	if err := c.doRequest(ctx, http.MethodPost, path, status, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CreateCheckRun creates a check run for the GitHub Checks API
func (c *Client) CreateCheckRun(ctx context.Context, owner, repo string, checkRun *CheckRun) (*CheckRun, error) {
	path := fmt.Sprintf("/repos/%s/%s/check-runs", owner, repo)
	var result CheckRun
	if err := c.doRequest(ctx, http.MethodPost, path, checkRun, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// UpdateCheckRun updates an existing check run
func (c *Client) UpdateCheckRun(ctx context.Context, owner, repo string, checkRunID int64, checkRun *CheckRun) (*CheckRun, error) {
	path := fmt.Sprintf("/repos/%s/%s/check-runs/%d", owner, repo, checkRunID)
	var result CheckRun
	if err := c.doRequest(ctx, http.MethodPatch, path, checkRun, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CreatePullRequest creates a new pull request
func (c *Client) CreatePullRequest(ctx context.Context, owner, repo string, input *PullRequestInput) (*PullRequest, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls", owner, repo)
	var result PullRequest
	if err := c.doRequest(ctx, http.MethodPost, path, input, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// RequestReviewers requests reviewers for a pull request.
func (c *Client) RequestReviewers(ctx context.Context, owner, repo string, number int, reviewers, teamReviewers []string) error {
	if len(reviewers) == 0 && len(teamReviewers) == 0 {
		return nil
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/requested_reviewers", owner, repo, number)
	body := map[string][]string{}
	if len(reviewers) > 0 {
		body["reviewers"] = reviewers
	}
	if len(teamReviewers) > 0 {
		body["team_reviewers"] = teamReviewers
	}
	return c.doRequest(ctx, http.MethodPost, path, body, nil)
}

// GetPullRequest fetches a pull request by number
func (c *Client) GetPullRequest(ctx context.Context, owner, repo string, number int) (*PullRequest, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number)
	var result PullRequest
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ClosePullRequest closes a pull request without merging.
func (c *Client) ClosePullRequest(ctx context.Context, owner, repo string, number int) error {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number)
	payload := map[string]string{"state": "closed"}
	return c.doRequest(ctx, http.MethodPatch, path, payload, nil)
}

// AddPRComment adds a comment to a pull request (issue comment API)
func (c *Client) AddPRComment(ctx context.Context, owner, repo string, number int, body string) (*PRComment, error) {
	// PRs use the issues API for general comments
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number)
	reqBody := map[string]string{"body": body}
	var result PRComment
	if err := c.doRequest(ctx, http.MethodPost, path, reqBody, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ListIssues lists issues for a repository with optional filters.
// Note: Labels are filtered case-insensitively in Go code after fetching,
// because GitHub API label queries are case-sensitive.
// Results are paginated at 100 per page. A safety cap of 50 pages prevents
// runaway loops on very large repos (mirrors ListPullRequests).
func (c *Client) ListIssues(ctx context.Context, owner, repo string, opts *ListIssuesOptions) ([]*Issue, error) {
	const perPage = 100
	const maxPages = 50

	basePath := fmt.Sprintf("/repos/%s/%s/issues?", owner, repo)

	params := []string{}
	var filterLabels []string
	if opts != nil {
		filterLabels = opts.Labels // Save for post-fetch filtering
		if opts.State != "" {
			params = append(params, "state="+opts.State)
		}
		if opts.Sort != "" {
			params = append(params, "sort="+opts.Sort)
		}
		if !opts.Since.IsZero() {
			params = append(params, "since="+opts.Since.Format(time.RFC3339))
		}
	}

	for _, p := range params {
		basePath += p + "&"
	}

	var issues []*Issue
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("%sper_page=%d&page=%d", basePath, perPage, page)
		var batch []*Issue
		if err := c.doRequest(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		issues = append(issues, batch...)
		if len(batch) < perPage {
			break
		}
	}

	// Filter by labels case-insensitively
	if len(filterLabels) > 0 {
		var filtered []*Issue
		for _, issue := range issues {
			hasAllLabels := true
			for _, wantLabel := range filterLabels {
				if !HasLabel(issue, wantLabel) {
					hasAllLabels = false
					break
				}
			}
			if hasAllLabels {
				filtered = append(filtered, issue)
			}
		}
		return filtered, nil
	}

	return issues, nil
}

// HasLabel checks if an issue has a specific label (case-insensitive)
func HasLabel(issue *Issue, labelName string) bool {
	for _, label := range issue.Labels {
		if strings.EqualFold(label.Name, labelName) {
			return true
		}
	}
	return false
}

// MergePullRequest merges a pull request.
// method can be "merge", "squash", or "rebase" (use MergeMethod* constants).
// commitTitle is optional - if empty, GitHub uses the default.
func (c *Client) MergePullRequest(ctx context.Context, owner, repo string, number int, method, commitTitle string) error {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", owner, repo, number)

	body := map[string]string{
		"merge_method": method,
	}
	if commitTitle != "" {
		body["commit_title"] = commitTitle
	}

	return c.doRequest(ctx, http.MethodPut, path, body, nil)
}

// GetCombinedStatus gets combined status for a commit SHA
func (c *Client) GetCombinedStatus(ctx context.Context, owner, repo, sha string) (*CombinedStatus, error) {
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/status", owner, repo, sha)

	var status CombinedStatus
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &status); err != nil {
		return nil, err
	}

	return &status, nil
}

// ListCheckRuns lists check runs for a commit SHA
func (c *Client) ListCheckRuns(ctx context.Context, owner, repo, sha string) (*CheckRunsResponse, error) {
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs", owner, repo, sha)

	var result CheckRunsResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// ApprovePullRequest creates an approval review on a PR
func (c *Client) ApprovePullRequest(ctx context.Context, owner, repo string, number int, body string) error {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, number)

	payload := map[string]string{
		"event": ReviewEventApprove,
	}
	if body != "" {
		payload["body"] = body
	}

	return c.doRequest(ctx, http.MethodPost, path, payload, nil)
}

// IssueInput is the input for creating a new issue
type IssueInput struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels,omitempty"`
}

// CreateIssue creates a new issue in a repository
func (c *Client) CreateIssue(ctx context.Context, owner, repo string, input *IssueInput) (*Issue, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues", owner, repo)
	var issue Issue
	if err := c.doRequest(ctx, http.MethodPost, path, input, &issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

// GetBranch fetches information about a branch
func (c *Client) GetBranch(ctx context.Context, owner, repo, branch string) (*Branch, error) {
	path := fmt.Sprintf("/repos/%s/%s/branches/%s", owner, repo, url.PathEscape(branch))
	var result Branch
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ListPullRequests lists pull requests for a repository.
// state can be "open", "closed", or "all".
// Results are paginated at 100 per page. A safety cap of 50 pages prevents
// runaway loops on very large repos.
func (c *Client) ListPullRequests(ctx context.Context, owner, repo, state string) ([]*PullRequest, error) {
	const perPage = 100
	const maxPages = 50

	var all []*PullRequest
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("/repos/%s/%s/pulls?state=%s&per_page=%d&page=%d", owner, repo, state, perPage, page)
		var batch []*PullRequest
		if err := c.doRequest(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < perPage {
			break
		}
	}
	return all, nil
}

// CreateRelease creates a new release
func (c *Client) CreateRelease(ctx context.Context, owner, repo string, input *ReleaseInput) (*Release, error) {
	path := fmt.Sprintf("/repos/%s/%s/releases", owner, repo)
	var result Release
	if err := c.doRequest(ctx, http.MethodPost, path, input, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// CreateGitTag creates a lightweight git tag via the GitHub API.
func (c *Client) CreateGitTag(ctx context.Context, owner, repo, tag, sha string) error {
	path := fmt.Sprintf("/repos/%s/%s/git/refs", owner, repo)
	body := map[string]string{
		"ref": "refs/tags/" + tag,
		"sha": sha,
	}
	return c.doRequest(ctx, http.MethodPost, path, body, nil)
}

// GetLatestRelease gets the latest published release.
// Returns nil, nil if no releases exist.
func (c *Client) GetLatestRelease(ctx context.Context, owner, repo string) (*Release, error) {
	path := fmt.Sprintf("/repos/%s/%s/releases/latest", owner, repo)
	var result Release
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		// 404 means no releases exist - return nil, nil
		if isNotFoundError(err) {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

// GetReleaseByTag fetches a release by its tag name.
// Returns nil, nil if no release exists for the given tag (404).
func (c *Client) GetReleaseByTag(ctx context.Context, owner, repo, tag string) (*Release, error) {
	path := fmt.Sprintf("/repos/%s/%s/releases/tags/%s", owner, repo, tag)
	var result Release
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		if isNotFoundError(err) {
			return nil, nil
		}
		return nil, err
	}
	return &result, nil
}

// UpdateRelease updates an existing release.
func (c *Client) UpdateRelease(ctx context.Context, owner, repo string, releaseID int64, input *ReleaseInput) (*Release, error) {
	path := fmt.Sprintf("/repos/%s/%s/releases/%d", owner, repo, releaseID)
	var result Release
	if err := c.doRequest(ctx, http.MethodPatch, path, input, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ListReleases lists releases for a repository (newest first).
// Paginates exhaustively using perPage as the page size, up to a 50-page
// safety cap (mirrors ListPullRequests) — a single page silently truncated
// releases in repos with more than perPage entries.
func (c *Client) ListReleases(ctx context.Context, owner, repo string, perPage int) ([]*Release, error) {
	const maxPages = 50

	var all []*Release
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("/repos/%s/%s/releases?per_page=%d&page=%d", owner, repo, perPage, page)
		var batch []*Release
		if err := c.doRequest(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < perPage {
			break
		}
	}
	return all, nil
}

// ListTags lists repository tags (newest first).
// Paginates exhaustively using perPage as the page size, up to a 50-page
// safety cap (mirrors ListPullRequests) — a single page silently truncated
// tags in repos with more than perPage entries.
func (c *Client) ListTags(ctx context.Context, owner, repo string, perPage int) ([]*Tag, error) {
	const maxPages = 50

	var all []*Tag
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("/repos/%s/%s/tags?per_page=%d&page=%d", owner, repo, perPage, page)
		var batch []*Tag
		if err := c.doRequest(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < perPage {
			break
		}
	}
	return all, nil
}

// GetTagForSHA returns the tag name if a tag exists at the given SHA, or empty
// string if none. Paginates exhaustively (per_page=100, up to 50 pages) — a
// bounded lookup misses tags beyond the first page and stalls release draining
// for SHAs tagged long ago (caught by Pilot's TestHandleReleasing_ExhaustiveTagDrain).
func (c *Client) GetTagForSHA(ctx context.Context, owner, repo, sha string) (string, error) {
	const perPage = 100
	const maxPages = 50
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("/repos/%s/%s/tags?per_page=%d&page=%d", owner, repo, perPage, page)
		var batch []*Tag
		if err := c.doRequest(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return "", err
		}
		for _, tag := range batch {
			if tag.Commit.SHA == sha {
				return tag.Name, nil
			}
		}
		if len(batch) < perPage {
			break
		}
	}
	return "", nil
}

// GetPRCommits returns all commits in a pull request
func (c *Client) GetPRCommits(ctx context.Context, owner, repo string, prNumber int) ([]*Commit, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/commits?per_page=100", owner, repo, prNumber)
	var result []*Commit
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// CompareCommits compares two commits and returns commits between them
func (c *Client) CompareCommits(ctx context.Context, owner, repo, base, head string) ([]*Commit, error) {
	path := fmt.Sprintf("/repos/%s/%s/compare/%s...%s", owner, repo, base, head)
	var result struct {
		Commits []*Commit `json:"commits"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result.Commits, nil
}

// CompareStatus returns GitHub's relationship of head to base for base...head:
// one of "ahead", "behind", "identical", or "diverged". It is the cheapest way
// to ask "is base an ancestor of head?" — base...head is "ahead" when head
// contains base plus more commits, and "identical" when they are the same
// commit. Used to detect a commit already covered by an existing release tag.
func (c *Client) CompareStatus(ctx context.Context, owner, repo, base, head string) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/compare/%s...%s", owner, repo, base, head)
	var result struct {
		Status string `json:"status"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return "", err
	}
	return result.Status, nil
}

// GetJobLogs fetches the logs for a GitHub Actions job (check run).
// Uses GET /repos/{owner}/{repo}/actions/jobs/{job_id}/logs which returns
// a 302 redirect to a log download URL. Returns the raw log text.
func (c *Client) GetJobLogs(ctx context.Context, owner, repo string, jobID int64) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/jobs/%d/logs", owner, repo, jobID)

	token, err := c.resolveToken(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("API error (status %d) fetching job logs", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read log response: %w", err)
	}

	return string(body), nil
}

// isNotFoundError checks if error is a 404 not found error
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return len(errStr) >= 21 && errStr[:21] == "API error (status 404"
}

// isUnprocessableError checks if error is a 422 unprocessable entity error
func isUnprocessableError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return len(errStr) >= 21 && errStr[:21] == "API error (status 422"
}

// UpdateRef creates or updates a branch ref to point at the given SHA.
// Uses PATCH /repos/{owner}/{repo}/git/refs/heads/{branch} with force=true.
// If the ref does not exist, falls back to creating it via POST.
func (c *Client) UpdateRef(ctx context.Context, owner, repo, branch, sha string) error {
	path := fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", owner, repo, url.PathEscape(branch))
	body := map[string]interface{}{
		"sha":   sha,
		"force": true,
	}
	err := c.doRequest(ctx, http.MethodPatch, path, body, nil)
	if err == nil {
		return nil
	}
	// If the ref doesn't exist yet, create it
	if isUnprocessableError(err) || isNotFoundError(err) {
		createPath := fmt.Sprintf("/repos/%s/%s/git/refs", owner, repo)
		createBody := map[string]string{
			"ref": "refs/heads/" + branch,
			"sha": sha,
		}
		return c.doRequest(ctx, http.MethodPost, createPath, createBody, nil)
	}
	return err
}

// DeleteBranch deletes a branch from the repository.
// Returns nil on success, or if the branch was already deleted (404/422).
func (c *Client) DeleteBranch(ctx context.Context, owner, repo, branch string) error {
	path := fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", owner, repo, url.PathEscape(branch))
	err := c.doRequest(ctx, http.MethodDelete, path, nil, nil)
	// 404 = branch doesn't exist, 422 = branch already deleted
	if isNotFoundError(err) || isUnprocessableError(err) {
		return nil
	}
	return err
}

// ListPullRequestReviews lists all reviews for a pull request
func (c *Client) ListPullRequestReviews(ctx context.Context, owner, repo string, number int) ([]*PullRequestReview, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, number)
	var result []*PullRequestReview
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// PRFile represents a file changed in a pull request.
type PRFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"` // "added", "removed", "modified", "renamed"
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// ListPullRequestFiles returns the list of files changed in a pull request.
func (c *Client) ListPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]*PRFile, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/files", owner, repo, number)
	var result []*PRFile
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// HasApprovalReview checks if a PR has at least one approval review.
// Returns (hasApproval, approverLogin, error).
// Only considers the latest review from each user.
func (c *Client) HasApprovalReview(ctx context.Context, owner, repo string, number int) (bool, string, error) {
	reviews, err := c.ListPullRequestReviews(ctx, owner, repo, number)
	if err != nil {
		return false, "", err
	}

	// Track latest review state per user
	latestReviews := make(map[string]string) // user login -> state
	for _, review := range reviews {
		latestReviews[review.User.Login] = review.State
	}

	// Check if any user's latest review is APPROVED
	for login, state := range latestReviews {
		if state == ReviewStateApproved {
			return true, login, nil
		}
	}

	return false, "", nil
}

// GetPullRequestComments returns line-level review comments on a pull request.
// Intentionally single-page (per_page=100, no page loop): review comment
// counts on a single PR stay well under 100 in practice, unlike board-scale
// issue/release/tag listings.
func (c *Client) GetPullRequestComments(ctx context.Context, owner, repo string, number int) ([]*PRReviewComment, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments?per_page=100", owner, repo, number)
	var result []*PRReviewComment
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// ExecuteGraphQL executes a GitHub GraphQL query or mutation.
// Posts to baseURL+"/graphql" (testable via NewClientWithBaseURL).
// result is unmarshalled from response.data if non-nil.
// Transient transport errors (5xx, network) and GraphQL-level rate limits
// (HTTP 200 + RATE_LIMITED / "was submitted too quickly") are retried via
// c.retryOpts, matching the behaviour of doRequest.
// Any GraphQL error (regardless of type) aborts the call; use
// ExecuteGraphQLTolerant for board pagination that must survive per-node
// NOT_FOUND/FORBIDDEN errors.
func (c *Client) ExecuteGraphQL(ctx context.Context, query string, variables map[string]interface{}, result interface{}) error {
	return c.executeGraphQLCore(ctx, query, variables, result, false)
}

// ExecuteGraphQLTolerant is like ExecuteGraphQL but tolerates per-node
// NOT_FOUND/FORBIDDEN errors in partial responses. When all errors are tolerable
// it unmarshals Data into result and returns *PartialGraphQLError so the caller
// can log/count the dropped nodes. A single non-tolerable error (e.g. RATE_LIMITED,
// empty Type, auth, syntax) causes the whole call to fail exactly as ExecuteGraphQL
// would, without unmarshalling Data.
func (c *Client) ExecuteGraphQLTolerant(ctx context.Context, query string, variables map[string]interface{}, result interface{}) error {
	return c.executeGraphQLCore(ctx, query, variables, result, true)
}

// executeGraphQLCore is the shared implementation for ExecuteGraphQL and
// ExecuteGraphQLTolerant. When tolerant=false any error in the response is
// fatal (strict mode). When tolerant=true, a response whose errors are all
// NOT_FOUND/FORBIDDEN has its data unmarshalled and a *PartialGraphQLError
// is returned; a single non-tolerable error makes the whole call fatal.
func (c *Client) executeGraphQLCore(ctx context.Context, query string, variables map[string]interface{}, result interface{}, tolerant bool) error {
	reqBody := GraphQLRequest{Query: query, Variables: variables}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal graphql request: %w", err)
	}

	endpoint := c.baseURL + "/graphql"

	return c.withAuthRetry(func() error {
		return WithRetryVoid(ctx, func() error {
			token, err := c.resolveToken(ctx)
			if err != nil {
				return fmt.Errorf("resolve token: %w", err)
			}

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
			if err != nil {
				return fmt.Errorf("create graphql request: %w", err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")

			resp, err := c.httpClient.Do(req)
			if err != nil {
				return fmt.Errorf("graphql request failed: %w", err)
			}
			defer func() { _ = resp.Body.Close() }()

			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				return fmt.Errorf("read graphql response: %w", err)
			}

			if resp.StatusCode == http.StatusUnauthorized {
				return &AuthError{Message: string(respBody)}
			}
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("graphql API error (status %d): %s", resp.StatusCode, string(respBody))
			}

			var gqlResp GraphQLResponse
			if err := json.Unmarshal(respBody, &gqlResp); err != nil {
				return fmt.Errorf("parse graphql response: %w", err)
			}

			if len(gqlResp.Errors) > 0 {
				// Aggregate ALL errors (message + type + path), not just Errors[0].
				// GitHub Projects V2 frequently returns several per-node errors at
				// once, and surfacing only the first made board flows hard to diagnose.
				//
				// In tolerant mode: if all errors are NOT_FOUND/FORBIDDEN, unmarshal
				// the good data and return *PartialGraphQLError. Any non-tolerable error
				// (including mixed tolerable+fatal) makes the whole response fatal.
				if tolerant {
					allTolerable := true
					for _, ge := range gqlResp.Errors {
						if !isTolerable(ge.Type) {
							allTolerable = false
							break
						}
					}
					if allTolerable {
						if result != nil && len(gqlResp.Data) > 0 {
							if err := json.Unmarshal(gqlResp.Data, result); err != nil {
								return fmt.Errorf("unmarshal graphql data: %w", err)
							}
						}
						return &PartialGraphQLError{Errors: gqlResp.Errors}
					}
				}

				msgs := make([]string, len(gqlResp.Errors))
				for i, ge := range gqlResp.Errors {
					msgs[i] = ge.String()
				}
				return fmt.Errorf("graphql error: %s", strings.Join(msgs, "; "))
			}

			if result != nil && len(gqlResp.Data) > 0 {
				if err := json.Unmarshal(gqlResp.Data, result); err != nil {
					return fmt.Errorf("unmarshal graphql data: %w", err)
				}
			}

			return nil
		}, c.retryOpts)
	})
}

// SearchPRsForIssue returns all PRs that reference the given issue number.
// Intentionally single-page (per_page=100, no page loop): the Search API's
// own result cap (1000 items) makes this a bounded lookup for one issue's
// referencing PRs, not the kind of board-scale listing ListIssues covers.
func (c *Client) SearchPRsForIssue(ctx context.Context, owner, repo string, issueNumber int) ([]*PullRequest, error) {
	q := fmt.Sprintf("repo:%s/%s is:pr #%d", owner, repo, issueNumber)
	path := fmt.Sprintf("/search/issues?q=%s&per_page=100", url.QueryEscape(q))

	var result struct {
		Items []struct {
			ID          int64  `json:"id"`
			Number      int    `json:"number"`
			Title       string `json:"title"`
			State       string `json:"state"`
			HTMLURL     string `json:"html_url"`
			PullRequest *struct {
				MergedAt string `json:"merged_at"`
			} `json:"pull_request"`
		} `json:"items"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("search PRs for issue #%d: %w", issueNumber, err)
	}

	prs := make([]*PullRequest, 0, len(result.Items))
	for _, item := range result.Items {
		pr := &PullRequest{
			ID:      item.ID,
			Number:  item.Number,
			Title:   item.Title,
			State:   item.State,
			HTMLURL: item.HTMLURL,
		}
		if item.PullRequest != nil && item.PullRequest.MergedAt != "" {
			pr.MergedAt = item.PullRequest.MergedAt
			pr.Merged = true
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

// SearchOpenPRsForIssue returns open PRs that reference the given issue number.
// The returned PullRequest values include the User (author) field so callers can
// distinguish Pilot-bot PRs from human recovery PRs.
// Intentionally single-page (per_page=100, no page loop): open PRs against a
// single issue are a small, bounded set, unlike board-scale listing.
func (c *Client) SearchOpenPRsForIssue(ctx context.Context, owner, repo string, issueNumber int) ([]*PullRequest, error) {
	q := fmt.Sprintf("repo:%s/%s is:pr is:open #%d", owner, repo, issueNumber)
	path := fmt.Sprintf("/search/issues?q=%s&per_page=100", url.QueryEscape(q))

	var result struct {
		Items []struct {
			ID      int64  `json:"id"`
			Number  int    `json:"number"`
			Title   string `json:"title"`
			State   string `json:"state"`
			HTMLURL string `json:"html_url"`
			User    *User  `json:"user"`
		} `json:"items"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("search open PRs for issue #%d: %w", issueNumber, err)
	}

	prs := make([]*PullRequest, 0, len(result.Items))
	for _, item := range result.Items {
		prs = append(prs, &PullRequest{
			ID:      item.ID,
			Number:  item.Number,
			Title:   item.Title,
			State:   item.State,
			HTMLURL: item.HTMLURL,
			User:    item.User,
		})
	}
	return prs, nil
}

// GetAuthenticatedUser returns the GitHub user associated with the current token.
func (c *Client) GetAuthenticatedUser(ctx context.Context) (*User, error) {
	var user User
	if err := c.doRequest(ctx, http.MethodGet, "/user", nil, &user); err != nil {
		return nil, fmt.Errorf("get authenticated user: %w", err)
	}
	return &user, nil
}

// SearchMergedPRsForIssue checks if any merged PRs exist that reference the given
// issue number in their title. Returns true if at least one merged PR is found.
func (c *Client) SearchMergedPRsForIssue(ctx context.Context, owner, repo string, issueNumber int) (bool, error) {
	q := fmt.Sprintf("repo:%s/%s GH-%d in:title is:pr is:merged", owner, repo, issueNumber)
	path := fmt.Sprintf("/search/issues?q=%s&per_page=1", url.QueryEscape(q))

	var result struct {
		TotalCount int `json:"total_count"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return false, fmt.Errorf("search merged PRs for issue %d: %w", issueNumber, err)
	}
	return result.TotalCount > 0, nil
}

// FindMergedPRByBranch looks up PRs by head branch via the strongly-consistent REST API.
// Returns true if any PR on that branch is merged.
func (c *Client) FindMergedPRByBranch(ctx context.Context, owner, repo, branch string) (bool, error) {
	head := fmt.Sprintf("%s:%s", owner, branch)
	path := fmt.Sprintf("/repos/%s/%s/pulls?head=%s&state=closed&per_page=10",
		owner, repo, url.QueryEscape(head))

	var prs []*PullRequest
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &prs); err != nil {
		return false, fmt.Errorf("list PRs by branch %s: %w", branch, err)
	}
	for _, pr := range prs {
		if pr.MergedAt != "" || pr.Merged {
			return true, nil
		}
	}
	return false, nil
}

// FindOpenPRByBranch looks up OPEN PRs by head branch via the strongly-consistent
// REST API (no Search API indexing lag). Returns true if any open PR exists on
// that branch — the counterpart to FindMergedPRByBranch for the
// PR-created-but-not-yet-merged window.
func (c *Client) FindOpenPRByBranch(ctx context.Context, owner, repo, branch string) (bool, error) {
	head := fmt.Sprintf("%s:%s", owner, branch)
	path := fmt.Sprintf("/repos/%s/%s/pulls?head=%s&state=open&per_page=10",
		owner, repo, url.QueryEscape(head))

	var prs []*PullRequest
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &prs); err != nil {
		return false, fmt.Errorf("list open PRs by branch %s: %w", branch, err)
	}
	// The server filters by head=owner:branch&state=open, so a matching PR has
	// state=="open" and head.ref==branch. Re-check both rather than trusting the
	// array length, mirroring FindMergedPRByBranch's field inspection.
	for _, pr := range prs {
		if pr.State == "open" && pr.Head.Ref == branch {
			return true, nil
		}
	}
	return false, nil
}

// SearchOpenSubIssues counts open issues in a repo whose body contains "Parent: GH-{parentNum}".
func (c *Client) SearchOpenSubIssues(ctx context.Context, owner, repo string, parentNum int) (int, error) {
	q := fmt.Sprintf(`repo:%s/%s "Parent: GH-%d" is:issue is:open`, owner, repo, parentNum)
	path := fmt.Sprintf("/search/issues?q=%s&per_page=1", url.QueryEscape(q))

	var result struct {
		TotalCount int `json:"total_count"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &result); err != nil {
		return 0, fmt.Errorf("search open sub-issues for parent %d: %w", parentNum, err)
	}
	return result.TotalCount, nil
}

// UpdatePullRequestBranch updates the PR branch with the latest base branch.
func (c *Client) UpdatePullRequestBranch(ctx context.Context, owner, repo string, number int) error {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/update-branch", owner, repo, number)
	body := map[string]interface{}{}
	return c.doRequest(ctx, http.MethodPut, path, body, nil)
}

// GetIssueNodeID fetches the GraphQL node ID for a given issue number via the REST API.
func (c *Client) GetIssueNodeID(ctx context.Context, owner, repo string, number int) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number)
	var issue struct {
		NodeID string `json:"node_id"`
	}
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &issue); err != nil {
		return "", fmt.Errorf("get issue node ID for %s/%s#%d: %w", owner, repo, number, err)
	}
	if issue.NodeID == "" {
		return "", fmt.Errorf("issue %s/%s#%d returned empty node_id", owner, repo, number)
	}
	return issue.NodeID, nil
}

// LinkSubIssue links a child issue to a parent issue using the addSubIssue GraphQL mutation.
func (c *Client) LinkSubIssue(ctx context.Context, owner, repo string, parentNum, childNum int) error {
	parentID, err := c.GetIssueNodeID(ctx, owner, repo, parentNum)
	if err != nil {
		return fmt.Errorf("resolve parent node ID: %w", err)
	}
	childID, err := c.GetIssueNodeID(ctx, owner, repo, childNum)
	if err != nil {
		return fmt.Errorf("resolve child node ID: %w", err)
	}

	const mutation = `mutation($parentID: ID!, $childID: ID!) {
		addSubIssue(input: {issueId: $parentID, subIssueId: $childID}) {
			issue { id }
			subIssue { id }
		}
	}`

	variables := map[string]interface{}{
		"parentID": parentID,
		"childID":  childID,
	}
	return c.ExecuteGraphQL(ctx, mutation, variables, nil)
}

// GetOpenSubIssueNumbers queries native GitHub sub-issues for a parent issue
// and returns the numbers of sub-issues in OPEN state plus whether the parent
// has any native sub-issue links at all. Callers that need to cross-check each
// open child individually (rather than just count them) use this variant.
func (c *Client) GetOpenSubIssueNumbers(ctx context.Context, owner, repo string, parentNum int) (numbers []int, hasNativeLinks bool, err error) {
	parentID, err := c.GetIssueNodeID(ctx, owner, repo, parentNum)
	if err != nil {
		return nil, false, fmt.Errorf("resolve parent node ID: %w", err)
	}

	const query = `query($issueID: ID!) {
		node(id: $issueID) {
			... on Issue {
				subIssues(first: 100) {
					totalCount
					nodes {
						number
						state
					}
				}
			}
		}
	}`

	var result struct {
		Node struct {
			SubIssues struct {
				TotalCount int `json:"totalCount"`
				Nodes      []struct {
					Number int    `json:"number"`
					State  string `json:"state"`
				} `json:"nodes"`
			} `json:"subIssues"`
		} `json:"node"`
	}

	if err := c.ExecuteGraphQL(ctx, query, map[string]interface{}{"issueID": parentID}, &result); err != nil {
		return nil, false, fmt.Errorf("query sub-issues for %s/%s#%d: %w", owner, repo, parentNum, err)
	}

	if result.Node.SubIssues.TotalCount == 0 {
		return nil, false, nil
	}

	for _, n := range result.Node.SubIssues.Nodes {
		if n.State == "OPEN" {
			numbers = append(numbers, n.Number)
		}
	}
	return numbers, true, nil
}

// GetOpenSubIssueCount queries native GitHub sub-issues for a parent issue and returns:
//   - count: number of sub-issues in OPEN state
//   - hasNativeLinks: true when the parent has at least one native sub-issue link
//   - error: any API or parsing error
func (c *Client) GetOpenSubIssueCount(ctx context.Context, owner, repo string, parentNum int) (count int, hasNativeLinks bool, err error) {
	parentID, err := c.GetIssueNodeID(ctx, owner, repo, parentNum)
	if err != nil {
		return 0, false, fmt.Errorf("resolve parent node ID: %w", err)
	}

	const query = `query($issueID: ID!) {
		node(id: $issueID) {
			... on Issue {
				subIssues(first: 100) {
					totalCount
					nodes {
						state
					}
				}
			}
		}
	}`

	var result struct {
		Node struct {
			SubIssues struct {
				TotalCount int `json:"totalCount"`
				Nodes      []struct {
					State string `json:"state"`
				} `json:"nodes"`
			} `json:"subIssues"`
		} `json:"node"`
	}

	if err := c.ExecuteGraphQL(ctx, query, map[string]interface{}{"issueID": parentID}, &result); err != nil {
		return 0, false, fmt.Errorf("query sub-issues for %s/%s#%d: %w", owner, repo, parentNum, err)
	}

	if result.Node.SubIssues.TotalCount == 0 {
		return 0, false, nil
	}

	openCount := 0
	for _, n := range result.Node.SubIssues.Nodes {
		if n.State == "OPEN" {
			openCount++
		}
	}
	return openCount, true, nil
}

// SearchOpenPilotIssuesWithSubIssues returns issue numbers for open issues labeled with
// the trigger label that have at least one sub-issue.
// limit controls the maximum number of issues fetched from the API.
func (c *Client) SearchOpenPilotIssuesWithSubIssues(ctx context.Context, owner, repo string, limit int) ([]int, error) {
	const query = `query($owner: String!, $repo: String!, $first: Int!) {
		repository(owner: $owner, name: $repo) {
			issues(first: $first, states: [OPEN], labels: ["pilot"]) {
				nodes {
					number
					subIssuesSummary {
						total
						completed
					}
				}
			}
		}
	}`

	var result struct {
		Repository struct {
			Issues struct {
				Nodes []struct {
					Number           int `json:"number"`
					SubIssuesSummary struct {
						Total     int `json:"total"`
						Completed int `json:"completed"`
					} `json:"subIssuesSummary"`
				} `json:"nodes"`
			} `json:"issues"`
		} `json:"repository"`
	}

	variables := map[string]interface{}{
		"owner": owner,
		"repo":  repo,
		"first": limit,
	}

	if err := c.ExecuteGraphQL(ctx, query, variables, &result); err != nil {
		return nil, fmt.Errorf("search issues with sub-issues for %s/%s: %w", owner, repo, err)
	}

	var numbers []int
	for _, node := range result.Repository.Issues.Nodes {
		if node.SubIssuesSummary.Total > 0 {
			numbers = append(numbers, node.Number)
		}
	}
	return numbers, nil
}
