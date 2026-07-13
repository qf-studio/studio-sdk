package linear

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// Compile-time interface assertion.
var _ core.SyncCapable = (*SyncAdapter)(nil)

// SyncAdapter implements core.SyncCapable (core.SyncSource + core.SyncWriter)
// for Linear, backed by a *Client. It is a distinct type from *Client because
// the SyncCapable method signatures (normalized on core.IssueSnapshot) differ
// from Client's existing native-shaped methods (e.g. GetIssue returns *Issue,
// not core.IssueSnapshot).
//
// projectID in every SyncSource/SyncWriter method is the Linear team key
// (e.g. "ENG"), matching the team-key filters already used by
// ListIssuesOptions/ListIssuesSinceOptions.
type SyncAdapter struct {
	client *Client

	teamIDMu    sync.RWMutex
	teamIDCache map[string]string
}

// NewSyncAdapter creates a SyncAdapter backed by client.
func NewSyncAdapter(client *Client) *SyncAdapter {
	return &SyncAdapter{
		client:      client,
		teamIDCache: make(map[string]string),
	}
}

// syncIssuesFields is the field selection shared by every sync query/mutation
// that returns an issue.
const syncIssuesFields = `
	id
	identifier
	title
	description
	priority
	state { id name type }
	labels { nodes { id name } }
	assignee { id name email }
	project { id name }
	team { id name key }
	url
	createdAt
	updatedAt
`

// toSnapshot maps a native Linear issue to the normalized core.IssueSnapshot.
func toSnapshot(issue *Issue) core.IssueSnapshot {
	labelNames := make([]string, 0, len(issue.Labels))
	for _, l := range issue.Labels {
		labelNames = append(labelNames, l.Name)
	}

	assignee := ""
	if issue.Assignee != nil {
		assignee = issue.Assignee.Name
	}

	return core.IssueSnapshot{
		NativeID:   issue.ID,
		SequenceID: issue.Identifier,
		Title:      issue.Title,
		Body:       issue.Description,
		State:      issue.State.Name,
		StateGroup: issue.State.Type,
		Labels:     labelNames,
		Priority:   core.NormalizePriority(issue.Priority),
		Assignee:   assignee,
		URL:        issue.URL,
		CreatedAt:  issue.CreatedAt,
		UpdatedAt:  issue.UpdatedAt,
	}
}

// priorityRankFromNormalized inverts core.NormalizePriority, mapping the
// normalized priority vocabulary back to Linear's native rank so field
// patches can be sent over the issueUpdate/issueCreate mutations.
func priorityRankFromNormalized(p string) int {
	switch p {
	case core.PriorityUrgent:
		return PriorityUrgent
	case core.PriorityHigh:
		return PriorityHigh
	case core.PriorityMedium:
		return PriorityMedium
	case core.PriorityLow:
		return PriorityLow
	default:
		return PriorityNone
	}
}

// ListUpdatedSince returns issues in the team keyed by projectID whose
// updatedAt is strictly greater than since, one page at a time following the
// #85 cursor-following convention (page is the opaque "after" cursor).
func (s *SyncAdapter) ListUpdatedSince(ctx context.Context, projectID string, since time.Time, page core.Cursor) ([]core.IssueSnapshot, core.Cursor, error) {
	query := `
		query SyncIssuesSince($teamId: String!, $since: DateTimeOrDuration!, $first: Int!, $after: String) {
			issues(
				filter: {
					team: { key: { eq: $teamId } }
					updatedAt: { gt: $since }
				}
				first: $first
				after: $after
				orderBy: updatedAt
			) {
				nodes { ` + syncIssuesFields + ` }
				pageInfo { hasNextPage endCursor }
			}
		}
	`

	variables := map[string]interface{}{
		"teamId": projectID,
		"since":  since.UTC().Format(time.RFC3339),
		"first":  listIssuesPageSize,
	}
	if page != "" {
		variables["after"] = string(page)
	}

	return s.runIssuesPage(ctx, query, variables)
}

// ListAll returns every issue in the team keyed by projectID, one page at a
// time. Unlike ListIssues (used by the polling path), it does not restrict
// to backlog/unstarted/started states — a full sync needs to observe
// completed/canceled issues too, to reconcile the host's own board state.
func (s *SyncAdapter) ListAll(ctx context.Context, projectID string, page core.Cursor) ([]core.IssueSnapshot, core.Cursor, error) {
	query := `
		query SyncAllIssues($teamId: String!, $first: Int!, $after: String) {
			issues(
				filter: { team: { key: { eq: $teamId } } }
				first: $first
				after: $after
				orderBy: createdAt
			) {
				nodes { ` + syncIssuesFields + ` }
				pageInfo { hasNextPage endCursor }
			}
		}
	`

	variables := map[string]interface{}{
		"teamId": projectID,
		"first":  listIssuesPageSize,
	}
	if page != "" {
		variables["after"] = string(page)
	}

	return s.runIssuesPage(ctx, query, variables)
}

