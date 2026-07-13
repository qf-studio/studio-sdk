package github

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// Compile-time interface assertion.
var _ core.SyncCapable = (*SyncClient)(nil)

// issuesSyncPerPage is the page size used by SyncClient's exhaustive issue
// listing, matching the #86 pagination pattern used elsewhere in this package.
const issuesSyncPerPage = 100

// pilotOpMarkerFormat renders the idempotency marker embedded in comment
// bodies posted via SyncClient.AddComment. A retried sync scans existing
// comments for this marker before posting, so a retry never double-posts.
const pilotOpMarkerFormat = "<!-- pilot-op:%s -->"

// SyncClient implements core.SyncCapable for a single GitHub repository. It
// wraps *Client the same way Poller does: bound to one owner/repo pair at
// construction, since GitHub issue numbers (used as IssueSnapshot.NativeID)
// are only unique within a repository.
type SyncClient struct {
	client *Client
	owner  string
	repo   string
}

// NewSyncClient creates a SyncClient bound to owner/repo.
func NewSyncClient(client *Client, owner, repo string) *SyncClient {
	return &SyncClient{client: client, owner: owner, repo: repo}
}

// resolveRepo returns the owner/repo to query for a projectID. projectID is
// expected in "owner/repo" form (matching Config.Repo); if empty or
// malformed, the SyncClient's bound owner/repo is used instead.
func (s *SyncClient) resolveRepo(projectID string) (owner, repo string) {
	if projectID != "" {
		if parts := strings.SplitN(projectID, "/", 2); len(parts) == 2 {
			return parts[0], parts[1]
		}
	}
	return s.owner, s.repo
}

// parseCursor decodes a core.Cursor into a 1-based page number. An empty
// cursor requests the first page.
func parseCursor(page core.Cursor) (int, error) {
	if page == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(string(page))
	if err != nil {
		return 0, fmt.Errorf("invalid cursor %q: %w", page, err)
	}
	return n, nil
}

// parseNativeID decodes an IssueSnapshot.NativeID into a GitHub issue number.
func parseNativeID(nativeID string) (int, error) {
	n, err := strconv.Atoi(nativeID)
	if err != nil {
		return 0, fmt.Errorf("invalid nativeID %q: %w", nativeID, err)
	}
	return n, nil
}

// toSnapshot maps a GitHub Issue onto the normalized core.IssueSnapshot.
// StateGroup mirrors State (open/closed) since GitHub issues have no
// separate state-category concept.
func (s *SyncClient) toSnapshot(issue *Issue) core.IssueSnapshot {
	labels := make([]string, 0, len(issue.Labels))
	for _, l := range issue.Labels {
		labels = append(labels, l.Name)
	}
	assignee := ""
	if issue.Assignee != nil {
		assignee = issue.Assignee.Login
	}
	return core.IssueSnapshot{
		NativeID:   strconv.Itoa(issue.Number),
		SequenceID: "GH-" + strconv.Itoa(issue.Number),
		Title:      issue.Title,
		Body:       issue.Body,
		State:      issue.State,
		StateGroup: issue.State,
		Labels:     labels,
		Priority:   core.NormalizePriority(int(extractPriority(issue.Labels))),
		Assignee:   assignee,
		URL:        issue.HTMLURL,
		CreatedAt:  issue.CreatedAt,
		UpdatedAt:  issue.UpdatedAt,
	}
}

// listIssuesPage fetches a single page of issues, sorted by update time
// ascending, optionally filtered by since. It does not filter out pull
// requests — callers do that, since the raw batch length (including PRs)
// is what determines whether another page exists.
func (s *SyncClient) listIssuesPage(ctx context.Context, owner, repo string, since time.Time, pageNum int) ([]*Issue, error) {
	params := []string{
		"state=all",
		"sort=updated",
		"direction=asc",
		fmt.Sprintf("per_page=%d", issuesSyncPerPage),
		fmt.Sprintf("page=%d", pageNum),
	}
	if !since.IsZero() {
		params = append(params, "since="+since.Format(time.RFC3339))
	}
	path := fmt.Sprintf("/repos/%s/%s/issues?%s", owner, repo, strings.Join(params, "&"))

	var batch []*Issue
	if err := s.client.doRequest(ctx, http.MethodGet, path, nil, &batch); err != nil {
		return nil, err
	}
	return batch, nil
}

// listPage is the shared implementation for ListUpdatedSince and ListAll:
// both are exhaustive, cursor-paginated issue listings that differ only in
// whether a since filter is applied.
func (s *SyncClient) listPage(ctx context.Context, projectID string, since time.Time, page core.Cursor) ([]core.IssueSnapshot, core.Cursor, error) {
	owner, repo := s.resolveRepo(projectID)
	pageNum, err := parseCursor(page)
	if err != nil {
		return nil, "", err
	}

	batch, err := s.listIssuesPage(ctx, owner, repo, since, pageNum)
	if err != nil {
		return nil, "", err
	}

	snapshots := make([]core.IssueSnapshot, 0, len(batch))
	for _, issue := range batch {
		if issue.PullRequest != nil {
			continue // GitHub's /issues endpoint returns PRs too; exclude them.
		}
		snapshots = append(snapshots, s.toSnapshot(issue))
	}

	next := core.Cursor("")
	if len(batch) == issuesSyncPerPage {
		next = core.Cursor(strconv.Itoa(pageNum + 1))
	}
	return snapshots, next, nil
}

