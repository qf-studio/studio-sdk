package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// boardRetryTestServer serves both the REST issue-refresh endpoint and the GraphQL
// project-board endpoint needed to drive a board-sourced dispatch through
// checkForNewIssues end to end: candidate discovery (FindIssuesFromProject),
// confirmed-dispatch sync (syncBoardStatusInProgress), and — the behavior under
// test — the post-failure revert (syncBoardStatusRetry, GH-4474).
//
// The project item's "current" status is always reported as inProgressOption, so
// syncBoardStatusInProgress (target: in-progress) is idempotent (no mutation) while
// syncBoardStatusRetry (target: source status) always sees a mismatch and fires a
// mutation — letting tests assert exactly one recorded mutation, with the reverted
// status as its payload.
type boardRetryTestServer struct {
	server *httptest.Server

	mu        sync.Mutex
	mutations []string // status names written via updateProjectV2ItemFieldValue
}

func newBoardRetryTestServer(t *testing.T, issue *Issue, sourceStatus, inProgressStatus string) *boardRetryTestServer {
	t.Helper()

	const projectID = "PVT_test"
	const itemID = "PVTI_test"
	optionIDs := map[string]string{
		strings.ToLower(sourceStatus):     "OPT_source",
		strings.ToLower(inProgressStatus): "OPT_inprogress",
	}
	nameByOption := map[string]string{
		"OPT_source":     sourceStatus,
		"OPT_inprogress": inProgressStatus,
	}

	ts := &boardRetryTestServer{}
	ts.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.HasPrefix(r.URL.Path, "/repos/") {
			// Fresh-label-check GET before dispatch confirmation.
			_, _ = w.Write(mustJSON(issue))
			return
		}

		var req GraphQLRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode graphql request: %v", err)
		}

		switch {
		case strings.Contains(req.Query, "items(first: 100"):
			// Board-source candidate listing.
			_, _ = w.Write([]byte(itemsResp([]map[string]interface{}{
				issueNode(issue.Number, issue.NodeID, issue.Title, issue.Body, "OPEN", "org/repo", sourceStatus, "pilot"),
			}, false, "")))

		case strings.Contains(req.Query, "organization"):
			_, _ = w.Write([]byte(orgProjectResp(projectID)))

		case strings.Contains(req.Query, "field(name:"):
			opts := []map[string]interface{}{
				{"id": "OPT_source", "name": sourceStatus},
				{"id": "OPT_inprogress", "name": inProgressStatus},
			}
			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"node": map[string]interface{}{
						"field": map[string]interface{}{
							"id":      "FIELD_status",
							"options": opts,
						},
					},
				},
			}
			b, _ := json.Marshal(resp)
			_, _ = w.Write(b)

		case strings.Contains(req.Query, "projectItems(first:"):
			resp := map[string]interface{}{
				"data": map[string]interface{}{
					"node": map[string]interface{}{
						"projectItems": map[string]interface{}{
							"nodes": []map[string]interface{}{
								{
									"id":               itemID,
									"project":          map[string]interface{}{"id": projectID},
									"fieldValueByName": map[string]interface{}{"optionId": optionIDs[strings.ToLower(inProgressStatus)]},
								},
							},
						},
					},
				},
			}
			b, _ := json.Marshal(resp)
			_, _ = w.Write(b)

		case strings.Contains(req.Query, "updateProjectV2ItemFieldValue"):
			optionID, _ := req.Variables["optionID"].(string)
			ts.mu.Lock()
			ts.mutations = append(ts.mutations, nameByOption[optionID])
			ts.mu.Unlock()
			_, _ = w.Write([]byte(`{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"` + itemID + `"}}}}`))

		default:
			t.Fatalf("unexpected graphql query: %s", req.Query)
		}
	}))
	return ts
}

func (ts *boardRetryTestServer) close() { ts.server.Close() }

func (ts *boardRetryTestServer) recordedMutations() []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]string(nil), ts.mutations...)
}

func boardRetryTestIssue() *Issue {
	return &Issue{
		Number:    99,
		NodeID:    "ISSUE_99",
		Title:     "Board-sourced task",
		Body:      "Plain body, no dependencies",
		State:     "open",
		Labels:    []Label{{Name: "pilot"}},
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}
}

