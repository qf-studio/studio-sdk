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

func labelLookupServer(t *testing.T, teamNodes, workspaceNodes string) (*httptest.Server, *int) {
	t.Helper()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		nodes := teamNodes
		if strings.Contains(req.Query, "GetWorkspaceLabel") {
			nodes = workspaceNodes
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(GraphQLResponse{
			Data: json.RawMessage(`{"issueLabels": {"nodes": ` + nodes + `}}`),
		})
	}))
	return server, &requests
}

func TestGetLabelByName_TeamScoped(t *testing.T) {
	server, requests := labelLookupServer(t,
		`[{"id": "label-team-1", "name": "pilot"}]`, `[]`)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	id, err := client.GetLabelByName(context.Background(), "ROU", "pilot")
	if err != nil {
		t.Fatalf("GetLabelByName: %v", err)
	}
	if id != "label-team-1" {
		t.Errorf("id = %q, want label-team-1", id)
	}
	if *requests != 1 {
		t.Errorf("requests = %d, want 1 (no fallback on a team-scoped hit)", *requests)
	}
}

func TestGetLabelByName_WorkspaceScopedFallback(t *testing.T) {
	server, requests := labelLookupServer(t,
		`[]`,
		`[{"id": "label-other", "name": "pilot", "team": {"id": "team-other"}},
		  {"id": "label-ws-1", "name": "pilot", "team": null}]`)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	id, err := client.GetLabelByName(context.Background(), "ROU", "pilot")
	if err != nil {
		t.Fatalf("GetLabelByName: %v", err)
	}
	if id != "label-ws-1" {
		t.Errorf("id = %q, want label-ws-1 (the team-less node, not another team's)", id)
	}
	if *requests != 2 {
		t.Errorf("requests = %d, want 2 (team query then workspace fallback)", *requests)
	}
}

func TestGetLabelByName_NotFoundAnywhere(t *testing.T) {
	server, _ := labelLookupServer(t, `[]`,
		`[{"id": "label-other", "name": "pilot", "team": {"id": "team-other"}}]`)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	_, err := client.GetLabelByName(context.Background(), "ROU", "pilot")
	if err == nil {
		t.Fatal("expected error when the label exists only in another team")
	}
	if !strings.Contains(err.Error(), "not found in team ROU") {
		t.Errorf("error = %v, want the not-found-in-team message", err)
	}
}
