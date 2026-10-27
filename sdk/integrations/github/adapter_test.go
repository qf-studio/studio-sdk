package github

import (
	"testing"
	"time"

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

func TestNewPollerSequentialSettings_Defaults(t *testing.T) {
	a := New(&Config{Token: "t", Repo: "o/r"})
	p := a.NewPoller(core.PollerDeps{}).(*Poller)
	if !p.waitForMerge || p.prPollInterval != 30*time.Second || p.prTimeout != time.Hour {
		t.Errorf("got %v/%v/%v, want true/30s/1h", p.waitForMerge, p.prPollInterval, p.prTimeout)
	}
}

func TestNewPollerSequentialSettings_AppliedToPoller(t *testing.T) {
	no := false
	a := New(&Config{Token: "t", Repo: "o/r"})
	p := a.NewPoller(core.PollerDeps{
		WaitForMerge:   &no,
		PRPollInterval: 15 * time.Second,
		PRTimeout:      10 * time.Minute,
	}).(*Poller)
	if p.waitForMerge || p.prPollInterval != 15*time.Second || p.prTimeout != 10*time.Minute {
		t.Errorf("got %v/%v/%v, want false/15s/10m", p.waitForMerge, p.prPollInterval, p.prTimeout)
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
			a := New(&Config{Token: "t", Repo: "o/r"})
			p := a.NewPoller(tt.deps).(*Poller)
			if p.waitForMerge != tt.wantWait || p.prPollInterval != tt.wantIvl || p.prTimeout != tt.wantTO {
				t.Errorf("got %v/%v/%v, want %v/%v/%v", p.waitForMerge, p.prPollInterval, p.prTimeout, tt.wantWait, tt.wantIvl, tt.wantTO)
			}
		})
	}
}
