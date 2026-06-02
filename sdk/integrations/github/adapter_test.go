package github

import (
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

func TestAdapter_InterfaceAssertions(t *testing.T) {
	// Compile-time assertions are in adapter.go; this test confirms runtime behavior.
	var _ core.Adapter = (*Adapter)(nil)
	var _ core.Pollable = (*Adapter)(nil)
	var _ core.WebhookCapable = (*Adapter)(nil)
}

func TestAdapter_Name(t *testing.T) {
	a := New(DefaultConfig())
	if got := a.Name(); got != "github" {
		t.Errorf("Name() = %q, want %q", got, "github")
	}
}

func TestAdapter_WebhookSource(t *testing.T) {
	a := New(DefaultConfig())
	if got := a.WebhookSource(); got != "github" {
		t.Errorf("WebhookSource() = %q, want %q", got, "github")
	}
}

func TestAdapter_New_ReturnsNonNil(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Token = "test-token"
	cfg.Repo = "owner/repo"

	a := New(cfg)
	if a == nil {
		t.Fatal("New() returned nil")
	}
}

func TestToIssueEvent(t *testing.T) {
	issue := &Issue{
		Number: 42,
		Title:  "Fix bug",
		Body:   "Details",
		Labels: []Label{{Name: "pilot"}, {Name: "priority:high"}},
	}

	ev := toIssueEvent(issue, "owner/myrepo")

	if ev.IssueID != "42" {
		t.Errorf("IssueID = %q, want %q", ev.IssueID, "42")
	}
	if ev.SequenceID != "GH-42" {
		t.Errorf("SequenceID = %q, want %q", ev.SequenceID, "GH-42")
	}
	if ev.Title != "Fix bug" {
		t.Errorf("Title = %q, want %q", ev.Title, "Fix bug")
	}
	if ev.ProjectID != "myrepo" {
		t.Errorf("ProjectID = %q, want %q", ev.ProjectID, "myrepo")
	}
	if ev.Action != "created" {
		t.Errorf("Action = %q, want %q", ev.Action, "created")
	}
	if ev.Priority != "high" {
		t.Errorf("Priority = %q, want %q", ev.Priority, "high")
	}
}