func (s *SyncAdapter) runIssuesPage(ctx context.Context, query string, variables map[string]interface{}) ([]core.IssueSnapshot, core.Cursor, error) {
	var result struct {
		Issues struct {
			Nodes    []*issueListItem `json:"nodes"`
			PageInfo pageInfo         `json:"pageInfo"`
		} `json:"issues"`
	}

	if err := s.client.Execute(ctx, query, variables, &result); err != nil {
		return nil, "", err
	}

	snapshots := make([]core.IssueSnapshot, 0, len(result.Issues.Nodes))
	for _, node := range result.Issues.Nodes {
		snapshots = append(snapshots, toSnapshot(node.toIssue()))
	}

	next := core.Cursor("")
	if result.Issues.PageInfo.HasNextPage {
		next = core.Cursor(result.Issues.PageInfo.EndCursor)
	}

	return snapshots, next, nil
}

// GetIssue fetches a single issue snapshot by its Linear issue id.
func (s *SyncAdapter) GetIssue(ctx context.Context, nativeID string) (core.IssueSnapshot, error) {
	issue, err := s.client.GetIssue(ctx, nativeID)
	if err != nil {
		return core.IssueSnapshot{}, err
	}
	return toSnapshot(issue), nil
}

// UpdateFields applies a partial field patch to nativeID via the Linear
// issueUpdate mutation. Recognized fields keys: "title" (string),
// "description" (string), "labels" ([]string of label names), "priority"
// (a core.Priority* normalized string). Unrecognized keys are ignored.
func (s *SyncAdapter) UpdateFields(ctx context.Context, nativeID string, fields core.FieldPatch) (core.IssueSnapshot, error) {
	variables := map[string]interface{}{"id": nativeID}

	if v, ok := fields["title"]; ok {
		if str, ok := v.(string); ok {
			variables["title"] = str
		}
	}
	if v, ok := fields["description"]; ok {
		if str, ok := v.(string); ok {
			variables["description"] = str
		}
	}
	if v, ok := fields["priority"]; ok {
		if str, ok := v.(string); ok {
			variables["priority"] = priorityRankFromNormalized(str)
		}
	}
	if v, ok := fields["labels"]; ok {
		names := toStringSlice(v)
		if names != nil {
			issue, err := s.client.GetIssue(ctx, nativeID)
			if err != nil {
				return core.IssueSnapshot{}, fmt.Errorf("failed to resolve team for label update on %s: %w", nativeID, err)
			}
			labelIDs := make([]string, 0, len(names))
			for _, name := range names {
				id, err := s.client.GetOrCreateLabel(ctx, issue.Team.Key, name, "#8b949e")
				if err != nil {
					return core.IssueSnapshot{}, fmt.Errorf("failed to resolve label %q: %w", name, err)
				}
				labelIDs = append(labelIDs, id)
			}
			variables["labelIds"] = labelIDs
		}
	}

	if len(variables) == 1 {
		// No recognized fields in the patch; nothing to update.
		return s.GetIssue(ctx, nativeID)
	}

	mutation := `
		mutation SyncUpdateIssueFields($id: String!, $title: String, $description: String, $labelIds: [String!], $priority: Int) {
			issueUpdate(id: $id, input: { title: $title, description: $description, labelIds: $labelIds, priority: $priority }) {
				success
				issue { ` + syncIssuesFields + ` }
			}
		}
	`

	var result struct {
		IssueUpdate struct {
			Success bool          `json:"success"`
			Issue   issueListItem `json:"issue"`
		} `json:"issueUpdate"`
	}

	if err := s.client.Execute(ctx, mutation, variables, &result); err != nil {
		return core.IssueSnapshot{}, err
	}
	if !result.IssueUpdate.Success {
		return core.IssueSnapshot{}, fmt.Errorf("issueUpdate returned success=false for %s", nativeID)
	}

	return toSnapshot(result.IssueUpdate.Issue.toIssue()), nil
}

// TransitionState moves nativeID to providerState (a workflow state id).
func (s *SyncAdapter) TransitionState(ctx context.Context, nativeID, providerState string) error {
	return s.client.UpdateIssueState(ctx, nativeID, providerState)
}

