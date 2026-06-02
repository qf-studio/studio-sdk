package gitlab

import (
	"context"
	"testing"

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
