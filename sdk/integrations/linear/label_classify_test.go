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

// classifyLabelServer returns a fake GraphQL server that answers
// ClassifyLabel's underlying query — the shared GetWorkspaceLabel lookup
// findLabelsByName also uses — with the given nodes JSON, following the
// label_lookup_test.go idiom of decoding the real GraphQLRequest and
// replying with a GraphQLResponse envelope.
func classifyLabelServer(t *testing.T, nodes string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !strings.Contains(req.Query, "GetWorkspaceLabel") {
			t.Fatalf("unexpected query: %s", req.Query)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(GraphQLResponse{
			Data: json.RawMessage(`{"issueLabels": {"nodes": ` + nodes + `}}`),
		})
	}))
}

func TestClassifyLabel(t *testing.T) {
	tests := []struct {
		name          string
		nodes         string
		teamRef       string
		wantClass     LabelClassification
		wantLabelID   string
		wantRemedyHas string
	}{
		{
			name:          "team scoped, matched by key",
			nodes:         `[{"id": "label-1", "name": "pilot", "team": {"id": "team-rou", "key": "ROU", "name": "Routing"}}]`,
			teamRef:       "ROU",
			wantClass:     LabelTeamScoped,
			wantLabelID:   "label-1",
			wantRemedyHas: "no action needed",
		},
		{
			name:          "team scoped, matched by UUID",
			nodes:         `[{"id": "label-1", "name": "pilot", "team": {"id": "11111111-1111-1111-1111-111111111111", "key": "ROU", "name": "Routing"}}]`,
			teamRef:       "11111111-1111-1111-1111-111111111111",
			wantClass:     LabelTeamScoped,
			wantLabelID:   "label-1",
			wantRemedyHas: "no action needed",
		},
		{
			name:          "workspace scoped",
			nodes:         `[{"id": "label-ws", "name": "pilot", "team": null}]`,
			teamRef:       "ROU",
			wantClass:     LabelWorkspaceScoped,
			wantLabelID:   "label-ws",
			wantRemedyHas: "delete & recreate team-scoped",
		},
		{
			name:          "another team's",
			nodes:         `[{"id": "label-other", "name": "pilot", "team": {"id": "team-eng", "key": "ENG", "name": "Engineering"}}]`,
			teamRef:       "ROU",
			wantClass:     LabelAnotherTeam,
			wantLabelID:   "label-other",
			wantRemedyHas: "belongs to team Engineering",
		},
		{
			name:          "missing",
			nodes:         `[]`,
			teamRef:       "ROU",
			wantClass:     LabelMissing,
			wantLabelID:   "",
			wantRemedyHas: "does not exist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := classifyLabelServer(t, tt.nodes)
			defer server.Close()

			client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
			result, err := client.ClassifyLabel(context.Background(), tt.teamRef, "pilot")
			if err != nil {
				t.Fatalf("ClassifyLabel: %v", err)
			}
			if result.Classification != tt.wantClass {
				t.Errorf("Classification = %v, want %v", result.Classification, tt.wantClass)
			}
			if result.LabelID != tt.wantLabelID {
				t.Errorf("LabelID = %q, want %q", result.LabelID, tt.wantLabelID)
			}
			if !strings.Contains(result.Remedy, tt.wantRemedyHas) {
				t.Errorf("Remedy = %q, want to contain %q", result.Remedy, tt.wantRemedyHas)
			}
		})
	}
}

func TestClassifyLabel_AnotherTeam_ReportsOwningTeam(t *testing.T) {
	server := classifyLabelServer(t,
		`[{"id": "label-other", "name": "pilot", "team": {"id": "team-eng", "key": "ENG", "name": "Engineering"}}]`)
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	result, err := client.ClassifyLabel(context.Background(), "ROU", "pilot")
	if err != nil {
		t.Fatalf("ClassifyLabel: %v", err)
	}
	if result.OwningTeam == nil {
		t.Fatal("expected OwningTeam to be set for the another_team classification")
	}
	if result.OwningTeam.Key != "ENG" {
		t.Errorf("OwningTeam.Key = %q, want ENG", result.OwningTeam.Key)
	}
}

func TestClassifyLabel_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "internal error"}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeLinearToken, server.URL)
	_, err := client.ClassifyLabel(context.Background(), "ROU", "pilot")
	if err == nil {
		t.Fatal("expected error from API failure")
	}
	if !strings.Contains(err.Error(), "API error:") {
		t.Errorf("error = %v, want to contain %q", err, "API error:")
	}
}
