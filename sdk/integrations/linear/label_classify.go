package linear

import (
	"context"
	"fmt"
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

// ClassifyLabel determines how a label named labelName resolves against
// teamRef (a team key or UUID), across all scopes in the workspace — not
// just the team-filtered match GetLabelByName performs. It reuses
// findLabelsByName, the same all-scopes lookup that backs GetLabelByName's
// workspace fallback, so the classification reflects the real production
// lookup path without maintaining a second copy of that query.
//
// This is a diagnostic helper: it does not create, delete, or move labels.
func (c *Client) ClassifyLabel(ctx context.Context, teamRef, labelName string) (*LabelClassificationResult, error) {
	nodes, err := c.findLabelsByName(ctx, labelName)
	if err != nil {
		return nil, err
	}

	var otherTeamNode *labelByName

	for i := range nodes {
		node := &nodes[i]

		if node.Team == nil {
			return &LabelClassificationResult{
				Classification: LabelWorkspaceScoped,
				LabelID:        node.ID,
				Remedy: fmt.Sprintf(
					"label %q is workspace-scoped (scope is immutable in Linear) — delete & recreate team-scoped under %s",
					labelName, teamRef,
				),
			}, nil
		}

		if node.Team.ID == teamRef || node.Team.Key == teamRef {
			return &LabelClassificationResult{
				Classification: LabelTeamScoped,
				LabelID:        node.ID,
				OwningTeam:     node.Team,
				Remedy:         fmt.Sprintf("label %q is already team-scoped under %s — no action needed", labelName, teamRef),
			}, nil
		}

		if otherTeamNode == nil {
			otherTeamNode = node
		}
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