// syncCommentMarker returns the idempotency marker embedded in comment
// bodies posted via AddComment, so retried syncs can detect a prior post.
func syncCommentMarker(idemKey string) string {
	return fmt.Sprintf("<!-- pilot-op:%s -->", idemKey)
}

// AddComment posts body as a comment on nativeID, embedding a
// "<!-- pilot-op:{idemKey} -->" marker. If a comment with the same marker
// already exists on the issue, AddComment is a no-op — this makes retried
// syncs idempotent.
func (s *SyncAdapter) AddComment(ctx context.Context, nativeID, body, idemKey string) error {
	marker := syncCommentMarker(idemKey)

	query := `
		query SyncIssueComments($id: String!) {
			issue(id: $id) {
				comments {
					nodes { id body }
				}
			}
		}
	`

	var result struct {
		Issue struct {
			Comments struct {
				Nodes []struct {
					ID   string `json:"id"`
					Body string `json:"body"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"issue"`
	}

	if err := s.client.Execute(ctx, query, map[string]interface{}{"id": nativeID}, &result); err != nil {
		return fmt.Errorf("failed to list comments for %s: %w", nativeID, err)
	}

	for _, c := range result.Issue.Comments.Nodes {
		if strings.Contains(c.Body, marker) {
			return nil
		}
	}

	return s.client.AddComment(ctx, nativeID, body+"\n\n"+marker)
}

// CreateIssue creates a new issue in the team keyed by projectID from draft.
func (s *SyncAdapter) CreateIssue(ctx context.Context, projectID string, draft core.IssueDraft) (core.IssueSnapshot, error) {
	teamID, err := s.resolveTeamID(ctx, projectID)
	if err != nil {
		return core.IssueSnapshot{}, fmt.Errorf("failed to resolve team %q: %w", projectID, err)
	}

	labelIDs := make([]string, 0, len(draft.Labels))
	for _, name := range draft.Labels {
		id, err := s.client.GetOrCreateLabel(ctx, projectID, name, "#8b949e")
		if err != nil {
			return core.IssueSnapshot{}, fmt.Errorf("failed to resolve label %q: %w", name, err)
		}
		labelIDs = append(labelIDs, id)
	}

	mutation := `
		mutation SyncCreateIssue($teamId: String!, $title: String!, $description: String, $labelIds: [String!], $priority: Int) {
			issueCreate(input: {
				teamId: $teamId,
				title: $title,
				description: $description,
				labelIds: $labelIds,
				priority: $priority
			}) {
				success
				issue { ` + syncIssuesFields + ` }
			}
		}
	`

	variables := map[string]interface{}{
		"teamId":      teamID,
		"title":       draft.Title,
		"description": draft.Body,
		"labelIds":    labelIDs,
		"priority":    priorityRankFromNormalized(draft.Priority),
	}

	var result struct {
		IssueCreate struct {
			Success bool          `json:"success"`
			Issue   issueListItem `json:"issue"`
		} `json:"issueCreate"`
	}

	if err := s.client.Execute(ctx, mutation, variables, &result); err != nil {
		return core.IssueSnapshot{}, fmt.Errorf("failed to create issue: %w", err)
	}
	if !result.IssueCreate.Success {
		return core.IssueSnapshot{}, fmt.Errorf("issueCreate returned success=false")
	}

	return toSnapshot(result.IssueCreate.Issue.toIssue()), nil
}

// resolveTeamID resolves a team key (e.g. "ENG") to its Linear team id,
// caching results since team keys rarely change.
func (s *SyncAdapter) resolveTeamID(ctx context.Context, teamKey string) (string, error) {
	s.teamIDMu.RLock()
	if id, ok := s.teamIDCache[teamKey]; ok {
		s.teamIDMu.RUnlock()
		return id, nil
	}
	s.teamIDMu.RUnlock()

	query := `
		query SyncResolveTeam($key: String!) {
			teams(filter: { key: { eq: $key } }) {
				nodes { id key name }
			}
		}
	`

	var result struct {
		Teams struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"teams"`
	}

	if err := s.client.Execute(ctx, query, map[string]interface{}{"key": teamKey}, &result); err != nil {
		return "", err
	}
	if len(result.Teams.Nodes) == 0 {
		return "", fmt.Errorf("no team found with key %q", teamKey)
	}

	id := result.Teams.Nodes[0].ID
	s.teamIDMu.Lock()
	s.teamIDCache[teamKey] = id
	s.teamIDMu.Unlock()

	return id, nil
}

// toStringSlice converts a FieldPatch value expected to be a string slice,
// accepting both []string (native Go callers) and []interface{} (values
// that passed through JSON unmarshaling). Returns nil if v is neither.
func toStringSlice(v interface{}) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	default:
		return nil
	}
}
