package github

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// --- hook fakes ---

type fakeTaskChecker struct {
	mu     sync.Mutex
	queued bool
	calls  []string
}

func (f *fakeTaskChecker) IsTaskQueued(taskID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, taskID)
	return f.queued
}

type fakeExecChecker struct {
	mu          sync.Mutex
	completed   bool
	checkErr    error
	invalidated []string
}

func (f *fakeExecChecker) HasCompletedExecution(taskID, projectPath string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.completed, f.checkErr
}

func (f *fakeExecChecker) InvalidateCompletion(taskID, projectPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidated = append(f.invalidated, taskID)
	return nil
}

type fakeJudge struct {
	verdict core.Verdict
	err     error
}

func (f *fakeJudge) JudgeIssue(_ context.Context, _, _, _ string) (core.Verdict, error) {
	return f.verdict, f.err
}

type declinedRecord struct {
	taskID, projectPath, status, reason string
}

type fakeExecSaver struct {
	mu      sync.Mutex
	records []declinedRecord
}

func (f *fakeExecSaver) SaveDeclinedExecution(taskID, projectPath, status, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, declinedRecord{taskID, projectPath, status, reason})
	return nil
}

// fakeExecSaverV2 implements core.ExecutionSaverV2. legacyCalls tracks calls
// to the embedded ExecutionSaver method so tests can assert the poller
// prefers SaveDeclinedExecutionRecord when both are available.
type fakeExecSaverV2 struct {
	mu          sync.Mutex
	records     []core.DeclinedExecutionRecord
	legacyCalls int
}

func (f *fakeExecSaverV2) SaveDeclinedExecution(taskID, projectPath, status, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.legacyCalls++
	return nil
}

func (f *fakeExecSaverV2) SaveDeclinedExecutionRecord(rec core.DeclinedExecutionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, rec)
	return nil
}

type fakeIssueMetrics struct {
	mu      sync.Mutex
	results []string
}

func (f *fakeIssueMetrics) RecordIssueProcessed(result string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = append(f.results, result)
}

func (f *fakeIssueMetrics) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.results...)
}

type rateLimitCall struct {
	taskID, title, body, errText string
}

type fakeRateLimitScheduler struct {
	mu     sync.Mutex
	accept bool
	calls  []rateLimitCall
}

func (f *fakeRateLimitScheduler) QueueRetryIfRateLimited(taskID, title, body, errText string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, rateLimitCall{taskID, title, body, errText})
	return f.accept
}

func (f *fakeRateLimitScheduler) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// --- helpers ---

func hookTestIssue() *Issue {
	return &Issue{
		Number:    42,
		Title:     "Fix the thing",
		Body:      "Details here",
		State:     "open",
		Labels:    []Label{{Name: "pilot"}},
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}
}

func newHookPoller(t *testing.T, serverURL string, handled *int32, handledMu *sync.Mutex, opts ...PollerOption) *Poller {
	t.Helper()
	client := NewClientWithBaseURL(testutil.FakeGitHubToken, serverURL)
	base := []PollerOption{
		WithOnIssue(func(ctx context.Context, iss *Issue) error {
			handledMu.Lock()
			*handled++
			handledMu.Unlock()
			return nil
		}),
		WithRetryGracePeriod(0),
	}
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second, append(base, opts...)...)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}
	return poller
}

// --- pre-flight judge ---

