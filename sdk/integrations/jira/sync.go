package jira

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// Compile-time interface assertion.
var _ core.SyncCapable = (*SyncClient)(nil)

// jiraSyncPerPage is the page size used by SyncClient's exhaustive JQL
// listings.
const jiraSyncPerPage = 50

// pilotOpMarkerFormat renders the idempotency marker embedded in comment
// bodies posted via SyncClient.AddComment. Jira preserves the marker text
// inside the ADF document on Cloud (and verbatim on Server), so a retried
// sync can scan raw comment JSON for it before posting.
const pilotOpMarkerFormat = "<!-- pilot-op:%s -->"

// defaultIssueType is used by CreateIssue when the draft does not specify one.
// Jira issue creation requires an issue type name; the SDK's core.IssueDraft
// has no such field, so a sensible default is applied here.
const defaultIssueType = "Task"

// jqlDateLayout is the date-time format JQL accepts for quoted comparisons
// (e.g. `updated >= "2026-07-13 00:00"`).
const jqlDateLayout = "2006-01-02 15:04"

// jiraDateLayout parses the timestamps Jira embeds in issue fields
// (RFC3339-ish with a 3-digit fraction and no colon in the offset).
const jiraDateLayout = "2006-01-02T15:04:05.000-0700"

// SyncClient implements core.SyncCapable for a single Jira project. It wraps
// *Client the same way the poller does, bound to one project key since JQL
// project filters and IssueSnapshot.NativeID (the issue key) are scoped per
// project.
type SyncClient struct {
	client     *Client
	projectKey string
}

// NewSyncClient creates a SyncClient bound to projectKey.
func NewSyncClient(client *Client, projectKey string) *SyncClient {
	return &SyncClient{client: client, projectKey: projectKey}
}

// resolveProject returns the project key to query for projectID: projectID
// itself if non-empty, else the SyncClient's bound project key.
func (s *SyncClient) resolveProject(projectID string) string {
	if projectID != "" {
		return projectID
	}
	return s.projectKey
}

// parseCursor decodes a core.Cursor into a startAt offset. An empty cursor
// requests the first page (startAt=0).
func parseCursor(page core.Cursor) (int, error) {
	if page == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(string(page))
	if err != nil {
		return 0, fmt.Errorf("invalid cursor %q: %w", page, err)
	}
	return n, nil
}

