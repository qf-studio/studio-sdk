package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// teamRefFixtureServer answers both ClassifyLabel's/getWorkspaceLabelByName's
// all-scopes "GetWorkspaceLabel" query (returns every node, unfiltered) and
// GetLabelByName's team-filtered "GetLabel" query, which it filters against
// nodes itself — by team.id when the query filters on team.id, by team.key
// when it filters on team.key — so the fixture actually exercises GH-133:
// ClassifyLabel and GetLabelByName must agree on which filter field a given
// teamRef dispatches to, or one silently misses what the other finds.
func teamRefFixtureServer(t *testing.T, nodes []labelByName) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(req.Query, "GetWorkspaceLabel") {
			body, err := json.Marshal(nodes)
			if err != nil {
				t.Fatalf("marshal nodes: %v", err)
			}
			_ = json.NewEncoder(w).Encode(GraphQLResponse{
				Data: json.RawMessage(`{"issueLabels": {"nodes": ` + string(body) + `}}`),
			})
			return
		}

		teamID, _ := req.Variables["teamId"].(string)
		byID := strings.Contains(req.Query, "team: { id:")

		var matched []labelByName
		for _, n := range nodes {
			if n.Team == nil {
				continue
			}
			if byID && n.Team.ID == teamID {
				matched = append(matched, n)
			}
			if !byID && n.Team.Key == teamID {
				matched = append(matched, n)
			}
		}
		body, err := json.Marshal(matched)
		if err != nil {
			t.Fatalf("marshal matched: %v", err)
		}
		_ = json.NewEncoder(w).Encode(GraphQLResponse{
			Data: json.RawMessage(`{"issueLabels": {"nodes": ` + string(body) + `}}`),
		})
	}))
}

// TestClassifyLabelAndGetLabelByName_AgreeOnUUIDTeamRef pins the GH-133 fix:
// a UUID-configured teamRef against a team-scoped label must resolve
// team_scoped via ClassifyLabel AND succeed via GetLabelByName, from the same
// fixture. Before the fix, GetLabelByName filtered by team key only, so a
// UUID teamRef never matched — preflight said "team_scoped" (fine) while
// startup's real lookup died, exactly the blind spot this issue reports.
func TestClassifyLabelAndGetLabelByName_AgreeOnUUIDTeamRef(t *testing.T) {
	const teamUUID = "11111111-1111-1111-1111-111111111111"
	nodes := []labelByName{
		{ID: "label-1", Name: "pilot", Team: &Team{ID: teamUUID, Key: "ROU", Name: "Routing"}},
	}

	server := teamRefFixtureServer(t, nodes)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)

	result, err := client.ClassifyLabel(context.Background(), teamUUID, "pilot")
	if err != nil {
		t.Fatalf("ClassifyLabel: %v", err)
	}
	if result.Classification != LabelTeamScoped {
		t.Errorf("ClassifyLabel Classification = %v, want %v", result.Classification, LabelTeamScoped)
	}

	id, err := client.GetLabelByName(context.Background(), teamUUID, "pilot")
	if err != nil {
		t.Fatalf("GetLabelByName: %v (GH-133: UUID teamRef must filter by team id, not key)", err)
	}
	if id != "label-1" {
		t.Errorf("GetLabelByName id = %q, want label-1", id)
	}
}

// TestClassifyLabel_KeyMatchIsCaseInsensitive pins the fold-in fix: a
// lower-cased teamRef must still match a label owned by the upper-cased team
// key Linear conventionally returns, rather than being misreported as
// LabelAnotherTeam.
func TestClassifyLabel_KeyMatchIsCaseInsensitive(t *testing.T) {
	server := classifyLabelServer(t,
		`[{"id": "label-1", "name": "pilot", "team": {"id": "team-rou", "key": "ROU", "name": "Routing"}}]`)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	result, err := client.ClassifyLabel(context.Background(), "rou", "pilot")
	if err != nil {
		t.Fatalf("ClassifyLabel: %v", err)
	}
	if result.Classification != LabelTeamScoped {
		t.Errorf("Classification = %v, want %v (case-insensitive key match)", result.Classification, LabelTeamScoped)
	}
}

// TestClassifyLabel_TeamScopedTakesPrecedenceOverWorkspaceScoped pins the
// precedence decision documented on ClassifyLabel: when the same label name
// exists both workspace-scoped and team-scoped for the requested team, the
// team-scoped match wins regardless of which node comes first in the API
// response — matching GetLabelByName's real lookup order (team-filtered
// query first, workspace fallback second).
func TestClassifyLabel_TeamScopedTakesPrecedenceOverWorkspaceScoped(t *testing.T) {
	// Workspace-scoped node appears before the team-scoped match in the
	// fixture, which is exactly the ordering that used to be misclassified.
	server := classifyLabelServer(t,
		`[{"id": "label-ws", "name": "pilot", "team": null},
		  {"id": "label-team", "name": "pilot", "team": {"id": "team-rou", "key": "ROU", "name": "Routing"}}]`)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	result, err := client.ClassifyLabel(context.Background(), "ROU", "pilot")
	if err != nil {
		t.Fatalf("ClassifyLabel: %v", err)
	}
	if result.Classification != LabelTeamScoped {
		t.Errorf("Classification = %v, want %v (team match must win over an earlier workspace-scoped node)", result.Classification, LabelTeamScoped)
	}
	if result.LabelID != "label-team" {
		t.Errorf("LabelID = %q, want label-team", result.LabelID)
	}
}
