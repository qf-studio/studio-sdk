package github

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// fakeProcessedStore is a minimal in-memory core.ProcessedStore for
// exercising the write-through path from MarkProcessed.
type fakeProcessedStore struct {
	mu    sync.Mutex
	calls []struct{ source, repo, issueID string }
}

func (f *fakeProcessedStore) Mark(source, repo, issueID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, struct{ source, repo, issueID string }{source, repo, issueID})
	return nil
}

func (f *fakeProcessedStore) Unmark(source, repo, issueID string) error { return nil }

func (f *fakeProcessedStore) IsProcessed(source, repo, issueID string) (bool, error) {
	return false, nil
}

func (f *fakeProcessedStore) Load(source, repo string) (map[string]time.Time, error) {
	return nil, nil
}

func TestPoller_MarkProcessed_TracksMark(t *testing.T) {
	p := &Poller{processed: make(map[int]time.Time)}

	if p.IsProcessed(1) {
		t.Fatal("issue 1 should not be processed yet")
	}

	p.MarkProcessed(1)

	if !p.IsProcessed(1) {
		t.Error("issue 1 should be processed after MarkProcessed")
	}
}

func TestPoller_MarkProcessed_WritesThroughToStore(t *testing.T) {
	tests := []struct {
		name   string
		owner  string
		repo   string
		number int
	}{
		{name: "basic repo key", owner: "owner", repo: "repo", number: 42},
		{name: "different repo key", owner: "acme", repo: "widgets", number: 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeProcessedStore{}
			p := &Poller{
				processed:      make(map[int]time.Time),
				owner:          tt.owner,
				repo:           tt.repo,
				processedStore: store,
			}

			p.MarkProcessed(tt.number)

			store.mu.Lock()
			defer store.mu.Unlock()
			if len(store.calls) != 1 {
				t.Fatalf("expected exactly 1 Mark call, got %d", len(store.calls))
			}
			call := store.calls[0]
			if call.source != "github" {
				t.Errorf("source = %q, want %q", call.source, "github")
			}
			if call.repo != p.repoKey() {
				t.Errorf("repo = %q, want %q", call.repo, p.repoKey())
			}
			if call.issueID != strconv.Itoa(tt.number) {
				t.Errorf("issueID = %q, want %q", call.issueID, strconv.Itoa(tt.number))
			}
		})
	}
}

func TestPoller_MarkProcessed_SkipsDispatchInPollCycle(t *testing.T) {
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

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, ts.server.URL)
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	poller.MarkProcessed(42)

	found, err := poller.findOldestUnprocessedIssue(context.Background())
	if err != nil {
		t.Fatalf("findOldestUnprocessedIssue: %v", err)
	}
	if found != nil {
		t.Errorf("expected no unprocessed issue after MarkProcessed, got issue %d", found.Number)
	}
}

func TestPoller_MarkProcessed_ClearProcessedRoundTrip(t *testing.T) {
	p := &Poller{processed: make(map[int]time.Time), logger: slog.Default()}

	p.MarkProcessed(1)
	if !p.IsProcessed(1) {
		t.Fatal("issue 1 should be processed after MarkProcessed")
	}

	p.ClearProcessed(1)
	if p.IsProcessed(1) {
		t.Error("issue 1 should not be processed after ClearProcessed")
	}
}
