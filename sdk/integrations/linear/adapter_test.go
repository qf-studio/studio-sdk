package linear

import (
	"context"
	"strings"
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func TestAdapter_Name(t *testing.T) {
	a := New(DefaultConfig())
	if got := a.Name(); got != "linear" {
		t.Errorf("Name() = %q, want %q", got, "linear")
	}
}

func TestAdapter_WebhookSource(t *testing.T) {
	a := New(DefaultConfig())
	if got := a.WebhookSource(); got != "linear" {
		t.Errorf("WebhookSource() = %q, want %q", got, "linear")
	}
}

func TestAdapter_InterfaceAssertions(t *testing.T) {
	// Compile-time assertions verified by var block in adapter.go.
	// This test confirms New() returns the right type at runtime.
	var _ core.Adapter = New(DefaultConfig())
	var _ core.Pollable = New(DefaultConfig())
	var _ core.WebhookCapable = New(DefaultConfig())
}

func TestToIssueEvent(t *testing.T) {
	issue := &Issue{
		ID:          "uuid-abc-123",
		Identifier:  "APP-42",
		Title:       "Fix the bug",
		Description: "Some description",
		Priority:    2,
		Labels:      []Label{{ID: "lbl-1", Name: "pilot"}},
		Team:        Team{ID: "team-id-1", Key: "APP"},
	}

	ev := toIssueEvent(issue)

	if ev.Action != "created" {
		t.Errorf("Action = %q, want %q", ev.Action, "created")
	}
	if ev.IssueID != "uuid-abc-123" {
		t.Errorf("IssueID = %q, want %q", ev.IssueID, "uuid-abc-123")
	}
	if ev.SequenceID != "LIN-APP-42" {
		t.Errorf("SequenceID = %q, want %q", ev.SequenceID, "LIN-APP-42")
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
	if len(ev.Labels) != 1 || ev.Labels[0] != "pilot" {
		t.Errorf("Labels = %v, want [pilot]", ev.Labels)
	}
}

func TestToIssueEvent_SequenceID_Prefix(t *testing.T) {
	issue := &Issue{
		ID:         "uuid-1",
		Identifier: "TST-7",
		Team:       Team{ID: "tid"},
	}

	ev := toIssueEvent(issue)

	if !strings.HasPrefix(ev.SequenceID, "LIN-") {
		t.Errorf("SequenceID %q does not have LIN- prefix", ev.SequenceID)
	}
	if ev.SequenceID != "LIN-TST-7" {
		t.Errorf("SequenceID = %q, want %q", ev.SequenceID, "LIN-TST-7")
	}
}

func TestToIssueEvent_PriorityNormalization(t *testing.T) {
	tests := []struct {
		linearPriority int
		want           string
	}{
		{0, core.PriorityNone},
		{1, core.PriorityUrgent},
		{2, core.PriorityHigh},
		{3, core.PriorityMedium},
		{4, core.PriorityLow},
		{99, core.PriorityNone},
	}

	for _, tt := range tests {
		issue := &Issue{ID: "id", Identifier: "TST-1", Priority: tt.linearPriority}
		ev := toIssueEvent(issue)
		if ev.Priority != tt.want {
			t.Errorf("priority %d: got %q, want %q", tt.linearPriority, ev.Priority, tt.want)
		}
	}
}

// TestAdapter_NewPoller_NoWorkspaces verifies the adapter returns a no-op poller
// when no workspaces are configured.
func TestAdapter_NewPoller_NoWorkspaces(t *testing.T) {
	cfg := &Config{Enabled: false}
	a := New(cfg)

	deps := core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(ctx context.Context, ev core.IssueEvent) (*core.IssueResult, error) {
			return nil, nil
		}),
	}

	poller := a.NewPoller(deps)
	if poller == nil {
		t.Fatal("NewPoller returned nil")
	}

	// nopPoller should start without error
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := poller.Start(ctx); err != nil {
		t.Errorf("nopPoller.Start() error = %v", err)
	}
}

// TestAdapter_NewPoller_WithWorkspace verifies the adapter creates a real poller
// for a configured workspace.
func TestAdapter_NewPoller_WithWorkspace(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Workspaces: []*WorkspaceConfig{
			{
				Name:         "test",
				APIKey:       testutil.FakeLinearToken,
				TeamID:       "TEAM1",
				TriggerLabel: "pilot",
			},
		},
	}
	a := New(cfg)

	deps := core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(ctx context.Context, ev core.IssueEvent) (*core.IssueResult, error) {
			return nil, nil
		}),
	}

	poller := a.NewPoller(deps)
	if poller == nil {
		t.Fatal("NewPoller returned nil")
	}

	// Verify it's a real *Poller (not nopPoller)
	if _, ok := poller.(*Poller); !ok {
		t.Errorf("NewPoller returned %T, want *Poller", poller)
	}
}

// TestPoller_SanitizeCalledInLivePath is the ASCII-smuggling guard.
// Invisible Unicode injected into issue Title/Description must be stripped
// before reaching the IssueHandler callback.
func TestPoller_SanitizeCalledInLivePath(t *testing.T) {
	// U+200B ZERO WIDTH SPACE — invisible, commonly used in prompt-injection attacks
	zwsp := "​"
	dirtyTitle := "Fix the" + zwsp + " bug"
	dirtyDesc := "Do the" + zwsp + " thing"

	var capturedTitle, capturedDesc string

	config := &WorkspaceConfig{
		TeamID:       "TEST",
		TriggerLabel: "pilot",
	}

	poller := NewPoller(nil, config, 30,
		WithOnLinearIssue(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			capturedTitle = issue.Title
			capturedDesc = issue.Description
			return &IssueResult{Success: true}, nil
		}),
	)

	issue := &Issue{
		ID:          "issue-sanitize",
		Identifier:  "TST-99",
		Title:       dirtyTitle,
		Description: dirtyDesc,
	}

	ctx := context.Background()
	poller.semaphore <- struct{}{}
	poller.activeWg.Add(1)
	go poller.processIssueAsync(ctx, issue)
	poller.activeWg.Wait()

	if strings.Contains(capturedTitle, zwsp) {
		t.Errorf("Title still contains invisible Unicode after sanitize: %q", capturedTitle)
	}
	if strings.Contains(capturedDesc, zwsp) {
		t.Errorf("Description still contains invisible Unicode after sanitize: %q", capturedDesc)
	}
	if capturedTitle == "" {
		t.Error("capturedTitle is empty — handler was not called")
	}
}