// TestPoller_ParallelUnmark_RevertsBoardCardToSourceColumn is the GH-4474 regression
// test: a board-sourced dispatch that fails before producing a PR must move the card
// back to the source column, not strand it in the in-progress column where
// FindIssuesFromProject can never see it again.
func TestPoller_ParallelUnmark_RevertsBoardCardToSourceColumn(t *testing.T) {
	tests := []struct {
		name          string
		handlerResult *IssueResult
		handlerErr    error
		wantMutations []string // expected board status writes, in order
		wantProcessed bool     // IsProcessed(issue.Number) after settling
	}{
		{
			name:          "execution error unmarks and reverts card to source column",
			handlerErr:    errors.New("spec-guard rejected: missing acceptance criteria"),
			wantMutations: []string{"Todo"},
			wantProcessed: false,
		},
		{
			name:          "unsuccessful result with no PR unmarks and reverts card to source column",
			handlerResult: &IssueResult{Success: false, PRNumber: 0},
			wantMutations: []string{"Todo"},
			wantProcessed: false,
		},
		{
			name:          "successful result leaves card in the in-progress column",
			handlerResult: &IssueResult{Success: true, PRNumber: 7},
			wantMutations: nil,
			wantProcessed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := boardRetryTestIssue()
			ts := newBoardRetryTestServer(t, issue, "Todo", "In Dev")
			defer ts.close()

			client := NewClientWithBaseURL(testutil.FakeGitHubToken, ts.server.URL)
			src := NewProjectBoardSource(client, &ProjectBoardConfig{
				ProjectNumber: 9,
				StatusField:   "Status",
				SourceEnabled: true,
				SourceStatus:  "Todo",
			}, "org", "repo")
			bsync := NewProjectBoardSync(client, &ProjectBoardConfig{
				Enabled:       true,
				ProjectNumber: 9,
				StatusField:   "Status",
			}, "org")

			poller, err := NewPoller(client, "org/repo", "pilot", 30*time.Second,
				WithOnIssueWithResult(func(ctx context.Context, iss *Issue) (*IssueResult, error) {
					return tt.handlerResult, tt.handlerErr
				}),
				WithProjectBoardSource(src),
				WithBoardSync(bsync, "In Dev"),
				WithRetryGracePeriod(0),
			)
			if err != nil {
				t.Fatalf("NewPoller: %v", err)
			}

			poller.checkForNewIssues(context.Background())
			poller.WaitForActive()

			gotMutations := ts.recordedMutations()
			if len(gotMutations) != len(tt.wantMutations) {
				t.Fatalf("board status mutations = %v, want %v", gotMutations, tt.wantMutations)
			}
			for i, want := range tt.wantMutations {
				if gotMutations[i] != want {
					t.Errorf("mutation[%d] = %q, want %q", i, gotMutations[i], want)
				}
			}

			if poller.IsProcessed(issue.Number) != tt.wantProcessed {
				t.Errorf("IsProcessed(%d) = %v, want %v", issue.Number, poller.IsProcessed(issue.Number), tt.wantProcessed)
			}
		})
	}
}

// TestPoller_SyncBoardStatusRetry_NoOpWithoutBoardSource verifies that reverting the
// board card on failure is skipped entirely for label-mode dispatch (no board source
// configured) — there is no card to revert, and the call must not panic or attempt a
// network round-trip.
func TestPoller_SyncBoardStatusRetry_NoOpWithoutBoardSource(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	bsync := NewProjectBoardSync(client, &ProjectBoardConfig{
		Enabled:       true,
		ProjectNumber: 1,
		StatusField:   "Status",
	}, "org")

	poller, err := NewPoller(client, "org/repo", "pilot", 0, WithBoardSync(bsync, "In Dev"))
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	poller.syncBoardStatusRetry(context.Background(), boardRetryTestIssue())

	if calls != 0 {
		t.Errorf("expected no network calls when projectBoardSource is nil, got %d", calls)
	}
}

// TestPoller_SyncBoardStatusRetry_DefaultsToTodo verifies the same "Todo" default
// used by fetchCandidates applies to the revert path when SourceStatus is unset.
func TestPoller_SyncBoardStatusRetry_DefaultsToTodo(t *testing.T) {
	issue := boardRetryTestIssue()
	ts := newBoardRetryTestServer(t, issue, "Todo", "In Dev")
	defer ts.close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, ts.server.URL)
	src := NewProjectBoardSource(client, &ProjectBoardConfig{
		ProjectNumber: 9,
		StatusField:   "Status",
		SourceEnabled: true,
		// SourceStatus intentionally empty.
	}, "org", "repo")
	bsync := NewProjectBoardSync(client, &ProjectBoardConfig{
		Enabled:       true,
		ProjectNumber: 9,
		StatusField:   "Status",
	}, "org")

	poller, err := NewPoller(client, "org/repo", "pilot", 0,
		WithProjectBoardSource(src),
		WithBoardSync(bsync, "In Dev"),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	poller.syncBoardStatusRetry(context.Background(), issue)

	got := ts.recordedMutations()
	if len(got) != 1 || got[0] != "Todo" {
		t.Errorf("mutations = %v, want [Todo] (default source status)", got)
	}
}