func TestPoller_PreFlightReject_LabelsCommentsAndSkips(t *testing.T) {
	ts := newPollerTestServer(hookTestIssue())
	defer ts.close()

	judge := &fakeJudge{verdict: core.Verdict{
		Accepted: false, Decision: "too_vague", Reason: "no acceptance criteria", Confidence: 0.9,
	}}
	saver := &fakeExecSaver{}

	var handled int32
	var mu sync.Mutex
	poller := newHookPoller(t, ts.server.URL, &handled, &mu,
		WithPreFlightJudge(judge),
		WithExecutionSaver(saver),
		WithExecutionChecker(nil, "/tmp/proj"), // sets projectPath only; checker stays nil
	)
	poller.execChecker = nil // explicit: only projectPath threading is under test here

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	mu.Lock()
	got := handled
	mu.Unlock()
	if got != 0 {
		t.Fatalf("handler called %d times for pre-flight-rejected issue, want 0", got)
	}
	if poller.IsProcessed(42) {
		t.Error("rejected issue must NOT be marked processed — label removal re-triggers dispatch")
	}

	ts.mu.Lock()
	labels := append([]string(nil), ts.labels...)
	ts.mu.Unlock()
	found := false
	for _, l := range labels {
		if l == LabelNeedsClarification {
			found = true
		}
	}
	if !found {
		t.Errorf("needs-clarification label not added; labels added: %v", labels)
	}

	saver.mu.Lock()
	records := append([]declinedRecord(nil), saver.records...)
	saver.mu.Unlock()
	if len(records) != 1 {
		t.Fatalf("declined execution records = %d, want 1", len(records))
	}
	rec := records[0]
	if rec.taskID != "GH-42" || rec.status != "declined-preflight" ||
		rec.reason != "no acceptance criteria" || rec.projectPath != "/tmp/proj" {
		t.Errorf("unexpected declined record: %+v", rec)
	}
}

// TestPoller_PreFlightReject_ExecutionSaverV2CarriesRepoIdentity verifies the
// GH-111 fix: a declined issue's record carries the issue's actual repo
// (owner/name), not just the shared ProjectPath — the GH-4833 root cause was
// that a single checkout path polled against multiple repos collides when
// records are keyed on ProjectPath alone. The wired saver is deliberately a
// non-default repo ("acme/widget") with a ProjectPath that looks like a
// shared checkout, to prove RepoOwner/RepoName come from the issue's poller,
// not from ProjectPath.
func TestPoller_PreFlightReject_ExecutionSaverV2CarriesRepoIdentity(t *testing.T) {
	ts := newPollerTestServer(hookTestIssue())
	defer ts.close()

	judge := &fakeJudge{verdict: core.Verdict{
		Accepted: false, Decision: "too_vague", Reason: "no acceptance criteria", Confidence: 0.9,
	}}
	saver := &fakeExecSaverV2{}

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, ts.server.URL)
	poller, err := NewPoller(client, "acme/widget", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, iss *Issue) error { return nil }),
		WithRetryGracePeriod(0),
		WithPreFlightJudge(judge),
		WithExecutionSaver(saver),
		WithExecutionChecker(nil, "/tmp/shared-project"), // sets projectPath only; checker stays nil
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}
	poller.execChecker = nil // explicit: only projectPath threading is under test here

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	saver.mu.Lock()
	records := append([]core.DeclinedExecutionRecord(nil), saver.records...)
	legacyCalls := saver.legacyCalls
	saver.mu.Unlock()

	if legacyCalls != 0 {
		t.Errorf("legacy SaveDeclinedExecution called %d times; ExecutionSaverV2 must take priority", legacyCalls)
	}
	if len(records) != 1 {
		t.Fatalf("declined execution records = %d, want 1", len(records))
	}
	rec := records[0]
	if rec.RepoOwner != "acme" || rec.RepoName != "widget" {
		t.Errorf("declined record repo = %s/%s, want acme/widget (must reflect the issue's repo, not the shared project path)", rec.RepoOwner, rec.RepoName)
	}
	if rec.ProjectPath != "/tmp/shared-project" {
		t.Errorf("declined record projectPath = %q, want /tmp/shared-project", rec.ProjectPath)
	}
	if rec.TaskID != "GH-42" || rec.Status != "declined-preflight" || rec.Reason != "no acceptance criteria" {
		t.Errorf("unexpected declined record: %+v", rec)
	}
}

