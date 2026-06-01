package core

import (
	"context"
	"testing"
)

type stubAdapter struct{ name string }

func (s stubAdapter) Name() string { return s.name }

func TestRegistry(t *testing.T) {
	t.Cleanup(Reset)
	Reset()

	if got := Get("github"); got != nil {
		t.Fatalf("expected empty registry, got %v", got)
	}

	Register(stubAdapter{name: "github"})
	Register(stubAdapter{name: "linear"})

	if got := Get("github"); got == nil || got.Name() != "github" {
		t.Fatalf("Get(github) = %v, want github", got)
	}

	all := All()
	if len(all) != 2 {
		t.Fatalf("All() len = %d, want 2", len(all))
	}

	// All returns a copy: mutating it must not affect the registry.
	delete(all, "github")
	if Get("github") == nil {
		t.Fatal("All() returned a live map reference, want a copy")
	}
}

func TestIssueHandlerFunc(t *testing.T) {
	called := false
	var h IssueHandler = IssueHandlerFunc(func(_ context.Context, ev IssueEvent) (*IssueResult, error) {
		called = true
		if ev.IssueID != "uuid-001" {
			t.Fatalf("IssueID = %q, want uuid-001", ev.IssueID)
		}
		if ev.SequenceID != "42" {
			t.Fatalf("SequenceID = %q, want 42", ev.SequenceID)
		}
		if ev.Priority != "high" {
			t.Fatalf("Priority = %q, want high", ev.Priority)
		}
		return &IssueResult{Success: true, PRNumber: 7}, nil
	})

	res, err := h.HandleIssue(context.Background(), IssueEvent{
		IssueID:    "uuid-001",
		SequenceID: "42",
		Priority:   "high",
	})
	if err != nil {
		t.Fatalf("HandleIssue error: %v", err)
	}
	if !called || res == nil || res.PRNumber != 7 {
		t.Fatalf("handler not invoked correctly: called=%v res=%v", called, res)
	}
}

type stubLister struct{ ids []string }

func (s stubLister) ListActiveTaskIDs(_ context.Context) ([]string, error) { return s.ids, nil }

func TestActiveExecutionLister(t *testing.T) {
	var _ ActiveExecutionLister = stubLister{}

	lister := stubLister{ids: []string{"GL-42", "GL-99"}}
	got, err := lister.ListActiveTaskIDs(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != "GL-42" || got[1] != "GL-99" {
		t.Fatalf("ListActiveTaskIDs = %v, want [GL-42 GL-99]", got)
	}
}

func TestIssueResultSkipped(t *testing.T) {
	var h IssueHandler = IssueHandlerFunc(func(_ context.Context, _ IssueEvent) (*IssueResult, error) {
		return &IssueResult{Skipped: true, SkipReason: "in_progress"}, nil
	})

	res, err := h.HandleIssue(context.Background(), IssueEvent{IssueID: "1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Skipped || res.SkipReason != "in_progress" {
		t.Fatalf("Skipped=%v SkipReason=%q, want true/in_progress", res.Skipped, res.SkipReason)
	}
}