// parseJiraTime parses a Jira field timestamp, returning the zero time if the
// field is empty or malformed rather than failing the whole sync over one
// unparsable date.
func parseJiraTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(jiraDateLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// stateGroup maps a Jira status category onto the taxonomy's three buckets.
// Category keys are stable across Cloud/Server ("new", "indeterminate",
// "done"); the category name is used as a fallback for anything else.
func stateGroup(cat StatusCategory) string {
	switch cat.Key {
	case "new":
		return "To Do"
	case "indeterminate":
		return "In Progress"
	case "done":
		return "Done"
	default:
		return cat.Name
	}
}

// toSnapshot maps a Jira Issue onto the normalized core.IssueSnapshot.
// NativeID and SequenceID are both the issue key (e.g. "PROJ-42") since Jira
// keys are already the provider-native, human-readable identifier.
func (s *SyncClient) toSnapshot(issue *Issue) core.IssueSnapshot {
	var priority int
	if issue.Fields.Priority != nil {
		priority = int(PriorityFromJira(issue.Fields.Priority.Name))
	}

	assignee := ""
	if issue.Fields.Assignee != nil {
		assignee = issue.Fields.Assignee.DisplayName
	}

	labels := issue.Fields.Labels
	if labels == nil {
		labels = []string{}
	}

	return core.IssueSnapshot{
		NativeID:   issue.Key,
		SequenceID: issue.Key,
		Title:      issue.Fields.Summary,
		Body:       string(issue.Fields.Description),
		State:      issue.Fields.Status.Name,
		StateGroup: stateGroup(issue.Fields.Status.StatusCategory),
		Labels:     labels,
		Priority:   core.NormalizePriority(priority),
		Assignee:   assignee,
		URL:        strings.TrimSuffix(s.client.baseURL, "/") + "/browse/" + issue.Key,
		CreatedAt:  parseJiraTime(issue.Fields.Created),
		UpdatedAt:  parseJiraTime(issue.Fields.Updated),
	}
}

// buildJQL constructs the JQL query for a project, optionally filtered by an
// updated-since timestamp, ordered by update time ascending so pagination is
// stable across pages.
func buildJQL(projectKey string, since time.Time) string {
	jql := fmt.Sprintf("project = %s", projectKey)
	if !since.IsZero() {
		jql += fmt.Sprintf(` AND updated >= "%s"`, since.Format(jqlDateLayout))
	}
	jql += " ORDER BY updated ASC"
	return jql
}

// listPage is the shared implementation for ListUpdatedSince and ListAll:
// both are exhaustive, cursor-paginated JQL searches that differ only in
// whether an updated-since filter is applied.
func (s *SyncClient) listPage(ctx context.Context, projectID string, since time.Time, page core.Cursor) ([]core.IssueSnapshot, core.Cursor, error) {
	startAt, err := parseCursor(page)
	if err != nil {
		return nil, "", err
	}

	jql := buildJQL(s.resolveProject(projectID), since)
	resp, err := s.client.SearchIssuesPaged(ctx, jql, startAt, jiraSyncPerPage)
	if err != nil {
		return nil, "", err
	}

	snapshots := make([]core.IssueSnapshot, 0, len(resp.Issues))
	for _, issue := range resp.Issues {
		snapshots = append(snapshots, s.toSnapshot(issue))
	}

	next := core.Cursor("")
	if len(resp.Issues) == jiraSyncPerPage {
		next = core.Cursor(strconv.Itoa(startAt + jiraSyncPerPage))
	}
	return snapshots, next, nil
}

// ListUpdatedSince returns issues updated on or after since, oldest first,
// paginated exhaustively at jiraSyncPerPage per page.
func (s *SyncClient) ListUpdatedSince(ctx context.Context, projectID string, since time.Time, page core.Cursor) ([]core.IssueSnapshot, core.Cursor, error) {
	return s.listPage(ctx, projectID, since, page)
}

// ListAll returns every issue in projectID, paginated exhaustively.
func (s *SyncClient) ListAll(ctx context.Context, projectID string, page core.Cursor) ([]core.IssueSnapshot, core.Cursor, error) {
	return s.listPage(ctx, projectID, time.Time{}, page)
}

// GetIssue fetches a single issue snapshot by its native ID (the issue key).
func (s *SyncClient) GetIssue(ctx context.Context, nativeID string) (core.IssueSnapshot, error) {
	issue, err := s.client.GetIssue(ctx, nativeID)
	if err != nil {
		return core.IssueSnapshot{}, err
	}
	return s.toSnapshot(issue), nil
}

// buildFieldPatch converts a core.FieldPatch into the Jira field map accepted
// by Client.UpdateFields, validating the field types the SDK contract
// documents (title/body as string, labels as []string) and mapping them onto
// Jira's own field names (summary/description/labels).
func buildFieldPatch(fields core.FieldPatch) (map[string]interface{}, error) {
	patch := map[string]interface{}{}
	if v, ok := fields["title"]; ok {
		str, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", "title", v)
		}
		patch["summary"] = str
	}
	if v, ok := fields["body"]; ok {
		str, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected string, got %T", "body", v)
		}
		patch["description"] = str
	}
	if v, ok := fields["labels"]; ok {
		labels, ok := v.([]string)
		if !ok {
			return nil, fmt.Errorf("field %q: expected []string, got %T", "labels", v)
		}
		patch["labels"] = labels
	}
	return patch, nil
}

// UpdateFields applies a partial patch (title/body/labels) to nativeID and
// returns the resulting snapshot.
func (s *SyncClient) UpdateFields(ctx context.Context, nativeID string, fields core.FieldPatch) (core.IssueSnapshot, error) {
	patch, err := buildFieldPatch(fields)
	if err != nil {
		return core.IssueSnapshot{}, err
	}
	if err := s.client.UpdateFields(ctx, nativeID, patch); err != nil {
		return core.IssueSnapshot{}, err
	}
	return s.GetIssue(ctx, nativeID)
}

// TransitionState moves nativeID to providerState, a Jira status or
// transition name (see Client.TransitionIssueTo).
func (s *SyncClient) TransitionState(ctx context.Context, nativeID, providerState string) error {
	return s.client.TransitionIssueTo(ctx, nativeID, providerState)
}

// AddComment posts body as a comment on nativeID, embedding an idempotency
// marker derived from idemKey. Before posting, it scans existing comments'
// raw JSON for the marker; if found, it does not repost (safe retry). Jira
// preserves the marker text verbatim inside the ADF document on Cloud, so a
// raw-bytes substring search works regardless of platform.
func (s *SyncClient) AddComment(ctx context.Context, nativeID, body, idemKey string) error {
	marker := fmt.Sprintf(pilotOpMarkerFormat, idemKey)

	existing, err := s.client.GetComments(ctx, nativeID)
	if err != nil {
		return err
	}
	for _, c := range existing {
		if strings.Contains(string(c.Body), marker) {
			return nil // Already posted; retry is a no-op.
		}
	}

	fullBody := body + "\n\n" + marker
	_, err = s.client.AddComment(ctx, nativeID, fullBody)
	return err
}

// CreateIssue creates a new issue in projectID from draft.
func (s *SyncClient) CreateIssue(ctx context.Context, projectID string, draft core.IssueDraft) (core.IssueSnapshot, error) {
	projectKey := s.resolveProject(projectID)
	created, err := s.client.CreateIssue(ctx, projectKey, defaultIssueType, draft.Title, draft.Body, draft.Labels)
	if err != nil {
		return core.IssueSnapshot{}, err
	}
	return s.GetIssue(ctx, created.Key)
}
