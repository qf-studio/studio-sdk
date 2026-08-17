package github

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// mergedWorkTestServer is a routable fake GitHub API for exercising
// hasMergedWork's default-branch resolution + merged-PR detection (GH-117).
type mergedWorkTestServer struct {
	server *httptest.Server

	mu           sync.Mutex
	repoCalls    int
	addedLabels  []string
	removedLabel []string

	// repoStatus/repoBody control the GET /repos/{owner}/{repo} response.
	repoStatus int
	repoBody   string

	// searchTotalCount controls the GET /search/issues response. searchWantBase,
	// if non-empty, asserts the query carries that base: qualifier.
	searchTotalCount int
	searchWantBase   string

	// pullsResponse controls the GET /repos/{owner}/{repo}/pulls response body.
	pullsResponse string
}

func newMergedWorkTestServer() *mergedWorkTestServer {
	ts := &mergedWorkTestServer{repoStatus: http.StatusOK, pullsResponse: "[]"}
	ts.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo":
			ts.mu.Lock()
			ts.repoCalls++
			status := ts.repoStatus
			body := ts.repoBody
			ts.mu.Unlock()
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
			ts.mu.Lock()
			total := ts.searchTotalCount
			wantBase := ts.searchWantBase
			ts.mu.Unlock()
			if wantBase != "" && !strings.Contains(r.URL.Query().Get("q"), "base:"+wantBase) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":"missing base qualifier"}`))
				return
			}
			_, _ = w.Write([]byte(`{"total_count":` + jsonInt(total) + `}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/pulls":
			ts.mu.Lock()
			body := ts.pullsResponse
			ts.mu.Unlock()
			_, _ = w.Write([]byte(body))
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/labels"):
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			ts.mu.Lock()
			ts.addedLabels = append(ts.addedLabels, body.Labels...)
			ts.mu.Unlock()
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/labels/"):
			parts := strings.Split(r.URL.Path, "/")
			label := parts[len(parts)-1]
			ts.mu.Lock()
			ts.removedLabel = append(ts.removedLabel, label)
			ts.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		}
	}))
	return ts
}

func (ts *mergedWorkTestServer) close() { ts.server.Close() }

func (ts *mergedWorkTestServer) repoCallCount() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.repoCalls
}

func (ts *mergedWorkTestServer) hasAddedLabel(label string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, l := range ts.addedLabels {
		if l == label {
			return true
		}
	}
	return false
}

func jsonInt(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func mergedWorkTestIssue() *Issue {
	return &Issue{Number: 117, Title: "fix(github/poller): hasMergedWork base check", State: "open"}
}

// TestHasMergedWork_DefaultBranchMerge_MarksDone is the unchanged-behavior
// case: a PR merged into the repo's default branch is real delivery.
func TestHasMergedWork_DefaultBranchMerge_MarksDone(t *testing.T) {
	ts := newMergedWorkTestServer()
	defer ts.close()
	ts.repoBody = `{"default_branch":"main"}`
	ts.searchTotalCount = 0
	ts.searchWantBase = "main"
	ts.pullsResponse = `[{"number":200,"merged_at":"2026-08-15T10:00:00Z","base":{"ref":"main"}}]`

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, ts.server.URL)
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	issue := mergedWorkTestIssue()
	if !poller.hasMergedWork(context.Background(), issue) {
		t.Fatal("expected hasMergedWork = true for a default-branch merge")
	}
	if !ts.hasAddedLabel(LabelDone) {
		t.Error("expected pilot-done label to be added")
	}
}

// TestHasMergedWork_NonDefaultBaseMerge_DoesNotMarkDone is the GH-117
// regression case: a stacked PR squash-merged into its stack parent branch
// (not main) must NOT self-seal the issue as delivered.
func TestHasMergedWork_NonDefaultBaseMerge_DoesNotMarkDone(t *testing.T) {
	ts := newMergedWorkTestServer()
	defer ts.close()
	ts.repoBody = `{"default_branch":"main"}`
	ts.searchTotalCount = 0
	ts.searchWantBase = "main"
	// Stacked merge: PR landed on pilot/GH-70, not the default branch.
	ts.pullsResponse = `[{"number":201,"merged_at":"2026-08-15T10:00:00Z","base":{"ref":"pilot/GH-70"}}]`

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, ts.server.URL)
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	issue := mergedWorkTestIssue()
	if poller.hasMergedWork(context.Background(), issue) {
		t.Fatal("expected hasMergedWork = false for a merge into a non-default base")
	}
	if ts.hasAddedLabel(LabelDone) {
		t.Error("pilot-done should not be added for a non-default-base merge")
	}

	// shouldRetryFailedIssue must not be blocked by the false-positive merge.
	poller.mu.Lock()
	poller.failedRetryCount[issue.Number] = 0
	poller.mu.Unlock()
	issue.Labels = []Label{{Name: LabelFailed}}
	if !poller.shouldRetryFailedIssue(context.Background(), issue) {
		t.Error("shouldRetryFailedIssue should allow retry when the only merge is on a non-default base")
	}
}

// TestHasMergedWork_DefaultBranchFetchFails_FallsBackToLegacy verifies the
// fail-open contract: if GetRepository errors, hasMergedWork falls back to
// the base-blind legacy checks (no polling wedge), logging exactly one WARN
// about the default-branch resolution failure — even across repeated calls,
// since resolution is cached per poller instance.
func TestHasMergedWork_DefaultBranchFetchFails_FallsBackToLegacy(t *testing.T) {
	ts := newMergedWorkTestServer()
	defer ts.close()
	ts.repoStatus = http.StatusInternalServerError
	ts.repoBody = `{"message":"internal error"}`
	// Legacy (base-blind) search finds a merged PR — old behavior preserved.
	ts.searchTotalCount = 1

	handler := &capturingHandler{}
	logger := slog.New(handler)

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, ts.server.URL)
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second, WithPollerLogger(logger))
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	issue1 := mergedWorkTestIssue()
	if !poller.hasMergedWork(context.Background(), issue1) {
		t.Fatal("expected hasMergedWork = true via legacy fallback when default branch resolution fails")
	}

	issue2 := &Issue{Number: 118, Title: "second issue", State: "open"}
	poller.hasMergedWork(context.Background(), issue2)

	if got := ts.repoCallCount(); got != 1 {
		t.Errorf("GetRepository should be called exactly once (cached), got %d calls", got)
	}

	warnCount := 0
	for _, r := range handler.records {
		if r.Level == slog.LevelWarn && strings.Contains(r.Message, "Failed to resolve default branch") {
			warnCount++
		}
	}
	if warnCount != 1 {
		t.Errorf("expected exactly 1 WARN about default branch resolution, got %d", warnCount)
	}
}
