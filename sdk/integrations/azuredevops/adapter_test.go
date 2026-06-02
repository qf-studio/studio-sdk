package azuredevops

import (
	"context"
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func TestAdapter_Name(t *testing.T) {
	a := New(DefaultConfig())
	if got := a.Name(); got != "azuredevops" {
		t.Errorf("Name() = %q, want %q", got, "azuredevops")
	}
}

func TestAdapter_WebhookSource(t *testing.T) {
	a := New(DefaultConfig())
	if got := a.WebhookSource(); got != "azuredevops" {
		t.Errorf("WebhookSource() = %q, want %q", got, "azuredevops")
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
	cfg.PAT = testutil.FakeAzureDevOpsPAT
	cfg.Organization = "myorg"
	cfg.Project = "myproject"

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
	wi := &WorkItem{
		ID: 42,
		Fields: map[string]interface{}{
			"System.Title":        "Fix the bug",
			"System.Description":  "Details here",
			"System.Tags":         "pilot; backend",
			"System.WorkItemType": "Task",
		},
	}

	ev := toIssueEvent(wi)

	if ev.Action != "created" {
		t.Errorf("Action = %q, want %q", ev.Action, "created")
	}
	if ev.IssueID != "42" {
		t.Errorf("IssueID = %q, want %q", ev.IssueID, "42")
	}
	if ev.SequenceID != "AZDO-42" {
		t.Errorf("SequenceID = %q, want %q", ev.SequenceID, "AZDO-42")
	}
	if ev.Title != "Fix the bug" {
		t.Errorf("Title = %q, want %q", ev.Title, "Fix the bug")
	}
	if ev.Body != "Details here" {
		t.Errorf("Body = %q, want %q", ev.Body, "Details here")
	}
	if ev.ProjectID != "Task" {
		t.Errorf("ProjectID = %q, want %q", ev.ProjectID, "Task")
	}
	if len(ev.Labels) != 2 {
		t.Errorf("len(Labels) = %d, want 2", len(ev.Labels))
	}
	// No priority field → normalized "none".
	if ev.Priority != core.PriorityNone {
		t.Errorf("Priority = %q, want %q", ev.Priority, core.PriorityNone)
	}
}

func TestToIssueEvent_Priority(t *testing.T) {
	wi := &WorkItem{
		ID: 9,
		Fields: map[string]interface{}{
			"System.Title":                   "High pri",
			"Microsoft.VSTS.Common.Priority": float64(2), // Azure 2 → High
		},
	}

	ev := toIssueEvent(wi)

	if ev.Priority != core.PriorityHigh {
		t.Errorf("Priority = %q, want %q", ev.Priority, core.PriorityHigh)
	}
}
