package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// pollerTestServer is a minimal test server for poller tests.
type pollerTestServer struct {
	server *httptest.Server
	mu     sync.Mutex
	labels []string
}

func newPollerTestServer(issue *Issue) *pollerTestServer {
	ts := &pollerTestServer{}
	ts.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/labels"):
			var body struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			ts.mu.Lock()
			ts.labels = append(ts.labels, body.Labels...)
			ts.mu.Unlock()
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/labels/"):
			parts := strings.Split(r.URL.Path, "/")
			label := parts[len(parts)-1]
			ts.mu.Lock()
			newLabels := ts.labels[:0]
			for _, l := range ts.labels {
				if l != label {
					newLabels = append(newLabels, l)
				}
			}
			ts.labels = newLabels
			ts.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			lastSegment := ""
			if len(parts) > 0 {
				lastSegment = parts[len(parts)-1]
			}
			isSingleGet := len(lastSegment) > 0 && lastSegment[0] >= '0' && lastSegment[0] <= '9'
			if isSingleGet {
				_, _ = w.Write(mustJSON(issue))
			} else {
				_, _ = w.Write(mustJSON([]*Issue{issue}))
			}
		}
	}))
	return ts
}

func (ts *pollerTestServer) close() { ts.server.Close() }

func TestPoller_ParallelDispatch_CallsHandler(t *testing.T) {
	pilot := Label{Name: "pilot"}
	issue := &Issue{
		Number:    42,
		Title:     "Fix the thing",
		Body:      "Details here",
		State:     "open",
		Labels:    []Label{pilot},
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}

	ts := newPollerTestServer(issue)
	defer ts.close()

	var handled sync.WaitGroup
	handled.Add(1)
	var handledIssue *Issue
	var mu sync.Mutex

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, ts.server.URL)
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, iss *Issue) error {
			mu.Lock()
			handledIssue = iss
			mu.Unlock()
			handled.Done()
			return nil
		}),
		WithRetryGracePeriod(0),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	mu.Lock()
	got := handledIssue
	mu.Unlock()

	if got == nil {
		t.Fatal("handler was never called")
	}
	if got.Number != 42 {
		t.Errorf("handler got issue %d, want 42", got.Number)
	}
}

// TestPoller_ParallelDispatch_UsesFreshIssueNotListSnapshot verifies GH-105:
// the pre-dispatch GetIssue refresh must not be discarded. The list snapshot
// (fetchCandidates) carries a stale body while the single-issue GET
// (pre-dispatch refresh) returns an updated body; the handler must observe
// the fresh body, not the stale list snapshot.
func TestPoller_ParallelDispatch_UsesFreshIssueNotListSnapshot(t *testing.T) {
	pilot := Label{Name: "pilot"}
	staleIssue := &Issue{
		Number:    42,
		Title:     "Fix the thing",
		Body:      "stale list-snapshot body",
		State:     "open",
		Labels:    []Label{pilot},
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}
	freshIssue := &Issue{
		Number:    42,
		Title:     "Fix the thing",
		Body:      "fresh body from pre-dispatch refresh",
		State:     "open",
		Labels:    []Label{pilot},
		CreatedAt: staleIssue.CreatedAt,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues/42"):
			// Pre-dispatch GetIssue — must return the fresh object.
			_, _ = w.Write(mustJSON(freshIssue))
		default:
			// ListIssues (fetchCandidates) — returns the stale snapshot.
			_, _ = w.Write(mustJSON([]*Issue{staleIssue}))
		}
	}))
	defer server.Close()

	var handled sync.WaitGroup
	handled.Add(1)
	var handledIssue *Issue
	var mu sync.Mutex

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, iss *Issue) error {
			mu.Lock()
			handledIssue = iss
			mu.Unlock()
			handled.Done()
			return nil
		}),
		WithRetryGracePeriod(0),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	mu.Lock()
	got := handledIssue
	mu.Unlock()

	if got == nil {
		t.Fatal("handler was never called")
	}
	if got.Body != freshIssue.Body {
		t.Errorf("handler got body %q, want fresh body %q (stale snapshot leaked through dispatch)", got.Body, freshIssue.Body)
	}
}