func TestPoller_PreFlightAccept_Dispatches(t *testing.T) {
	ts := newPollerTestServer(hookTestIssue())
	defer ts.close()

	var handled int32
	var mu sync.Mutex
	poller := newHookPoller(t, ts.server.URL, &handled, &mu,
		WithPreFlightJudge(&fakeJudge{verdict: core.Verdict{Accepted: true}}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	mu.Lock()
	defer mu.Unlock()
	if handled != 1 {
		t.Fatalf("handler called %d times for accepted issue, want 1", handled)
	}
}

func TestPoller_PreFlightError_FailsOpen(t *testing.T) {
	ts := newPollerTestServer(hookTestIssue())
	defer ts.close()

	var handled int32
	var mu sync.Mutex
	poller := newHookPoller(t, ts.server.URL, &handled, &mu,
		WithPreFlightJudge(&fakeJudge{err: errors.New("judge unavailable")}),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	mu.Lock()
	defer mu.Unlock()
	if handled != 1 {
		t.Fatalf("handler called %d times on judge error, want 1 (fail-open)", handled)
	}
}

// --- task checker ---

func TestPoller_TaskChecker_GatesRetryRedispatch(t *testing.T) {
	ts := newPollerTestServer(hookTestIssue())
	defer ts.close()

	tc := &fakeTaskChecker{queued: true}
	var handled int32
	var mu sync.Mutex
	poller := newHookPoller(t, ts.server.URL, &handled, &mu, WithTaskChecker(tc))

	// Simulate a prior dispatch: processed, but status labels since removed.
	poller.markProcessed(42)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	mu.Lock()
	got := handled
	mu.Unlock()
	if got != 0 {
		t.Fatalf("handler called %d times while task still queued, want 0", got)
	}
	tc.mu.Lock()
	calls := append([]string(nil), tc.calls...)
	tc.mu.Unlock()
	if len(calls) == 0 || calls[0] != "GH-42" {
		t.Fatalf("taskChecker calls = %v, want [GH-42 ...]", calls)
	}

	// Task drains → retry proceeds. WaitForActive latches stopping; clear it
	// so the second dispatch round can spawn its goroutine.
	poller.stopping.Store(false)
	tc.mu.Lock()
	tc.queued = false
	tc.mu.Unlock()

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	mu.Lock()
	defer mu.Unlock()
	if handled != 1 {
		t.Fatalf("handler called %d times after task drained, want 1", handled)
	}
}

// --- execution checker ---

func TestPoller_ExecChecker_SkipsCompletedExecution(t *testing.T) {
	ts := newPollerTestServer(hookTestIssue())
	defer ts.close()

	ec := &fakeExecChecker{completed: true}
	var handled int32
	var mu sync.Mutex
	poller := newHookPoller(t, ts.server.URL, &handled, &mu,
		WithExecutionChecker(ec, "/tmp/proj"),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	mu.Lock()
	got := handled
	mu.Unlock()
	if got != 0 {
		t.Fatalf("handler called %d times with completed execution, want 0", got)
	}
	if !poller.IsProcessed(42) {
		t.Error("issue with completed execution should be marked processed")
	}
}

func TestPoller_ExecChecker_ErrorFailsOpen(t *testing.T) {
	ts := newPollerTestServer(hookTestIssue())
	defer ts.close()

	ec := &fakeExecChecker{checkErr: errors.New("store down")}
	var handled int32
	var mu sync.Mutex
	poller := newHookPoller(t, ts.server.URL, &handled, &mu,
		WithExecutionChecker(ec, "/tmp/proj"),
	)

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	mu.Lock()
	defer mu.Unlock()
	if handled != 1 {
		t.Fatalf("handler called %d times on checker error, want 1 (fail-open)", handled)
	}
}

func TestPoller_RetryReady_InvalidatesCompletion(t *testing.T) {
	issue := hookTestIssue()
	issue.Labels = append(issue.Labels, Label{Name: LabelRetryReady})
	ts := newPollerTestServer(issue)
	defer ts.close()

	ec := &fakeExecChecker{}
	client := NewClientWithBaseURL(testutil.FakeGitHubToken, ts.server.URL)
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithExecutionChecker(ec, "/tmp/proj"),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	if !poller.shouldRetryRetryReadyIssue(context.Background(), issue) {
		t.Fatal("expected retry-ready issue to be approved for retry")
	}

	ec.mu.Lock()
	defer ec.mu.Unlock()
	if len(ec.invalidated) != 1 || ec.invalidated[0] != "GH-42" {
		t.Fatalf("InvalidateCompletion calls = %v, want [GH-42]", ec.invalidated)
	}
}

// --- rate-limit scheduler ---

func TestPoller_RateLimitScheduler_QueuedRetryStaysMarked(t *testing.T) {
	ts := newPollerTestServer(hookTestIssue())
	defer ts.close()

	rls := &fakeRateLimitScheduler{accept: true}
	metrics := &fakeIssueMetrics{}

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, ts.server.URL)
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, iss *Issue) error {
			return errors.New("Claude API rate limit reached; resets 3am")
		}),
		WithRetryGracePeriod(0),
		WithRateLimitScheduler(rls),
		WithIssueMetricsRecorder(metrics),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if rls.callCount() != 1 {
		t.Fatalf("rate-limit scheduler called %d times, want 1", rls.callCount())
	}
	if !poller.IsProcessed(42) {
		t.Error("rate-limit-queued issue must stay marked processed — the scheduler owns the retry")
	}
	results := metrics.recorded()
	if len(results) != 1 || results[0] != "rate_limited" {
		t.Errorf("issue metrics = %v, want [rate_limited]", results)
	}
}

func TestPoller_RateLimitScheduler_DeclinedFallsThroughToUnmark(t *testing.T) {
	ts := newPollerTestServer(hookTestIssue())
	defer ts.close()

	rls := &fakeRateLimitScheduler{accept: false}

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, ts.server.URL)
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, iss *Issue) error {
			return errors.New("ordinary failure")
		}),
		WithRetryGracePeriod(0),
		WithRateLimitScheduler(rls),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if rls.callCount() != 1 {
		t.Fatalf("rate-limit scheduler called %d times, want 1", rls.callCount())
	}
	if poller.IsProcessed(42) {
		t.Error("non-rate-limit failure should unmark the issue for normal retry")
	}
}

