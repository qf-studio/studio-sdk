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

func createLabelServer(t *testing.T) (*httptest.Server, *int, *[]string) {
	t.Helper()
	requests := 0
	var mutationTeamIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "teams(filter"):
			_ = json.NewEncoder(w).Encode(GraphQLResponse{
				Data: json.RawMessage(`{"teams": {"nodes": [{"id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}]}}`),
			})
		case strings.Contains(req.Query, "issueLabelCreate"):
			mutationTeamIDs = append(mutationTeamIDs, req.Variables["teamId"].(string))
			_ = json.NewEncoder(w).Encode(GraphQLResponse{
				Data: json.RawMessage(`{"issueLabelCreate": {"success": true, "issueLabel": {"id": "label-1", "name": "pilot-done"}}}`),
			})
		default:
			t.Fatalf("unexpected query: %s", req.Query)
		}
	}))
	return server, &requests, &mutationTeamIDs
}

func TestCreateLabel_ResolvesTeamKeyToUUID(t *testing.T) {
	server, requests, teamIDs := createLabelServer(t)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	id, err := client.CreateLabel(context.Background(), "ROU", "pilot-done", "#00AA55")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if id != "label-1" {
		t.Errorf("id = %q, want label-1", id)
	}
	if *requests != 2 {
		t.Errorf("requests = %d, want 2 (team resolution then mutation)", *requests)
	}
	if len(*teamIDs) != 1 || (*teamIDs)[0] != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("mutation teamId = %v, want the resolved UUID, never the key", *teamIDs)
	}
}

func TestCreateLabel_UUIDPassesThrough(t *testing.T) {
	server, requests, teamIDs := createLabelServer(t)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	uuid := "11111111-2222-3333-4444-555555555555"
	if _, err := client.CreateLabel(context.Background(), uuid, "pilot-done", "#00AA55"); err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if *requests != 1 {
		t.Errorf("requests = %d, want 1 (a UUID needs no resolution query)", *requests)
	}
	if (*teamIDs)[0] != uuid {
		t.Errorf("mutation teamId = %q, want the UUID untouched", (*teamIDs)[0])
	}
}

func TestCreateLabel_TeamResolutionCached(t *testing.T) {
	server, requests, _ := createLabelServer(t)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	for _, name := range []string{"pilot-in-progress", "pilot-done"} {
		if _, err := client.CreateLabel(context.Background(), "ROU", name, "#0066FF"); err != nil {
			t.Fatalf("CreateLabel(%s): %v", name, err)
		}
	}
	if *requests != 3 {
		t.Errorf("requests = %d, want 3 (one team resolution, two mutations)", *requests)
	}
}
