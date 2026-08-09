package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// TestWithAdapterClient_InjectedIntoPoller verifies WithAdapterClient routes
// the injected client into the Poller NewPoller constructs, rather than the
// adapter building its own static-token client from Config.Token.
func TestWithAdapterClient_InjectedIntoPoller(t *testing.T) {
	injected := NewClient("injected-token")

	cfg := DefaultConfig()
	cfg.Token = "boot-token"
	cfg.Repo = "owner/repo"

	a := New(cfg, WithAdapterClient(injected))

	deps := core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(_ context.Context, _ core.IssueEvent) (*core.IssueResult, error) {
			return nil, nil
		}),
	}

	got := a.NewPoller(deps)
	p, ok := got.(*Poller)
	if !ok {
		t.Fatalf("NewPoller returned %T, want *Poller", got)
	}
	if p.client != injected {
		t.Errorf("poller.client = %p, want injected client %p", p.client, injected)
	}
}

// TestWithAdapterClient_Nil verifies WithAdapterClient(nil) is a no-op: the
// adapter falls back to constructing a static-token client from
// Config.Token, exactly as if no option had been passed.
func TestWithAdapterClient_Nil(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Token = "boot-token"
	cfg.Repo = "owner/repo"

	a := New(cfg, WithAdapterClient(nil))

	deps := core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(_ context.Context, _ core.IssueEvent) (*core.IssueResult, error) {
			return nil, nil
		}),
	}

	got := a.NewPoller(deps)
	p, ok := got.(*Poller)
	if !ok {
		t.Fatalf("NewPoller returned %T, want *Poller", got)
	}
	if p.client == nil {
		t.Fatal("poller.client is nil, want a client constructed from Config.Token")
	}
	if p.client.token != "boot-token" {
		t.Errorf("poller.client.token = %q, want %q", p.client.token, "boot-token")
	}
}

// TestWithAdapterClient_NoInjection verifies the pre-existing behavior is
// unchanged when no AdapterOption is passed at all: NewPoller still
// constructs a static-token client from Config.Token.
func TestWithAdapterClient_NoInjection(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Token = "boot-token"
	cfg.Repo = "owner/repo"

	a := New(cfg)

	deps := core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(_ context.Context, _ core.IssueEvent) (*core.IssueResult, error) {
			return nil, nil
		}),
	}

	got := a.NewPoller(deps)
	p, ok := got.(*Poller)
	if !ok {
		t.Fatalf("NewPoller returned %T, want *Poller", got)
	}
	if p.client == nil || p.client.token != "boot-token" {
		t.Fatalf("poller.client not constructed from Config.Token as expected")
	}
}

// TestWithAdapterClient_RotationReachesPollPath is the load-bearing
// regression test for GH-109: a TokenFunc-backed client injected via
// WithAdapterClient must have its token re-resolved on every poll, all the
// way through Adapter.NewPoller -> Poller.checkForNewIssues -> client.doRequest.
// No construction-time freeze anywhere in the adapter->poller chain.
func TestWithAdapterClient_RotationReachesPollPath(t *testing.T) {
	var seenAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]*Issue{})
	}))
	defer server.Close()

	var calls int32
	tokenFn := func(_ context.Context) (string, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return "token-v1", nil
		}
		return "token-v2", nil
	}
	injected := NewClientWithTokenFunc(tokenFn, WithClientBaseURL(server.URL))
	injected.retryOpts = RetryOptions{MaxRetries: 0}

	cfg := DefaultConfig()
	cfg.Token = "boot-token" // must be ignored: injected client wins
	cfg.Repo = "owner/repo"

	a := New(cfg, WithAdapterClient(injected))

	deps := core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(_ context.Context, _ core.IssueEvent) (*core.IssueResult, error) {
			return nil, nil
		}),
	}

	got := a.NewPoller(deps)
	p, ok := got.(*Poller)
	if !ok {
		t.Fatalf("NewPoller returned %T, want *Poller", got)
	}

	ctx := context.Background()
	p.checkForNewIssues(ctx)
	p.checkForNewIssues(ctx)

	want := []string{"Bearer token-v1", "Bearer token-v2"}
	if len(seenAuth) != 2 || seenAuth[0] != want[0] || seenAuth[1] != want[1] {
		t.Errorf("seenAuth = %v, want %v", seenAuth, want)
	}
}