// --- adapter bridge ---

func TestAdapter_NewPoller_BridgesAllHooks(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Token = testutil.FakeGitHubToken
	cfg.Repo = "owner/repo"
	cfg.ProjectBoard = &ProjectBoardConfig{
		Enabled:       true,
		ProjectNumber: 7,
		StatusField:   "Status",
		Statuses:      ProjectStatuses{InProgress: "In Dev"},
		SourceEnabled: true,
		SourceStatus:  "Todo",
	}

	a := New(cfg)
	deps := core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(ctx context.Context, ev core.IssueEvent) (*core.IssueResult, error) {
			return nil, nil
		}),
		TaskChecker:          &fakeTaskChecker{},
		ExecutionChecker:     &fakeExecChecker{},
		ProjectPath:          "/tmp/proj",
		PreFlightJudge:       &fakeJudge{},
		ExecutionSaver:       &fakeExecSaver{},
		IssueMetricsRecorder: &fakeIssueMetrics{},
		RateLimitScheduler:   &fakeRateLimitScheduler{},
	}

	p, ok := a.NewPoller(deps).(*Poller)
	if !ok {
		t.Fatal("NewPoller did not return *Poller")
	}

	if p.taskChecker == nil {
		t.Error("TaskChecker not bridged")
	}
	if p.execChecker == nil {
		t.Error("ExecutionChecker not bridged")
	}
	if p.projectPath != "/tmp/proj" {
		t.Errorf("projectPath = %q, want /tmp/proj", p.projectPath)
	}
	if p.preFlightJudge == nil {
		t.Error("PreFlightJudge not bridged")
	}
	if p.execSaver == nil {
		t.Error("ExecutionSaver not bridged")
	}
	if p.issueMetrics == nil {
		t.Error("IssueMetricsRecorder not bridged")
	}
	if p.rateLimitScheduler == nil {
		t.Error("RateLimitScheduler not bridged")
	}
	if p.boardSync == nil || p.inProgressStatus != "In Dev" {
		t.Error("board sync not constructed from ProjectBoardConfig")
	}
	if p.projectBoardSource == nil {
		t.Error("board source not constructed despite SourceEnabled")
	}
}
