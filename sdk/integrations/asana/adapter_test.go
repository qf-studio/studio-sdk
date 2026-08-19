package asana

import (
	"context"
	"strings"
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func TestAdapter_Name(t *testing.T) {
	a := New(DefaultConfig())
	if got := a.Name(); got != "asana" {
		t.Errorf("Name() = %q, want %q", got, "asana")
	}
}

func TestAdapter_WebhookSource(t *testing.T) {
	a := New(DefaultConfig())
	if got := a.WebhookSource(); got != "asana" {
		t.Errorf("WebhookSource() = %q, want %q", got, "asana")
	}
}

func TestAdapter_InterfaceAssertions(t *testing.T) {
	// Compile-time assertions verified by var block in adapter.go.
	var _ core.Adapter = New(DefaultConfig())
	var _ core.Pollable = New(DefaultConfig())
	var _ core.WebhookCapable = New(DefaultConfig())
}

func TestAdapter_NewPoller_Disabled(t *testing.T) {
	cfg := &Config{Enabled: false}
	a := New(cfg)

	deps := core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(_ context.Context, _ core.IssueEvent) (*core.IssueResult, error) {
			return nil, nil
		}),
	}

	poller := a.NewPoller(deps)
	if poller == nil {
		t.Fatal("NewPoller returned nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := poller.Start(ctx); err != nil {
		t.Errorf("nopPoller.Start() error = %v", err)
	}
}

func TestAdapter_NewPoller_Enabled(t *testing.T) {
	cfg := &Config{
		Enabled:      true,
		AccessToken:  testutil.FakeAsanaToken,
		WorkspaceID:  testutil.FakeAsanaWorkspaceID,
		TriggerLabel: "pilot",
		Polling: &PollingConfig{
			Enabled: true,
		},
	}
	a := New(cfg)

	deps := core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(_ context.Context, _ core.IssueEvent) (*core.IssueResult, error) {
			return nil, nil
		}),
	}

	poller := a.NewPoller(deps)
	if poller == nil {
		t.Fatal("NewPoller returned nil")
	}

	if _, ok := poller.(*Poller); !ok {
		t.Errorf("NewPoller returned %T, want *Poller", poller)
	}
}

func TestAdapter_NewPoller_BridgesOnPRCreated(t *testing.T) {
	cfg := &Config{
		Enabled:      true,
		AccessToken:  testutil.FakeAsanaToken,
		WorkspaceID:  testutil.FakeAsanaWorkspaceID,
		TriggerLabel: "pilot",
	}
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

	p.onPRCreated(core.PRCreatedEvent{PRNumber: 7, PRURL: "https://example.com/pr/7", IssueID: "gid-1"})

	if called != 1 {
		t.Errorf("OnPRCreated called %d times, want 1", called)
	}
	if capturedEvent.PRNumber != 7 {
		t.Errorf("PRNumber = %d, want 7", capturedEvent.PRNumber)
	}
	if capturedEvent.IssueID != "gid-1" {
		t.Errorf("IssueID = %q, want %q", capturedEvent.IssueID, "gid-1")
	}
}

func TestToIssueEvent(t *testing.T) {
	task := &Task{
		GID:   "1234567890",
		Name:  "Fix the bug",
		Notes: "Some description",
		Tags:  []Tag{{Name: "pilot"}, {Name: "high"}},
		Projects: []Project{
			{GID: "proj-111", Name: "My Project"},
		},
	}

	ev := toIssueEvent(task)

	if ev.Action != "created" {
		t.Errorf("Action = %q, want %q", ev.Action, "created")
	}
	if ev.IssueID != "1234567890" {
		t.Errorf("IssueID = %q, want %q", ev.IssueID, "1234567890")
	}
	if ev.SequenceID != "ASANA-1234567890" {
		t.Errorf("SequenceID = %q, want %q", ev.SequenceID, "ASANA-1234567890")
	}
	if ev.Title != "Fix the bug" {
		t.Errorf("Title = %q, want %q", ev.Title, "Fix the bug")
	}
	if ev.Body != "Some description" {
		t.Errorf("Body = %q, want %q", ev.Body, "Some description")
	}
	if ev.Priority != core.PriorityHigh {
		t.Errorf("Priority = %q, want %q", ev.Priority, core.PriorityHigh)
	}
	if ev.ProjectID != "proj-111" {
		t.Errorf("ProjectID = %q, want %q", ev.ProjectID, "proj-111")
	}
	if len(ev.Labels) != 2 {
		t.Errorf("Labels len = %d, want 2", len(ev.Labels))
	}
}

func TestToIssueEvent_SequenceID_Prefix(t *testing.T) {
	task := &Task{GID: "9876543210"}

	ev := toIssueEvent(task)

	if !strings.HasPrefix(ev.SequenceID, "ASANA-") {
		t.Errorf("SequenceID %q does not have ASANA- prefix", ev.SequenceID)
	}
	if ev.SequenceID != "ASANA-9876543210" {
		t.Errorf("SequenceID = %q, want %q", ev.SequenceID, "ASANA-9876543210")
	}
}

func TestToIssueEvent_PriorityNormalization(t *testing.T) {
	tests := []struct {
		tagName string
		want    string
	}{
		{"urgent", core.PriorityUrgent},
		{"critical", core.PriorityUrgent},
		{"high", core.PriorityHigh},
		{"medium", core.PriorityMedium},
		{"low", core.PriorityLow},
		{"other", core.PriorityNone},
	}

	for _, tt := range tests {
		task := &Task{
			GID:  "id-1",
			Tags: []Tag{{Name: tt.tagName}},
		}
		ev := toIssueEvent(task)
		if ev.Priority != tt.want {
			t.Errorf("tag %q: Priority = %q, want %q", tt.tagName, ev.Priority, tt.want)
		}
	}
}

func TestToIssueEvent_NoTags(t *testing.T) {
	task := &Task{GID: "id-2", Tags: nil}

	ev := toIssueEvent(task)

	if ev.Priority != core.PriorityNone {
		t.Errorf("no tags: Priority = %q, want %q", ev.Priority, core.PriorityNone)
	}
	if ev.Labels == nil {
		t.Error("Labels should not be nil")
	}
	if len(ev.Labels) != 0 {
		t.Errorf("Labels = %v, want empty", ev.Labels)
	}
}

func TestToIssueEvent_NoProjects(t *testing.T) {
	task := &Task{GID: "id-3", Projects: nil}

	ev := toIssueEvent(task)

	if ev.ProjectID != "" {
		t.Errorf("ProjectID = %q, want empty", ev.ProjectID)
	}
}

// TestToIssueEvent_ASCIISmuggling verifies that sanitized title/body pass through
// as-is — the sanitize step happens in processTaskAsync before toIssueEvent.
func TestToIssueEvent_ASCIISmuggling(t *testing.T) {
	const clean = "clean title"
	task := &Task{
		GID:   "id-4",
		Name:  clean,
		Notes: clean,
	}

	ev := toIssueEvent(task)

	if ev.Title != clean {
		t.Errorf("Title = %q, want %q", ev.Title, clean)
	}
	if ev.Body != clean {
		t.Errorf("Body = %q, want %q", ev.Body, clean)
	}
}
