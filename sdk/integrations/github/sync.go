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
// ListUpdatedSince/ListAll follow this cursor to exhaustion with no page cap
// (unlike the host-facing ListIssues/ListReleases/ListTags, which impose a
// safety cap) — bounding the number of pages fetched per sync pass is the
// host's responsibility, since the shadow-diff engine already tracks a
// resumable cursor across calls.
const issuesSyncPerPage = 100

// commentScanPerPage is the page size used by SyncClient.AddComment's
// idempotency scan. It deliberately does not reuse Client.ListIssueComments,
// which is documented single-page (GitHub's default 30 comments) — a
// pilot-op marker posted beyond the first 30 comments would be invisible to
// a retry, causing a duplicate repost.
const commentScanPerPage = 100

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
// expected in "owner/repo" form (matching Config.Repo); an empty projectID
// resolves to the SyncClient's bound owner/repo. A non-empty projectID that
// isn't valid "owner/repo" form is rejected with an error rather than
// silently falling back to the bound repo — a malformed override should
// never be mistaken for "use the default".
func (s *SyncClient) resolveRepo(projectID string) (owner, repo string, err error) {
	if projectID == "" {
		return s.owner, s.repo, nil
	}
	parts := strings.SplitN(projectID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid projectID %q: expected \"owner/repo\"", projectID)
	}
	return parts[0], parts[1], nil
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

// parseNativeID decodes an IssueSnapshot.NativeID into the owner/repo/number
// it refers to. Two forms are accepted:
//   - a bare decimal number ("42"), which resolves to the SyncClient's bound
//     repo — the common case, and the format toSnapshot emits when a
//     snapshot was produced for the bound repo.
//   - "owner/repo#number" (e.g. "other-owner/other-repo#42"), which
//     toSnapshot emits when a snapshot was produced via a projectID override
//     (see resolveRepo) that resolved to a repo other than the bound one.
//
// This is what lets GetIssue/UpdateFields/TransitionState/AddComment — whose
// core.SyncWriter/SyncSource signatures carry only a nativeID, not a
// projectID — route a write-back to the same repo a listing call actually
// read the issue from, instead of silently hitting the bound repo's
// same-numbered issue.
func (s *SyncClient) parseNativeID(nativeID string) (owner, repo string, number int, err error) {
	if idx := strings.LastIndex(nativeID, "#"); idx != -1 {
		repoPart, numPart := nativeID[:idx], nativeID[idx+1:]
		parts := strings.SplitN(repoPart, "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", 0, fmt.Errorf("invalid nativeID %q: malformed repo prefix", nativeID)
		}
		n, err := strconv.Atoi(numPart)
		if err != nil {
			return "", "", 0, fmt.Errorf("invalid nativeID %q: %w", nativeID, err)
		}
		return parts[0], parts[1], n, nil
	}
	n, err := strconv.Atoi(nativeID)
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid nativeID %q: %w", nativeID, err)
	}
	return s.owner, s.repo, n, nil
}

