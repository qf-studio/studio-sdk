package plane

import (
	"context"
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
