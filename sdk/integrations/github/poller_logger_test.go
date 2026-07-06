package github

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// capturingHandler is a minimal slog.Handler that records every record it
// receives, so tests can assert log lines were routed to the injected
// logger instead of slog.Default().
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *capturingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *capturingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.records)
}

func TestPoller_InjectedLogger_ReceivesPollLines(t *testing.T) {
	ts := newPollerTestServer(hookTestIssue())
	defer ts.close()

	handler := &capturingHandler{}
	logger := slog.New(handler)

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, ts.server.URL)
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second,
		WithPollerLogger(logger),
		WithOnIssue(func(ctx context.Context, iss *Issue) error { return nil }),
	)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}

	poller.checkForNewIssues(context.Background())
	poller.WaitForActive()

	if handler.count() == 0 {
		t.Fatal("expected poller log lines to reach the injected logger")
	}
}

func TestPoller_NilLogger_FallsBackToDefault(t *testing.T) {
	client := NewClientWithBaseURL(testutil.FakeGitHubToken, "http://example.invalid")
	poller, err := NewPoller(client, "owner/repo", "pilot", 30*time.Second)
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}
	if poller.logger != slog.Default() {
		t.Error("poller.logger should default to slog.Default() when no logger is injected")
	}
}

func TestAdapter_NewPoller_BridgesLogger(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Token = testutil.FakeGitHubToken
	cfg.Repo = "owner/repo"

	handler := &capturingHandler{}
	logger := slog.New(handler)

	a := New(cfg)
	deps := core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(ctx context.Context, ev core.IssueEvent) (*core.IssueResult, error) {
			return nil, nil
		}),
		Logger: logger,
	}

	p, ok := a.NewPoller(deps).(*Poller)
	if !ok {
		t.Fatal("NewPoller did not return *Poller")
	}
	if p.logger != logger {
		t.Error("Logger not bridged from PollerDeps to Poller")
	}
}

func TestAdapter_NewPoller_NilLoggerLeavesDefault(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Token = testutil.FakeGitHubToken
	cfg.Repo = "owner/repo"

	a := New(cfg)
	deps := core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(ctx context.Context, ev core.IssueEvent) (*core.IssueResult, error) {
			return nil, nil
		}),
	}

	p, ok := a.NewPoller(deps).(*Poller)
	if !ok {
		t.Fatal("NewPoller did not return *Poller")
	}
	if p.logger != slog.Default() {
		t.Error("nil Logger in PollerDeps should leave the poller's default logger untouched")
	}
}
