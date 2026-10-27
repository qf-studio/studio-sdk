package github

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// runOverlapPoll runs one poll over issues and returns dispatched numbers, the
// captured log and the poller.
func runOverlapPoll(t *testing.T, issues []*Issue, prTimeout time.Duration, pre func(p *Poller)) ([]int, string, *Poller) {
	t.Helper()
	byNumber := map[string]*Issue{}
	for _, i := range issues {
		byNumber[strconv.Itoa(i.Number)] = i
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if i, ok := byNumber[parts[len(parts)-1]]; ok {
			_, _ = w.Write(mustJSON(i))
			return
		}
		_, _ = w.Write(mustJSON(issues))
	}))
	defer server.Close()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	var mu sync.Mutex
	var dispatched []int
	p, err := NewPoller(NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL), "owner/repo", "pilot", 30*time.Second,
		WithPollerLogger(logger),
		WithExecutionMode(ExecutionModeAuto),
		WithSequentialConfig(false, time.Second, prTimeout),
		WithOnIssue(func(ctx context.Context, issue *Issue) error {
			mu.Lock()
			dispatched = append(dispatched, issue.Number)
			mu.Unlock()
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}
	if pre != nil {
		pre(p)
	}
	p.checkForNewIssues(context.Background())
	p.WaitForActive()
	mu.Lock()
	defer mu.Unlock()
	return dispatched, buf.String(), p
}

func hasNumber(nums []int, n int) bool {
	for _, x := range nums {
		if x == n {
			return true
		}
	}
	return false
}

func TestCheckForNewIssuesOpenPROverlap_InProgressHolderDefers(t *testing.T) {
	now := time.Now()
	pilot := Label{Name: "pilot"}
	issues := []*Issue{
		{Number: 1, Title: "A", Body: "Edit internal/auth/a.go", Labels: []Label{pilot, {Name: LabelInProgress}}, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "B", Body: "Edit internal/auth/b.go", Labels: []Label{pilot}, CreatedAt: now.Add(-1 * time.Hour)},
	}
	got, logs, _ := runOverlapPoll(t, issues, time.Hour, nil)
	if hasNumber(got, 2) {
		t.Errorf("B dispatched while older overlapping A is in progress: %v", got)
	}
	if !strings.Contains(logs, "Deferring issue: overlaps open PR of older issue") || !strings.Contains(logs, "holder=1") || !strings.Contains(logs, "internal/auth") {
		t.Errorf("missing deferral log: %s", logs)
	}
}

func TestCheckForNewIssuesOpenPROverlap_DoneOpenHolderDefers(t *testing.T) {
	now := time.Now()
	pilot := Label{Name: "pilot"}
	issues := []*Issue{
		{Number: 1, Title: "A", Body: "Edit internal/auth/a.go", Labels: []Label{pilot, {Name: LabelDone}}, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "B", Body: "Edit internal/auth/b.go", Labels: []Label{pilot}, CreatedAt: now.Add(-1 * time.Hour)},
	}
	got, _, _ := runOverlapPoll(t, issues, time.Hour, nil)
	if hasNumber(got, 2) {
		t.Errorf("B dispatched while A's PR is unmerged: %v", got)
	}
}

func TestCheckForNewIssuesOpenPROverlap_ClosedHolderDispatches(t *testing.T) {
	now := time.Now()
	pilot := Label{Name: "pilot"}
	// A is closed, so it is absent from the open-issue list.
	issues := []*Issue{
		{Number: 2, Title: "B", Body: "Edit internal/auth/b.go", Labels: []Label{pilot}, CreatedAt: now.Add(-1 * time.Hour)},
	}
	got, logs, _ := runOverlapPoll(t, issues, time.Hour, func(p *Poller) {
		p.overlapHolders[1] = &overlapHolderState{since: now.Add(-time.Minute)}
	})
	if !hasNumber(got, 2) {
		t.Errorf("B not dispatched after A closed: %v", got)
	}
	if strings.Contains(logs, "Deferring issue") {
		t.Errorf("unexpected deferral: %s", logs)
	}
}

func TestCheckForNewIssuesOpenPROverlap_ClearsStaleEntry(t *testing.T) {
	now := time.Now()
	issues := []*Issue{
		{Number: 2, Title: "B", Body: "x", Labels: []Label{{Name: "pilot"}}, CreatedAt: now},
	}
	_, _, p := runOverlapPoll(t, issues, time.Hour, func(p *Poller) {
		p.overlapHolders[1] = &overlapHolderState{since: now}
	})
	if _, ok := p.overlapHolders[1]; ok {
		t.Error("holder entry for closed issue not cleared")
	}
}

func TestCheckForNewIssuesOpenPROverlap_ExpiredHolderReleases(t *testing.T) {
	now := time.Now()
	pilot := Label{Name: "pilot"}
	issues := []*Issue{
		{Number: 1, Title: "A", Body: "Edit internal/auth/a.go", Labels: []Label{pilot, {Name: LabelDone}}, CreatedAt: now.Add(-3 * time.Hour)},
		{Number: 2, Title: "B", Body: "Edit internal/auth/b.go", Labels: []Label{pilot}, CreatedAt: now.Add(-1 * time.Hour)},
	}
	got, logs, _ := runOverlapPoll(t, issues, time.Hour, func(p *Poller) {
		p.overlapHolders[1] = &overlapHolderState{since: now.Add(-2 * time.Hour)}
	})
	if !hasNumber(got, 2) {
		t.Errorf("B not dispatched after hold expired: %v", got)
	}
	if n := strings.Count(logs, "overlap hold expired"); n != 1 {
		t.Errorf("expiry warning logged %d times, want 1: %s", n, logs)
	}
}

func TestCheckForNewIssuesOpenPROverlap_NonOverlappingDispatches(t *testing.T) {
	now := time.Now()
	pilot := Label{Name: "pilot"}
	issues := []*Issue{
		{Number: 1, Title: "A", Body: "Edit internal/auth/a.go", Labels: []Label{pilot, {Name: LabelInProgress}}, CreatedAt: now.Add(-2 * time.Hour)},
		{Number: 2, Title: "B", Body: "Edit internal/auth/b.go", Labels: []Label{pilot}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 3, Title: "C", Body: "Edit internal/billing/c.go", Labels: []Label{pilot}, CreatedAt: now.Add(-30 * time.Minute)},
	}
	got, _, _ := runOverlapPoll(t, issues, time.Hour, nil)
	if !hasNumber(got, 3) || hasNumber(got, 2) {
		t.Errorf("dispatched = %v, want C only", got)
	}
}

func TestCheckForNewIssuesOpenPROverlap_NewerHolderDoesNotBlock(t *testing.T) {
	now := time.Now()
	pilot := Label{Name: "pilot"}
	issues := []*Issue{
		{Number: 1, Title: "newer in flight", Body: "Edit internal/auth/a.go", Labels: []Label{pilot, {Name: LabelInProgress}}, CreatedAt: now.Add(-1 * time.Hour)},
		{Number: 2, Title: "older candidate", Body: "Edit internal/auth/b.go", Labels: []Label{pilot}, CreatedAt: now.Add(-2 * time.Hour)},
	}
	got, _, _ := runOverlapPoll(t, issues, time.Hour, nil)
	if !hasNumber(got, 2) {
		t.Errorf("older B blocked by newer holder: %v", got)
	}
}
