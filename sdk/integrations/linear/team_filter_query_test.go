package linear

import (
	"strings"
	"testing"
)

func TestTeamFilterQueries_VariableTypeAndFilterField(t *testing.T) {
	const uuid = "0f1e2d3c-4b5a-4968-8777-665544332211"

	queries := []struct {
		name   string
		render func(teamID string) string
		// keyFilter is the exact team filter expected on the key branch.
		keyFilter string
	}{
		{"GetLabel", getLabelQuery, "team: { key: { eqIgnoreCase: $teamId } }"},
		{"ListIssues", listIssuesQuery, "team: { key: { eq: $teamId } }"},
		{"ListIssuesSince", listIssuesSinceQuery, "team: { key: { eq: $teamId } }"},
	}
	inputs := []struct {
		name       string
		teamID     string
		wantDecl   string
		wantAbsent string
		idBranch   bool
	}{
		{"key", "ENG", "$teamId: String!", "$teamId: ID!", false},
		{"uuid", uuid, "$teamId: ID!", "$teamId: String!", true},
	}

	for _, q := range queries {
		for _, in := range inputs {
			t.Run(q.name+"/"+in.name, func(t *testing.T) {
				got := q.render(in.teamID)
				if !strings.Contains(got, in.wantDecl) {
					t.Errorf("query missing %q:\n%s", in.wantDecl, got)
				}
				if strings.Contains(got, in.wantAbsent) {
					t.Errorf("query unexpectedly contains %q:\n%s", in.wantAbsent, got)
				}
				wantFilter := q.keyFilter
				if in.idBranch {
					wantFilter = "team: { id: { eq: $teamId } }"
				}
				if !strings.Contains(got, wantFilter) {
					t.Errorf("query missing filter %q:\n%s", wantFilter, got)
				}
			})
		}
	}
}
