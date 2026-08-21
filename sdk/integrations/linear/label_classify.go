package linear

import (
	"context"
	"fmt"
	"strings"
)

// LabelClassification describes how a label name resolves relative to a
// given team, used to diagnose the "label not found" failures that
// GetLabelByName surfaces as an opaque error.
type LabelClassification string

const (
	// LabelTeamScoped means the label exists and is owned by the requested team.
	LabelTeamScoped LabelClassification = "team_scoped"
	// LabelWorkspaceScoped means the label exists with no owning team (Linear's
	// default scope for labels created from the issue view). Scope is
	// immutable in Linear, so a workspace-scoped label can never become
	// team-scoped in place.
	LabelWorkspaceScoped LabelClassification = "workspace_scoped"
	// LabelAnotherTeam means a label with this name exists but is owned by a
	// different team than the one requested.
	LabelAnotherTeam LabelClassification = "another_team"
	// LabelMissing means no label with this name exists anywhere in the workspace.
	LabelMissing LabelClassification = "missing"
)

// LabelClassificationResult is the outcome of ClassifyLabel: the resolved
// classification, the owning team's details (when applicable), and a
// human-readable remedy an operator can act on directly.
type LabelClassificationResult struct {
	Classification LabelClassification
	LabelID        string
	OwningTeam     *Team
	Remedy         string
}

// teamRefMatches reports whether team is the team identified by teamRef,
// which may be either a team key (e.g. "ENG") or a team UUID — the same two
// forms GetLabelByName's teamID parameter accepts. This mirrors
// GetLabelByName's own dispatch: a UUID-shaped teamRef is compared only
// against the team's ID, a non-UUID teamRef only against its key, so
// ClassifyLabel's verdict always matches what GetLabelByName will actually
// do for the same teamRef. Key comparison is case-insensitive: Linear team
// keys are conventionally upper-case, but nothing enforces that in operator
// config, and a case mismatch there must not be misreported as
// LabelAnotherTeam. GetLabelByName's key filter uses Linear's eqIgnoreCase
// StringComparator operator (not eq, which is case-sensitive) so its GraphQL
// query actually matches case-insensitively too — otherwise this function's
// "always matches what GetLabelByName will actually do" claim would be false
// for any miscased teamRef (GH-135).
func teamRefMatches(team *Team, teamRef string) bool {
	if team == nil {
		return false
	}
	if looksLikeUUID(teamRef) {
		return team.ID == teamRef
	}
	return strings.EqualFold(team.Key, teamRef)
}

// ClassifyLabel determines how a label named labelName resolves against
// teamRef (a team key or UUID), across all scopes in the workspace — not
// just the team-filtered match GetLabelByName performs. It reuses
// findLabelsByName, the same all-scopes lookup that backs GetLabelByName's
// workspace fallback, so the classification reflects the real production
// lookup path without maintaining a second copy of that query.
//
// Precedence decision: a team-scoped match for teamRef always wins over a
// workspace-scoped node, even when the workspace-scoped node appears earlier
// in the result set. This mirrors GetLabelByName's real lookup order — it
// tries the team-filtered query first and only falls back to the
// workspace-scoped query when that comes back empty — so a label that
// GetLabelByName would resolve via the team filter is never misclassified as
// LabelWorkspaceScoped just because of API result ordering. Only when no
// team-scoped match exists does a workspace-scoped node (checked next) or
// another team's node (checked last) get reported.
//
// This is a diagnostic helper: it does not create, delete, or move labels.
func (c *Client) ClassifyLabel(ctx context.Context, teamRef, labelName string) (*LabelClassificationResult, error) {
	nodes, err := c.findLabelsByName(ctx, labelName)
	if err != nil {
		return nil, err
	}

	var workspaceNode *labelByName
	var otherTeamNode *labelByName

	for i := range nodes {
		node := &nodes[i]

		if teamRefMatches(node.Team, teamRef) {
			return &LabelClassificationResult{
				Classification: LabelTeamScoped,
				LabelID:        node.ID,
				OwningTeam:     node.Team,
				Remedy:         fmt.Sprintf("label %q is already team-scoped under %s — no action needed", labelName, teamRef),
			}, nil
		}

		if node.Team == nil {
			if workspaceNode == nil {
				workspaceNode = node
			}
			continue
		}

		if otherTeamNode == nil {
			otherTeamNode = node
		}
	}

	if workspaceNode != nil {
		return &LabelClassificationResult{
			Classification: LabelWorkspaceScoped,
			LabelID:        workspaceNode.ID,
			Remedy: fmt.Sprintf(
				"label %q is workspace-scoped (scope is immutable in Linear) — delete & recreate team-scoped under %s",
				labelName, teamRef,
			),
		}, nil
	}

	if otherTeamNode != nil {
		return &LabelClassificationResult{
			Classification: LabelAnotherTeam,
			LabelID:        otherTeamNode.ID,
			OwningTeam:     otherTeamNode.Team,
			Remedy: fmt.Sprintf(
				"label %q belongs to team %s — move or rename it, or create a separate label under %s",
				labelName, otherTeamNode.Team.Name, teamRef,
			),
		}, nil
	}

	return &LabelClassificationResult{
		Classification: LabelMissing,
		Remedy:         fmt.Sprintf("label %q does not exist — create it under team %s", labelName, teamRef),
	}, nil
}
