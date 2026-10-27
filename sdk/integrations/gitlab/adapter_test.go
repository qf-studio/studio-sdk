package gitlab

import (
	"context"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

func TestAdapter_Name(t *testing.T) {
	a := New(DefaultConfig())
	if got := a.Name(); got != "gitlab" {
		t.Errorf("Name() = %q, want %q", got, "gitlab")
	}
}

func TestAdapter_WebhookSource(t *testing.T) {
	a := New(DefaultConfig())
	if got := a.WebhookSource(); got != "gitlab" {
		t.Errorf("WebhookSource() = %q, want %q", got, "gitlab")
	}
}

func TestAdapter_New(t *testing.T) {
	cfg := DefaultConfig()
	a := New(cfg)
	if a == nil {
		t.Fatal("New returned nil")
	}
	if a.config != cfg {
		t.Error("adapter config not set correctly")
	}
}

func TestAdapter_InterfaceAssertions(t *testing.T) {
	// These mirror the compile-time var _ assertions in adapter.go.
	// If they fail the package won't compile at all, but we verify at runtime too.
	a := New(DefaultConfig())

	if _, ok := interface{}(a).(core.Adapter); !ok {
		t.Error("*Adapter does not implement core.Adapter")
	}
	if _, ok := interface{}(a).(core.Pollable); !ok {
		t.Error("*Adapter does not implement core.Pollable")
	}
	if _, ok := interface{}(a).(core.WebhookCapable); !ok {
		t.Error("*Adapter does not implement core.WebhookCapable")
	}
}

func TestAdapter_NewPoller_ReturnsPoller(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Token = "fake-token"
	cfg.Project = "namespace/project"

	a := New(cfg)
	deps := core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(ctx context.Context, ev core.IssueEvent) (*core.IssueResult, error) {
			return &core.IssueResult{Success: true}, nil
		}),
	}

	p := a.NewPoller(deps)
	if p == nil {
		t.Fatal("NewPoller returned nil")
	}
}

func TestAdapter_NewPoller_BridgesOnPRCreated(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Token = "fake-token"
	cfg.Project = "namespace/project"

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
	if p.OnMRCreated == nil {
		t.Fatal("expected OnMRCreated to be bridged from PollerDeps.OnPRCreated")
	}

	p.OnMRCreated(7, "https://example.com/mr/7", 10001, "abc123", "pilot/branch")

	if called != 1 {
		t.Errorf("OnPRCreated called %d times, want 1", called)
	}
	if capturedEvent.PRNumber != 7 {
		t.Errorf("PRNumber = %d, want 7", capturedEvent.PRNumber)
	}
	if capturedEvent.IssueID != "10001" {
		t.Errorf("IssueID = %q, want %q", capturedEvent.IssueID, "10001")
	}
}

func TestToIssueEvent(t *testing.T) {
	issue := &Issue{
		IID:         42,
		ProjectID:   7,
		Title:       "Fix the bug",
		Description: "Details here",
		Labels:      []string{"pilot", "backend"},
	}

	ev := toIssueEvent(issue)

	if ev.Action != "created" {
		t.Errorf("Action = %q, want %q", ev.Action, "created")
	}
	if ev.IssueID != "42" {
		t.Errorf("IssueID = %q, want %q", ev.IssueID, "42")
	}
	if ev.SequenceID != "GL-42" {
		t.Errorf("SequenceID = %q, want %q", ev.SequenceID, "GL-42")
	}
	if ev.Title != "Fix the bug" {
		t.Errorf("Title = %q, want %q", ev.Title, "Fix the bug")
	}
	if ev.Body != "Details here" {
		t.Errorf("Body = %q, want %q", ev.Body, "Details here")
	}
	if ev.ProjectID != "7" {
		t.Errorf("ProjectID = %q, want %q", ev.ProjectID, "7")
	}
	if len(ev.Labels) != 2 {
		t.Errorf("len(Labels) = %d, want 2", len(ev.Labels))
	}
	// No priority label → normalized "none".
	if ev.Priority != core.PriorityNone {
		t.Errorf("Priority = %q, want %q", ev.Priority, core.PriorityNone)
	}
}

func TestToIssueEvent_PriorityFromLabel(t *testing.T) {
	issue := &Issue{
		IID:    7,
		Title:  "Urgent thing",
		Labels: []string{"pilot", "priority::high"},
	}

	ev := toIssueEvent(issue)

	if ev.Priority != core.PriorityHigh {
		t.Errorf("Priority = %q, want %q", ev.Priority, core.PriorityHigh)
	}
}

func TestNewPollerSequentialSettings_Defaults(t *testing.T) {
	a := New(&Config{Token: "t", Project: "g/p"})
	p := a.NewPoller(core.PollerDeps{}).(*Poller)
	if !p.waitForMerge || p.mrPollInterval != 30*time.Second || p.mrTimeout != time.Hour {
		t.Errorf("got %v/%v/%v, want true/30s/1h", p.waitForMerge, p.mrPollInterval, p.mrTimeout)
	}
}

func TestNewPollerSequentialSettings_AppliedToPoller(t *testing.T) {
	no := false
	a := New(&Config{Token: "t", Project: "g/p"})
	p := a.NewPoller(core.PollerDeps{
		WaitForMerge:   &no,
		PRPollInterval: 15 * time.Second,
		PRTimeout:      10 * time.Minute,
	}).(*Poller)
	if p.waitForMerge || p.mrPollInterval != 15*time.Second || p.mrTimeout != 10*time.Minute {
		t.Errorf("got %v/%v/%v, want false/15s/10m", p.waitForMerge, p.mrPollInterval, p.mrTimeout)
	}
}

func TestNewPollerSequentialSettings_ZeroKeepsDefault(t *testing.T) {
	no := false
	tests := []struct {
		name     string
		deps     core.PollerDeps
		wantWait bool
		wantIvl  time.Duration
		wantTO   time.Duration
	}{
		{"zero timeout with wait set", core.PollerDeps{WaitForMerge: &no, PRPollInterval: 15 * time.Second}, false, 15 * time.Second, time.Hour},
		{"zero interval with timeout set", core.PollerDeps{PRTimeout: 10 * time.Minute}, true, 30 * time.Second, 10 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := New(&Config{Token: "t", Project: "g/p"})
			p := a.NewPoller(tt.deps).(*Poller)
			if p.waitForMerge != tt.wantWait || p.mrPollInterval != tt.wantIvl || p.mrTimeout != tt.wantTO {
				t.Errorf("got %v/%v/%v, want %v/%v/%v", p.waitForMerge, p.mrPollInterval, p.mrTimeout, tt.wantWait, tt.wantIvl, tt.wantTO)
			}
		})
	}
}
