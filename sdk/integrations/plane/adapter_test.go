package plane

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// TestAdapter_NewPoller_BridgesOnPRCreated verifies PollerDeps.OnPRCreated is
// bridged into the Plane-specific Poller's onPRCreated callback.
func TestAdapter_NewPoller_BridgesOnPRCreated(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.APIKey = "fake-api-key"
	cfg.WorkspaceSlug = "my-workspace"

	a := New(cfg)

	var capturedEvent core.PRCreatedEvent
	var called int

	deps := core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(_ context.Context, _ core.IssueEvent) (*core.IssueResult, error) {
			return nil, nil
		}),
		OnPRCreated: func(ev core.PRCreatedEvent) {
			called++
			capturedEvent = ev
		},
	}

	poller := a.NewPoller(deps)
	p, ok := poller.(*Poller)
	if !ok {
		t.Fatalf("NewPoller returned %T, want *Poller", poller)
	}
	if p.onPRCreated == nil {
		t.Fatal("expected onPRCreated to be bridged from PollerDeps.OnPRCreated")
	}

	p.onPRCreated(core.PRCreatedEvent{PRNumber: 7, PRURL: "https://example.com/pr/7", IssueID: "work-item-10001"})

	if called != 1 {
		t.Errorf("OnPRCreated called %d times, want 1", called)
	}
	if capturedEvent.PRNumber != 7 {
		t.Errorf("PRNumber = %d, want 7", capturedEvent.PRNumber)
	}
	if capturedEvent.IssueID != "work-item-10001" {
		t.Errorf("IssueID = %q, want %q", capturedEvent.IssueID, "work-item-10001")
	}
}

// mutation pin: Adapter.NewPoller wires PollerDeps.Logger into the client, so its Warn goes to the host logger and not slog.Default() -> this test fails
func TestAdapter_NewPoller_WiresLoggerIntoClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"wi-1","name":42}]}`))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.BaseURL = srv.URL
	cfg.APIKey = "fake-api-key"
	cfg.WorkspaceSlug = "my-workspace"

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	poller := New(cfg).NewPoller(core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(_ context.Context, _ core.IssueEvent) (*core.IssueResult, error) {
			return nil, nil
		}),
		Logger: logger,
	})
	p, ok := poller.(*Poller)
	if !ok {
		t.Fatalf("NewPoller returned %T, want *Poller", poller)
	}

	if _, err := p.client.ListWorkItems(context.Background(), "my-workspace", "proj-1", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "work_item_id=wi-1") {
		t.Errorf("client Warn did not reach the host logger; log:\n%s", buf.String())
	}
}
