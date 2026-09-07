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
	ts.pullsResponse = `[{"number":200,"title":"GH-117: fix the thing","merged_at":"2026-08-15T10:00:00Z","base":{"ref":"main"}}]`

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

// TestHasMergedWork_BranchLookup_ReusedByOtherIssue_DoesNotMarkDone is the
// GH-138 regression case: issue #275's branch pilot/GH-275 was reused by a
// follow-up issue (#277) whose PR (#278) merged into main. The branch
// lookup must not treat that unrelated merge as delivery of #275 — it must
// check that the merged PR actually references #275 (pilot-console incident,
// 2026-09-07).
func TestHasMergedWork_BranchLookup_ReusedByOtherIssue_DoesNotMarkDone(t *testing.T) {
	ts := newMergedWorkTestServer()
	defer ts.close()
	ts.repoBody = `{"default_branch":"main"}`
	ts.searchTotalCount = 0
	ts.searchWantBase = "main"
	// Merged PR is on branch pilot/GH-275 (the fixture's lookup branch), but
	// its title/body reference issue #277, not #275.
	ts.pullsResponse = `[{"number":278,"title":"GH-277: continue after CI flake","body":"Closes #277","merged_at":"2026-09-07T10:00:00Z","base":{"ref":"main"}}]`

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, ts.server.URL)
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	issue := &Issue{Number: 275, Title: "original issue", State: "open"}
	if poller.hasMergedWork(context.Background(), issue) {
		t.Fatal("expected hasMergedWork = false when the merged PR on the branch references a different issue")
	}
	if ts.hasAddedLabel(LabelDone) {
		t.Error("pilot-done should not be added when the branch's merged PR belongs to a different issue")
	}
}

// TestHasMergedWork_BranchLookup_AutopilotCIFixBody_DoesNotMarkDone is the
// GH-140 regression case: the real pilot-console PR #278 body (the autopilot
// CI-fix template) contains a bare "- **Original Issue**: #275" metadata
// line alongside "Closes #277". prReferencesIssue must not treat the bare
// #275 mention as delivery of #275 — only #277 (the PR's actual closing
// keyword target) is marked done. The #139 fixture used "Closes #277" only,
// which is not the template's real shape and did not catch this.
func TestHasMergedWork_BranchLookup_AutopilotCIFixBody_DoesNotMarkDone(t *testing.T) {
	ts := newMergedWorkTestServer()
	defer ts.close()
	ts.repoBody = `{"default_branch":"main"}`
	ts.searchTotalCount = 0
	ts.searchWantBase = "main"
	ts.pullsResponse = `[{"number":278,"title":"GH-277: fix(ci): resolve flaky test",` +
		`"body":"- **Original Issue**: #275\n\nCloses #277",` +
		`"merged_at":"2026-09-07T10:00:00Z","base":{"ref":"main"}}]`

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, ts.server.URL)
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	issue275 := &Issue{Number: 275, Title: "original issue", State: "open"}
	if poller.hasMergedWork(context.Background(), issue275) {
		t.Fatal("expected hasMergedWork = false for #275: the PR only bare-mentions it in the Original Issue metadata line")
	}
	if ts.hasAddedLabel(LabelDone) {
		t.Error("pilot-done should not be added to #275 from the autopilot CI-fix PR's Original Issue line")
	}

	ts.mu.Lock()
	ts.addedLabels = nil
	ts.mu.Unlock()

	issue277 := &Issue{Number: 277, Title: "CI flake", State: "open"}
	if !poller.hasMergedWork(context.Background(), issue277) {
		t.Fatal("expected hasMergedWork = true for #277: the PR's title and \"Closes #277\" both reference it")
	}
	if !ts.hasAddedLabel(LabelDone) {
		t.Error("expected pilot-done label to be added to #277")
	}
}

// TestHasMergedWork_BranchLookupLegacy_ReusedByOtherIssue_DoesNotMarkDone is
// the legacy-path (default branch unresolved) variant of
// TestHasMergedWork_BranchLookup_ReusedByOtherIssue_DoesNotMarkDone: when
// GetRepository fails, hasMergedWork falls back to FindMergedPRByBranch
// (base-blind). That fallback must still reject a merged PR found on
// pilot/GH-<n> whose body only bare-mentions the issue rather than closing
// it (GH-140).
func TestHasMergedWork_BranchLookupLegacy_ReusedByOtherIssue_DoesNotMarkDone(t *testing.T) {
	ts := newMergedWorkTestServer()
	defer ts.close()
	ts.repoStatus = http.StatusInternalServerError
	ts.repoBody = `{"message":"internal error"}`
	ts.searchTotalCount = 0
	ts.pullsResponse = `[{"number":278,"title":"GH-277: fix(ci): resolve flaky test",` +
		`"body":"- **Original Issue**: #275\n\nCloses #277",` +
		`"merged_at":"2026-09-07T10:00:00Z","base":{"ref":"main"}}]`

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, ts.server.URL)
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	issue := &Issue{Number: 275, Title: "original issue", State: "open"}
	if poller.hasMergedWork(context.Background(), issue) {
		t.Fatal("expected hasMergedWork = false via legacy branch lookup when the merged PR only bare-mentions this issue")
	}
	if ts.hasAddedLabel(LabelDone) {
		t.Error("pilot-done should not be added via legacy branch lookup from a bare issue mention")
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

// TestPrReferencesIssue covers the GH-140 fix directly: a bare "#<n>"
// reference anywhere in a PR body no longer counts as delivery. Only a
// "GH-<n>:" title prefix, or a closing keyword (close/fix/resolve, in their
// inflections) immediately followed by "#<n>", "GH-<n>", or an
// ".../issues/<n>" URL, counts.
func TestPrReferencesIssue(t *testing.T) {
	tests := []struct {
		name  string
		title string
		body  string
		want  bool
	}{
		{
			name:  "autopilot CI-fix template Original Issue line is not delivery",
			title: "GH-277: fix(ci): resolve flaky test",
			body:  "- **Original Issue**: #275\n\nCloses #277",
			want:  false,
		},
		{
			name: "closing keyword still marks the issue done",
			body: "Closes #275",
			want: true,
		},
		{
			name: "reverts PR reference is not delivery",
			body: "Reverts PR #275",
			want: false,
		},
		{
			name: "refs reference is not delivery",
			body: "Refs #275",
			want: false,
		},
		{
			name: "follow-up reference is not delivery",
			body: "Follow-up to #275",
			want: false,
		},
		{
			name: "closing keyword with full issues URL marks the issue done",
			body: "Closes https://github.com/o/r/issues/275",
			want: true,
		},
		{
			name: "closing keyword must be word-bounded, #27 does not match #275",
			body: "Closes #27",
			want: false,
		},
	}

	const issueNumber = 275
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr := &PullRequest{Title: tt.title, Body: tt.body}
			if got := prReferencesIssue(pr, issueNumber); got != tt.want {
				t.Errorf("prReferencesIssue(title=%q, body=%q, issue=%d) = %v, want %v",
					tt.title, tt.body, issueNumber, got, tt.want)
			}
		})
	}
}