// toSnapshot maps a GitHub Issue fetched from owner/repo onto the normalized
// core.IssueSnapshot. StateGroup mirrors State (open/closed) since GitHub
// issues have no separate state-category concept. NativeID is a bare issue
// number when owner/repo is the SyncClient's bound repo, or "owner/repo#N"
// when it was fetched via a projectID override — see parseNativeID.
func (s *SyncClient) toSnapshot(owner, repo string, issue *Issue) core.IssueSnapshot {
	labels := make([]string, 0, len(issue.Labels))
	for _, l := range issue.Labels {
		labels = append(labels, l.Name)
	}
	assignee := ""
	if issue.Assignee != nil {
		assignee = issue.Assignee.Login
	}
	nativeID := strconv.Itoa(issue.Number)
	if owner != s.owner || repo != s.repo {
		nativeID = fmt.Sprintf("%s/%s#%d", owner, repo, issue.Number)
	}
	return core.IssueSnapshot{
		NativeID:   nativeID,
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
		// UTC before formatting: a non-UTC offset (e.g. "+02:00") embeds a
		// literal "+" in the query string, which decodes as a space and
		// corrupts the since filter server-side.
		params = append(params, "since="+since.UTC().Format(time.RFC3339))
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
	owner, repo, err := s.resolveRepo(projectID)
	if err != nil {
		return nil, "", err
	}
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
		snapshots = append(snapshots, s.toSnapshot(owner, repo, issue))
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

// GetIssue fetches a single issue by nativeID (see parseNativeID for the
// accepted forms).
func (s *SyncClient) GetIssue(ctx context.Context, nativeID string) (core.IssueSnapshot, error) {
	owner, repo, number, err := s.parseNativeID(nativeID)
	if err != nil {
		return core.IssueSnapshot{}, err
	}
	issue, err := s.client.GetIssue(ctx, owner, repo, number)
	if err != nil {
		return core.IssueSnapshot{}, err
	}
	if issue.PullRequest != nil {
		return core.IssueSnapshot{}, fmt.Errorf("nativeID %s is a pull request, not an issue", nativeID)
	}
	return s.toSnapshot(owner, repo, issue), nil
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
	owner, repo, number, err := s.parseNativeID(nativeID)
	if err != nil {
		return core.IssueSnapshot{}, err
	}
	patch, err := buildIssuePatch(fields)
	if err != nil {
		return core.IssueSnapshot{}, err
	}

	path := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number)
	var issue Issue
	if err := s.client.doRequest(ctx, http.MethodPatch, path, patch, &issue); err != nil {
		return core.IssueSnapshot{}, err
	}
	return s.toSnapshot(owner, repo, &issue), nil
}

// TransitionState moves nativeID to providerState ("open" or "closed").
func (s *SyncClient) TransitionState(ctx context.Context, nativeID, providerState string) error {
	owner, repo, number, err := s.parseNativeID(nativeID)
	if err != nil {
		return err
	}
	return s.client.UpdateIssueState(ctx, owner, repo, number, providerState)
}

// hasCommentMarker scans nativeID's comments for marker, paginating
// exhaustively (commentScanPerPage per page, no cap) rather than relying on
// Client.ListIssueComments' documented single default-sized page — a marker
// posted beyond the first page would otherwise be invisible to a retry,
// causing AddComment to double-post.
func (s *SyncClient) hasCommentMarker(ctx context.Context, owner, repo string, number int, marker string) (bool, error) {
	for page := 1; ; page++ {
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=%d&page=%d", owner, repo, number, commentScanPerPage, page)
		var batch []*Comment
		if err := s.client.doRequest(ctx, http.MethodGet, path, nil, &batch); err != nil {
			return false, err
		}
		for _, c := range batch {
			if strings.Contains(c.Body, marker) {
				return true, nil
			}
		}
		if len(batch) < commentScanPerPage {
			return false, nil
		}
	}
}

// AddComment posts body as a comment on nativeID, embedding an idempotency
// marker derived from idemKey. Before posting, it scans existing comments
// for the marker; if found, it does not repost (safe retry).
func (s *SyncClient) AddComment(ctx context.Context, nativeID, body, idemKey string) error {
	owner, repo, number, err := s.parseNativeID(nativeID)
	if err != nil {
		return err
	}

	marker := fmt.Sprintf(pilotOpMarkerFormat, idemKey)

	found, err := s.hasCommentMarker(ctx, owner, repo, number, marker)
	if err != nil {
		return err
	}
	if found {
		return nil // Already posted; retry is a no-op.
	}

	fullBody := body + "\n\n" + marker
	_, err = s.client.AddComment(ctx, owner, repo, number, fullBody)
	return err
}

// CreateIssue creates a new issue in projectID from draft.
func (s *SyncClient) CreateIssue(ctx context.Context, projectID string, draft core.IssueDraft) (core.IssueSnapshot, error) {
	owner, repo, err := s.resolveRepo(projectID)
	if err != nil {
		return core.IssueSnapshot{}, err
	}
	input := &IssueInput{
		Title:  draft.Title,
		Body:   draft.Body,
		Labels: draft.Labels,
	}
	issue, err := s.client.CreateIssue(ctx, owner, repo, input)
	if err != nil {
		return core.IssueSnapshot{}, err
	}
	return s.toSnapshot(owner, repo, issue), nil
}
