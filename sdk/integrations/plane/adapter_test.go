package plane

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// TestAdapter_NewPoller_BridgesOnPRCreated verifies PollerDeps.OnPRCreated is
// bridged into the Plane-specific Poller's onPRCreated callback.
func TestAdapter_NewPoller_BridgesOnPRCreated(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.APIKey = "fake-api-key"
	cfg.WorkspaceSlug = "my-workspace"

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

	p.onPRCreated(core.PRCreatedEvent{PRNumber: 7, PRURL: "https://example.com/pr/7", IssueID: "work-item-10001"})

	if called != 1 {
		t.Errorf("OnPRCreated called %d times, want 1", called)
	}
	if capturedEvent.PRNumber != 7 {
		t.Errorf("PRNumber = %d, want 7", capturedEvent.PRNumber)
	}
	if capturedEvent.IssueID != "work-item-10001" {
		t.Errorf("IssueID = %q, want %q", capturedEvent.IssueID, "work-item-10001")
	}
}

// runAdapterPollerCase builds a poller through Adapter.NewPoller with the
// given CompletedStateAtMerge, runs one work item whose handler returns a
// successful result with a pull request, and returns the state IDs and the
// label patches set on the work item.
func runAdapterPollerCase(t *testing.T, completedStateAtMerge bool) (states []string, patches [][]string) {
	t.Helper()
	fake := newFakePlane(t)
	fake.labels["proj-a"] = projectLabelsFor("a")
	fake.states["proj-a"] = []State{
		{ID: "a-started", Name: "In Progress", Group: StateGroupStarted},
		{ID: "a-completed", Name: "Done", Group: StateGroupCompleted},
	}
	fake.items["proj-a"] = []WorkItem{{ID: "wi-1", Name: "One", ProjectID: "proj-a", LabelIDs: []string{"a-pilot"}}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.BaseURL = srv.URL
	cfg.APIKey = testutil.FakePlaneAPIKey
	cfg.WorkspaceSlug = "ws"
	cfg.ProjectIDs = []string{"proj-a"}
	cfg.Polling = &PollingConfig{Enabled: true, Interval: 10 * time.Millisecond}
	cfg.CompletedStateAtMerge = completedStateAtMerge

	var logs syncBuffer
	poller := New(cfg).NewPoller(core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(_ context.Context, _ core.IssueEvent) (*core.IssueResult, error) {
			return &core.IssueResult{Success: true, PRNumber: 42, PRURL: "https://example.test/pr"}, nil
		}),
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	p, ok := poller.(*Poller)
	if !ok {
		t.Fatalf("NewPoller returned %T, want *Poller", poller)
	}

	// The run is finished when the done label is set.
	err := runPoller(t, p, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return HasLabelID(&fake.items["proj-a"][0], "a-done")
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Start waits for the active run, so the last state update is in.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]string(nil), fake.statePatches["wi-1"]...), append([][]string(nil), fake.patches["wi-1"]...)
}

// mutation pin: Adapter.NewPoller does not add WithCompletedStateAtMerge when Config.CompletedStateAtMerge is true -> this test fails
func TestAdapter_NewPoller_CompletedStateAtMerge(t *testing.T) {
	states, patches := runAdapterPollerCase(t, true)

	if n := countState(states, "a-completed"); n != 0 {
		t.Errorf("completed state requests = %d (states %v), want 0", n, states)
	}
	if len(patches) == 0 || strings.Join(patches[len(patches)-1], ",") != "a-pilot,a-done" {
		t.Errorf("label patches = %v, want the last one to add the done label", patches)
	}
}

// mutation pin: Adapter.NewPoller adds WithCompletedStateAtMerge when Config.CompletedStateAtMerge is false -> this test fails
func TestAdapter_NewPoller_CompletedStateDefault(t *testing.T) {
	states, _ := runAdapterPollerCase(t, false)

	if n := countState(states, "a-completed"); n != 1 {
		t.Errorf("completed state requests = %d (states %v), want 1", n, states)
	}
}

// mutation pin: Adapter.NewPoller wires PollerDeps.Logger into the client, so its Warn goes to the host logger and not slog.Default() -> this test fails
func TestAdapter_NewPoller_WiresLoggerIntoClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"wi-1","name":42}]}`))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.BaseURL = srv.URL
	cfg.APIKey = "fake-api-key"
	cfg.WorkspaceSlug = "my-workspace"

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	poller := New(cfg).NewPoller(core.PollerDeps{
		Handler: core.IssueHandlerFunc(func(_ context.Context, _ core.IssueEvent) (*core.IssueResult, error) {
			return nil, nil
		}),
		Logger: logger,
	})
	p, ok := poller.(*Poller)
	if !ok {
		t.Fatalf("NewPoller returned %T, want *Poller", poller)
	}

	if _, err := p.client.ListWorkItems(context.Background(), "my-workspace", "proj-1", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "work_item_id=wi-1") {
		t.Errorf("client Warn did not reach the host logger; log:\n%s", buf.String())
	}
}
