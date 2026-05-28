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
		if ev.IssueID != "42" {
			t.Fatalf("IssueID = %q, want 42", ev.IssueID)
		}
		return &IssueResult{Success: true, PRNumber: 7}, nil
	})

	res, err := h.HandleIssue(context.Background(), IssueEvent{IssueID: "42"})
	if err != nil {
		t.Fatalf("HandleIssue error: %v", err)
	}
	if !called || res == nil || res.PRNumber != 7 {
		t.Fatalf("handler not invoked correctly: called=%v res=%v", called, res)
	}
}
