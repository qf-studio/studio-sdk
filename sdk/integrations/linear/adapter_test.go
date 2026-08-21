package linear

import (
	"context"
	"strings"
	"testing"
	"time"

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

// TestAdapter_NewPoller_BridgesOnPRCreated verifies PollerDeps.OnPRCreated is
// bridged into the Linear-specific Poller's OnPRCreated callback.
func TestAdapter_NewPoller_BridgesOnPRCreated(t *testing.T) {
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
	if p.OnPRCreated == nil {
		t.Fatal("expected OnPRCreated to be bridged from PollerDeps.OnPRCreated")
	}

	p.OnPRCreated(7, "https://example.com/pr/7", 0, "abc123", "pilot/branch", "issue-node-10001")

	if called != 1 {
		t.Errorf("OnPRCreated called %d times, want 1", called)
	}
	if capturedEvent.PRNumber != 7 {
		t.Errorf("PRNumber = %d, want 7", capturedEvent.PRNumber)
	}
	if capturedEvent.IssueID != "issue-node-10001" {
		t.Errorf("IssueID = %q, want %q", capturedEvent.IssueID, "issue-node-10001")
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

func TestAdapter_NewPoller_AllWorkspaces(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Polling: &PollingConfig{Enabled: true, Interval: 45 * time.Second},
		Workspaces: []*WorkspaceConfig{
			{Name: "one", APIKey: testutil.FakeLinearToken, TeamID: "OMN", TriggerLabel: "pilot"},
			{
				Name: "two", APIKey: testutil.FakeLinearToken, TeamID: "ROU", TriggerLabel: "llm-pilot",
				Polling: &PollingConfig{Enabled: true, Interval: 90 * time.Second},
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
	mp, ok := poller.(*multiPoller)
	if !ok {
		t.Fatalf("NewPoller returned %T, want *multiPoller for 2 workspaces", poller)
	}
	if len(mp.pollers) != 2 {
		t.Fatalf("multiPoller holds %d pollers, want 2", len(mp.pollers))
	}

	first, ok := mp.pollers[0].(*Poller)
	if !ok {
		t.Fatalf("pollers[0] is %T, want *Poller", mp.pollers[0])
	}
	second, ok := mp.pollers[1].(*Poller)
	if !ok {
		t.Fatalf("pollers[1] is %T, want *Poller", mp.pollers[1])
	}

	if first.config.TeamID != "OMN" || second.config.TeamID != "ROU" {
		t.Errorf("poller teams = %q, %q; want OMN, ROU", first.config.TeamID, second.config.TeamID)
	}
	if first.interval != 45*time.Second {
		t.Errorf("first interval = %v, want the global 45s", first.interval)
	}
	if second.interval != 90*time.Second {
		t.Errorf("second interval = %v, want the workspace override 90s", second.interval)
	}
}

func TestMultiPoller_StartRunsAllAndJoinsErrors(t *testing.T) {
	started := make(chan string, 2)
	mp := &multiPoller{pollers: []core.Poller{
		pollerFunc(func(ctx context.Context) error { started <- "a"; return nil }),
		pollerFunc(func(ctx context.Context) error { started <- "b"; return context.DeadlineExceeded }),
	}}

	err := mp.Start(context.Background())
	if len(started) != 2 {
		t.Fatalf("started %d pollers, want 2", len(started))
	}
	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Errorf("joined error = %v, want to contain %v", err, context.DeadlineExceeded)
	}
}

type pollerFunc func(ctx context.Context) error

func (f pollerFunc) Start(ctx context.Context) error { return f(ctx) }

func TestAdapter_NewPoller_SingleWorkspaceDirect(t *testing.T) {
	cfg := &Config{
		Enabled: true,
		Polling: &PollingConfig{Enabled: true, Interval: 45 * time.Second},
		Workspaces: []*WorkspaceConfig{
			{
				Name: "solo", APIKey: testutil.FakeLinearToken, TeamID: "OMN", TriggerLabel: "pilot",
				Polling: &PollingConfig{Enabled: true, Interval: 90 * time.Second},
			},
		},
	}
	deps := core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(ctx context.Context, ev core.IssueEvent) (*core.IssueResult, error) {
			return nil, nil
		}),
	}

	poller := New(cfg).NewPoller(deps)
	p, ok := poller.(*Poller)
	if !ok {
		t.Fatalf("NewPoller returned %T, want bare *Poller for a single workspace", poller)
	}
	if p.config.TeamID != "OMN" {
		t.Errorf("team = %q, want OMN", p.config.TeamID)
	}
	if p.interval != 90*time.Second {
		t.Errorf("interval = %v, want the workspace override 90s", p.interval)
	}
}