// ListUpdatedSince returns issues updated on or after since, oldest first,
// paginated exhaustively at issuesSyncPerPage per page.
func (s *SyncClient) ListUpdatedSince(ctx context.Context, projectID string, since time.Time, page core.Cursor) ([]core.IssueSnapshot, core.Cursor, error) {
	return s.listPage(ctx, projectID, since, page)
}

// ListAll returns every issue in projectID, paginated exhaustively.
func (s *SyncClient) ListAll(ctx context.Context, projectID string, page core.Cursor) ([]core.IssueSnapshot, core.Cursor, error) {
	return s.listPage(ctx, projectID, time.Time{}, page)
}

// GetIssue fetches a single issue by its number (as a decimal string).
func (s *SyncClient) GetIssue(ctx context.Context, nativeID string) (core.IssueSnapshot, error) {
	number, err := parseNativeID(nativeID)
	if err != nil {
		return core.IssueSnapshot{}, err
	}
	issue, err := s.client.GetIssue(ctx, s.owner, s.repo, number)
	if err != nil {
		return core.IssueSnapshot{}, err
	}
	if issue.PullRequest != nil {
		return core.IssueSnapshot{}, fmt.Errorf("nativeID %s is a pull request, not an issue", nativeID)
	}
	return s.toSnapshot(issue), nil
}

// buildIssuePatch converts a core.FieldPatch into the PATCH body accepted by
// GitHub's issues endpoint, validating the field types the SDK contract
// documents (title/body as string, labels as []string).
func buildIssuePatch(fields core.FieldPatch) (map[string]interface{}, error) {
	body := map[string]interface{}{}
	if v, ok := fields["title"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", "title", v)
		}
		body["title"] = s
	}
	if v, ok := fields["body"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", "body", v)
		}
		body["body"] = s
	}
	if v, ok := fields["labels"]; ok {
		labels, ok := v.([]string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected []string, got %T", "labels", v)
		}
		body["labels"] = labels
	}
	return body, nil
}

// UpdateFields applies a partial patch (title/body/labels) to nativeID.
func (s *SyncClient) UpdateFields(ctx context.Context, nativeID string, fields core.FieldPatch) (core.IssueSnapshot, error) {
	number, err := parseNativeID(nativeID)
	if err != nil {
		return core.IssueSnapshot{}, err
	}
	patch, err := buildIssuePatch(fields)
	if err != nil {
		return core.IssueSnapshot{}, err
	}

	path := fmt.Sprintf("/repos/%s/%s/issues/%d", s.owner, s.repo, number)
	var issue Issue
	if err := s.client.doRequest(ctx, http.MethodPatch, path, patch, &issue); err != nil {
		return core.IssueSnapshot{}, err
	}
	return s.toSnapshot(&issue), nil
}

// TransitionState moves nativeID to providerState ("open" or "closed").
func (s *SyncClient) TransitionState(ctx context.Context, nativeID, providerState string) error {
	number, err := parseNativeID(nativeID)
	if err != nil {
		return err
	}
	return s.client.UpdateIssueState(ctx, s.owner, s.repo, number, providerState)
}

// AddComment posts body as a comment on nativeID, embedding an idempotency
// marker derived from idemKey. Before posting, it scans existing comments
// for the marker; if found, it does not repost (safe retry).
func (s *SyncClient) AddComment(ctx context.Context, nativeID, body, idemKey string) error {
	number, err := parseNativeID(nativeID)
	if err != nil {
		return err
	}

	marker := fmt.Sprintf(pilotOpMarkerFormat, idemKey)

	existing, err := s.client.ListIssueComments(ctx, s.owner, s.repo, number)
	if err != nil {
		return err
	}
	for _, c := range existing {
		if strings.Contains(c.Body, marker) {
			return nil // Already posted; retry is a no-op.
		}
	}

	fullBody := body + "\n\n" + marker
	_, err = s.client.AddComment(ctx, s.owner, s.repo, number, fullBody)
	return err
}

// CreateIssue creates a new issue in projectID from draft.
func (s *SyncClient) CreateIssue(ctx context.Context, projectID string, draft core.IssueDraft) (core.IssueSnapshot, error) {
	owner, repo := s.resolveRepo(projectID)
	input := &IssueInput{
		Title:  draft.Title,
		Body:   draft.Body,
		Labels: draft.Labels,
	}
	issue, err := s.client.CreateIssue(ctx, owner, repo, input)
	if err != nil {
		return core.IssueSnapshot{}, err
	}
	return s.toSnapshot(issue), nil
}
