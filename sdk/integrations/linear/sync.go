package linear

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// uuidPattern matches a Linear-style UUID (e.g. workflow state or issue id),
// distinguishing an already-resolved id from a human-readable state name
// passed into TransitionState.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func looksLikeUUID(s string) bool {
	return uuidPattern.MatchString(s)
}

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

	// workflowStateMu/workflowStateCache cache team-key -> (state name ->
	// state id) so repeated TransitionState calls with a state name don't
	// re-fetch the team's workflow states on every call.
	workflowStateMu    sync.RWMutex
	workflowStateCache map[string]map[string]string
}

// NewSyncAdapter creates a SyncAdapter backed by client.
func NewSyncAdapter(client *Client) *SyncAdapter {
	return &SyncAdapter{
		client:             client,
		teamIDCache:        make(map[string]string),
		workflowStateCache: make(map[string]map[string]string),
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
// updatedAt is on or after since (inclusive), one page at a time following
// the #85 cursor-following convention (page is the opaque "after" cursor).
// since is formatted with nanosecond precision so an issue updated exactly at
// the watermark is never dropped; callers are expected to dedupe returned
// snapshots by NativeID across successive calls at the same watermark.
func (s *SyncAdapter) ListUpdatedSince(ctx context.Context, projectID string, since time.Time, page core.Cursor) ([]core.IssueSnapshot, core.Cursor, error) {
	query := `
		query SyncIssuesSince($teamId: String!, $since: DateTimeOrDuration!, $first: Int!, $after: String) {
			issues(
				filter: {
					team: { key: { eq: $teamId } }
					updatedAt: { gte: $since }
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
		"since":  since.UTC().Format(time.RFC3339Nano),
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
// (a core.Priority* normalized string). An unrecognized key, or a recognized
// key with the wrong value type, is rejected with an error and no field is
// applied.
func (s *SyncAdapter) UpdateFields(ctx context.Context, nativeID string, fields core.FieldPatch) (core.IssueSnapshot, error) {
	for key := range fields {
		switch key {
		case "title", "description", "priority", "labels":
		default:
			return core.IssueSnapshot{}, fmt.Errorf("unrecognized field key %q", key)
		}
	}

	variables := map[string]interface{}{"id": nativeID}

	if v, ok := fields["title"]; ok {
		str, ok := v.(string)
		if !ok {
			return core.IssueSnapshot{}, fmt.Errorf("field %q must be a string, got %T", "title", v)
		}
		variables["title"] = str
	}
	if v, ok := fields["description"]; ok {
		str, ok := v.(string)
		if !ok {
			return core.IssueSnapshot{}, fmt.Errorf("field %q must be a string, got %T", "description", v)
		}
		variables["description"] = str
	}
	if v, ok := fields["priority"]; ok {
		str, ok := v.(string)
		if !ok {
			return core.IssueSnapshot{}, fmt.Errorf("field %q must be a string, got %T", "priority", v)
		}
		variables["priority"] = priorityRankFromNormalized(str)
	}
	if v, ok := fields["labels"]; ok {
		names, ok := toStringSlice(v)
		if !ok {
			return core.IssueSnapshot{}, fmt.Errorf("field %q must be a []string, got %T", "labels", v)
		}
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

// TransitionState moves nativeID to providerState, which may be either a
// Linear workflow-state UUID (as returned in core.IssueSnapshot's internal
// representation, or by GetTeamDoneStateID) or a workflow-state NAME (the
// value core.IssueSnapshot.State actually carries). A UUID is passed straight
// through to Client.UpdateIssueState; a name is first resolved to the state's
// id via a per-team workflow-state lookup, since the underlying GraphQL
// mutation only accepts a stateId.
func (s *SyncAdapter) TransitionState(ctx context.Context, nativeID, providerState string) error {
	stateID := providerState
	if !looksLikeUUID(providerState) {
		issue, err := s.client.GetIssue(ctx, nativeID)
		if err != nil {
			return fmt.Errorf("failed to resolve issue %s to look up its team: %w", nativeID, err)
		}
		id, err := s.resolveWorkflowStateID(ctx, issue.Team.Key, providerState)
		if err != nil {
			return fmt.Errorf("failed to resolve state %q for team %s: %w", providerState, issue.Team.Key, err)
		}
		stateID = id
	}
	return s.client.UpdateIssueState(ctx, nativeID, stateID)
}

// resolveWorkflowStateID resolves stateName to its workflow-state id within
// teamKey's team, caching all of the team's states on first lookup.
func (s *SyncAdapter) resolveWorkflowStateID(ctx context.Context, teamKey, stateName string) (string, error) {
	s.workflowStateMu.RLock()
	if states, ok := s.workflowStateCache[teamKey]; ok {
		id, ok := states[stateName]
		s.workflowStateMu.RUnlock()
		if ok {
			return id, nil
		}
		return "", fmt.Errorf("no workflow state named %q found for team %s", stateName, teamKey)
	}
	s.workflowStateMu.RUnlock()

	query := `
		query SyncResolveWorkflowStates($teamKey: String!) {
			workflowStates(filter: { team: { key: { eq: $teamKey } } }) {
				nodes { id name }
			}
		}
	`

	var result struct {
		WorkflowStates struct {
			Nodes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"workflowStates"`
	}

	if err := s.client.Execute(ctx, query, map[string]interface{}{"teamKey": teamKey}, &result); err != nil {
		return "", err
	}

	states := make(map[string]string, len(result.WorkflowStates.Nodes))
	for _, n := range result.WorkflowStates.Nodes {
		states[n.Name] = n.ID
	}

	s.workflowStateMu.Lock()
	s.workflowStateCache[teamKey] = states
	s.workflowStateMu.Unlock()

	id, ok := states[stateName]
	if !ok {
		return "", fmt.Errorf("no workflow state named %q found for team %s", stateName, teamKey)
	}
	return id, nil
}

// syncCommentMarker returns the idempotency marker embedded in comment
// bodies posted via AddComment, so retried syncs can detect a prior post.
func syncCommentMarker(idemKey string) string {
	return fmt.Sprintf("<!-- pilot-op:%s -->", idemKey)
}

// syncCommentsPageSize is the GraphQL page size used when scanning an
// issue's comments for a prior idempotency marker in AddComment.
const syncCommentsPageSize = 100

// AddComment posts body as a comment on nativeID, embedding a
// "<!-- pilot-op:{idemKey} -->" marker. If a comment with the same marker
// already exists on the issue, AddComment is a no-op — this makes retried
// syncs idempotent. The existing-comments scan follows the comments
// connection's pagination cursor to completion, so a marker sitting beyond
// the first page is still found.
func (s *SyncAdapter) AddComment(ctx context.Context, nativeID, body, idemKey string) error {
	marker := syncCommentMarker(idemKey)

	query := `
		query SyncIssueComments($id: String!, $first: Int!, $after: String) {
			issue(id: $id) {
				comments(first: $first, after: $after) {
					nodes { id body }
					pageInfo { hasNextPage endCursor }
				}
			}
		}
	`

	after := ""
	for {
		variables := map[string]interface{}{"id": nativeID, "first": syncCommentsPageSize}
		if after != "" {
			variables["after"] = after
		}

		var result struct {
			Issue struct {
				Comments struct {
					Nodes []struct {
						ID   string `json:"id"`
						Body string `json:"body"`
					} `json:"nodes"`
					PageInfo pageInfo `json:"pageInfo"`
				} `json:"comments"`
			} `json:"issue"`
		}

		if err := s.client.Execute(ctx, query, variables, &result); err != nil {
			return fmt.Errorf("failed to list comments for %s: %w", nativeID, err)
		}

		for _, c := range result.Issue.Comments.Nodes {
			if strings.Contains(c.Body, marker) {
				return nil
			}
		}

		if !result.Issue.Comments.PageInfo.HasNextPage {
			break
		}
		after = result.Issue.Comments.PageInfo.EndCursor
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
// that passed through JSON unmarshaling). Returns ok=false if v is neither,
// or if a []interface{} contains a non-string element.
func toStringSlice(v interface{}) (out []string, ok bool) {
	switch t := v.(type) {
	case []string:
		return t, true
	case []interface{}:
		out = make([]string, 0, len(t))
		for _, item := range t {
			str, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, str)
		}
		return out, true
	default:
		return nil, false
	}
}