// TestPoller_ParallelDispatch_FailedRefresh_FallsBackToSnapshot verifies GH-105's
// fail-open requirement: when the pre-dispatch GetIssue fails, dispatch proceeds
// with the list-snapshot object unchanged.
func TestPoller_ParallelDispatch_FailedRefresh_FallsBackToSnapshot(t *testing.T) {
	pilot := Label{Name: "pilot"}
	snapshotIssue := &Issue{
		Number:    42,
		Title:     "Fix the thing",
		Body:      "snapshot body used on fail-open",
		State:     "open",
		Labels:    []Label{pilot},
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues/42"):
			// Pre-dispatch GetIssue fails — dispatch must fall back to snapshot.
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(mustJSON([]*Issue{snapshotIssue}))
		}
	}))
	defer server.Close()

	var handled sync.WaitGroup
	handled.Add(1)
	var handledIssue *Issue
	var mu sync.Mutex

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithOnIssue(func(ctx context.Context, iss *Issue) error {
			mu.Lock()
			handledIssue = iss
			mu.Unlock()
			handled.Done()
			return nil
		}),
		WithRetryGracePeriod(0),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	mu.Lock()
	got := handledIssue
	mu.Unlock()

	if got == nil {
		t.Fatal("handler was never called")
	}
	if got.Body != snapshotIssue.Body {
		t.Errorf("handler got body %q, want snapshot body %q (fail-open fallback broken)", got.Body, snapshotIssue.Body)
	}
}

func TestPoller_NewPoller_InvalidRepo(t *testing.T) {
	_, err := NewPoller(nil, "badrepo", "pilot", 30*time.Second)
	if err == nil {
		t.Fatal("expected error for invalid repo format, got nil")
	}
}

func TestPoller_IsProcessed_TracksMark(t *testing.T) {
	p := &Poller{
		processed: make(map[int]time.Time),
		logger:    nil,
	}
	if p.IsProcessed(1) {
		t.Error("issue 1 should not be processed yet")
	}
	p.processed[1] = time.Now()
	if !p.IsProcessed(1) {
		t.Error("issue 1 should be processed after mark")
	}
	p.Reset()
	if p.IsProcessed(1) {
		t.Error("issue 1 should not be processed after reset")
	}
}

func TestPoller_ParseDependencies(t *testing.T) {
	tests := []struct {
		body string
		want []int
	}{
		{"", nil},
		{"Depends on: #123", []int{123}},
		{"Blocked by #456\nDepends on: #789", []int{456, 789}},
		{"Requires: #42", []int{42}},
		{"No dependencies", nil},
	}
	for _, tt := range tests {
		got := ParseDependencies(tt.body)
		if len(got) != len(tt.want) {
			t.Errorf("ParseDependencies(%q) = %v, want %v", tt.body, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("ParseDependencies(%q)[%d] = %d, want %d", tt.body, i, got[i], tt.want[i])
			}
		}
	}
}

func TestPoller_ExtractPRNumber(t *testing.T) {
	tests := []struct {
		url     string
		want    int
		wantErr bool
	}{
		{"https://github.com/owner/repo/pull/123", 123, false},
		{"https://github.com/owner/repo/pulls/456", 456, false},
		{"", 0, true},
		{"https://github.com/owner/repo/issues/42", 0, true},
	}
	for _, tt := range tests {
		got, err := ExtractPRNumber(tt.url)
		if (err != nil) != tt.wantErr {
			t.Errorf("ExtractPRNumber(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ExtractPRNumber(%q) = %d, want %d", tt.url, got, tt.want)
		}
	}
}
