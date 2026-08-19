package jira

import (
	"context"
	"strings"
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func TestAdapter_Name(t *testing.T) {
	a := New(DefaultConfig())
	if got := a.Name(); got != "jira" {
		t.Errorf("Name() = %q, want %q", got, "jira")
	}
}

func TestAdapter_WebhookSource(t *testing.T) {
	a := New(DefaultConfig())
	if got := a.WebhookSource(); got != "jira" {
		t.Errorf("WebhookSource() = %q, want %q", got, "jira")
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
		BaseURL:      "https://example.atlassian.net",
		Username:     testutil.FakeJiraUsername,
		APIToken:     testutil.FakeJiraAPIToken,
		Platform:     PlatformCloud,
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
		BaseURL:      "https://example.atlassian.net",
		Username:     testutil.FakeJiraUsername,
		APIToken:     testutil.FakeJiraAPIToken,
		Platform:     PlatformCloud,
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

	p.onPRCreated(core.PRCreatedEvent{PRNumber: 7, PRURL: "https://example.com/pr/7", IssueID: "10001"})

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
		ID:  "10001",
		Key: "PROJ-42",
		Fields: Fields{
			Summary:     "Fix the bug",
			Description: "Some description",
			Priority:    &JiraPriority{Name: "High"},
			Labels:      []string{"pilot"},
			Project:     Project{Key: "PROJ"},
		},
	}

	ev := toIssueEvent(issue)

	if ev.Action != "created" {
		t.Errorf("Action = %q, want %q", ev.Action, "created")
	}
	if ev.IssueID != "10001" {
		t.Errorf("IssueID = %q, want %q", ev.IssueID, "10001")
	}
	if ev.SequenceID != "JIRA-PROJ-42" {
		t.Errorf("SequenceID = %q, want %q", ev.SequenceID, "JIRA-PROJ-42")
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
	if ev.ProjectID != "PROJ" {
		t.Errorf("ProjectID = %q, want %q", ev.ProjectID, "PROJ")
	}
	if len(ev.Labels) != 1 || ev.Labels[0] != "pilot" {
		t.Errorf("Labels = %v, want [pilot]", ev.Labels)
	}
}

func TestToIssueEvent_SequenceID_Prefix(t *testing.T) {
	issue := &Issue{
		ID:  "10002",
		Key: "TST-7",
		Fields: Fields{
			Project: Project{Key: "TST"},
		},
	}

	ev := toIssueEvent(issue)

	if !strings.HasPrefix(ev.SequenceID, "JIRA-") {
		t.Errorf("SequenceID %q does not have JIRA- prefix", ev.SequenceID)
	}
	if ev.SequenceID != "JIRA-TST-7" {
		t.Errorf("SequenceID = %q, want %q", ev.SequenceID, "JIRA-TST-7")
	}
}

func TestToIssueEvent_PriorityNormalization(t *testing.T) {
	tests := []struct {
		jiraPriority string
		want         string
	}{
		{"Highest", core.PriorityUrgent},
		{"Blocker", core.PriorityUrgent},
		{"High", core.PriorityHigh},
		{"Medium", core.PriorityMedium},
		{"Low", core.PriorityLow},
		{"Lowest", core.PriorityNone},
		{"", core.PriorityNone},
	}

	for _, tt := range tests {
		var prio *JiraPriority
		if tt.jiraPriority != "" {
			prio = &JiraPriority{Name: tt.jiraPriority}
		}
		issue := &Issue{
			ID:  "id",
			Key: "TST-1",
			Fields: Fields{
				Priority: prio,
			},
		}
		ev := toIssueEvent(issue)
		if ev.Priority != tt.want {
			t.Errorf("priority %q: got %q, want %q", tt.jiraPriority, ev.Priority, tt.want)
		}
	}
}

func TestToIssueEvent_NilPriority(t *testing.T) {
	issue := &Issue{
		ID:  "10003",
		Key: "TST-5",
		Fields: Fields{
			Priority: nil,
		},
	}

	ev := toIssueEvent(issue)
	if ev.Priority != core.PriorityNone {
		t.Errorf("nil priority: got %q, want %q", ev.Priority, core.PriorityNone)
	}
}

func TestToIssueEvent_NilLabels(t *testing.T) {
	issue := &Issue{
		ID:  "10004",
		Key: "TST-6",
		Fields: Fields{
			Labels: nil,
		},
	}

	ev := toIssueEvent(issue)
	if ev.Labels == nil {
		t.Error("Labels should not be nil")
	}
	if len(ev.Labels) != 0 {
		t.Errorf("Labels = %v, want empty slice", ev.Labels)
	}
}
