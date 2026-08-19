package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func TestNewPoller(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{
		TriggerLabel: "pilot",
		ProjectKey:   "TEST",
	}
	poller := NewPoller(client, config, 30*time.Second)

	if poller.pilotLabel != "pilot" {
		t.Errorf("expected pilotLabel 'pilot', got '%s'", poller.pilotLabel)
	}
	if poller.interval != 30*time.Second {
		t.Errorf("expected interval 30s, got %v", poller.interval)
	}
	if len(poller.processed) != 0 {
		t.Error("expected empty processed map")
	}
}

func TestNewPoller_DefaultLabel(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{TriggerLabel: ""}
	poller := NewPoller(client, config, 30*time.Second)

	if poller.pilotLabel != "pilot" {
		t.Errorf("expected default pilotLabel 'pilot', got '%s'", poller.pilotLabel)
	}
}

func TestPollerWithOptions(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{TriggerLabel: "pilot"}

	var callbackCalled bool
	handler := func(ctx context.Context, issue *Issue) (*IssueResult, error) {
		callbackCalled = true
		return &IssueResult{Success: true}, nil
	}

	poller := NewPoller(client, config, 30*time.Second, WithOnJiraIssue(handler))

	if poller.onIssue == nil {
		t.Error("expected onIssue handler to be set")
	}

	_, _ = poller.onIssue(context.Background(), &Issue{})
	if !callbackCalled {
		t.Error("expected callback to be called")
	}
}

func TestPollerMarkProcessed(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{TriggerLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second)

	if poller.IsProcessed("TEST-123") {
		t.Error("expected TEST-123 NOT to be processed initially")
	}

	poller.markProcessed("TEST-123")

	if !poller.IsProcessed("TEST-123") {
		t.Error("expected TEST-123 to be processed after marking")
	}
	if poller.ProcessedCount() != 1 {
		t.Errorf("expected processed count 1, got %d", poller.ProcessedCount())
	}

	poller.Reset()

	if poller.IsProcessed("TEST-123") {
		t.Error("expected TEST-123 NOT to be processed after reset")
	}
	if poller.ProcessedCount() != 0 {
		t.Errorf("expected processed count 0 after reset, got %d", poller.ProcessedCount())
	}
}

func TestPollerClearProcessed(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{TriggerLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second)

	poller.markProcessed("TEST-123")
	poller.markProcessed("TEST-456")

	if poller.ProcessedCount() != 2 {
		t.Errorf("expected processed count 2, got %d", poller.ProcessedCount())
	}

	poller.ClearProcessed("TEST-123")

	if poller.IsProcessed("TEST-123") {
		t.Error("expected TEST-123 NOT to be processed after clearing")
	}
	if !poller.IsProcessed("TEST-456") {
		t.Error("expected TEST-456 to still be processed")
	}
	if poller.ProcessedCount() != 1 {
		t.Errorf("expected processed count 1 after clearing one, got %d", poller.ProcessedCount())
	}
}

func TestPollerConcurrentAccess(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{TriggerLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "TEST-" + string(rune('0'+n%10))
			poller.markProcessed(key)
			_ = poller.IsProcessed(key)
			_ = poller.ProcessedCount()
		}(i)
	}
	wg.Wait()

	count := poller.ProcessedCount()
	if count == 0 {
		t.Error("expected some processed items")
	}
}

func TestPollerBuildJQL(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)

	tests := []struct {
		name    string
		config  *Config
		wantJQL string
	}{
		{
			name:    "label only",
			config:  &Config{TriggerLabel: "pilot"},
			wantJQL: `labels = "pilot" AND statusCategory != Done ORDER BY created ASC`,
		},
		{
			name:    "label and project",
			config:  &Config{TriggerLabel: "pilot", ProjectKey: "TEST"},
			wantJQL: `labels = "pilot" AND project = "TEST" AND statusCategory != Done ORDER BY created ASC`,
		},
		{
			name:    "custom label",
			config:  &Config{TriggerLabel: "autopilot", ProjectKey: "MYPROJ"},
			wantJQL: `labels = "autopilot" AND project = "MYPROJ" AND statusCategory != Done ORDER BY created ASC`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			poller := NewPoller(client, tt.config, 30*time.Second)
			got := poller.buildJQL()
			if got != tt.wantJQL {
				t.Errorf("buildJQL() = %q, want %q", got, tt.wantJQL)
			}
		})
	}
}

func TestPollerCheckForNewIssues(t *testing.T) {
	var requestCount int
	var processedIssue *Issue

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/search/jql" {
			resp := SearchResponse{
				Issues: []*Issue{
					{
						Key: "TEST-1",
						Fields: Fields{
							Summary:     "First issue",
							Description: "Test description",
							Created:     "2024-01-01T10:00:00.000+0000",
							Labels:      []string{"pilot"},
						},
					},
					{
						Key: "TEST-2",
						Fields: Fields{
							Summary:     "Second issue (in progress)",
							Description: "Already being worked on",
							Created:     "2024-01-02T10:00:00.000+0000",
							Labels:      []string{"pilot", "pilot-in-progress"},
						},
					},
				},
				IsLast: true,
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL, testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{TriggerLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second,
		WithOnJiraIssue(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			processedIssue = issue
			return &IssueResult{Success: true}, nil
		}),
	)

	ctx := context.Background()
	poller.checkForNewIssues(ctx)
	poller.WaitForActive()

	if processedIssue == nil {
		t.Fatal("expected an issue to be processed")
	}
	if processedIssue.Key != "TEST-1" {
		t.Errorf("expected TEST-1 to be processed, got %s", processedIssue.Key)
	}
	if !poller.IsProcessed("TEST-1") {
		t.Error("expected TEST-1 to be marked as processed")
	}
}

func TestPollerCheckForNewIssues_SkipsAlreadyProcessed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/search/jql" {
			resp := SearchResponse{
				Issues: []*Issue{
					{
						Key: "TEST-1",
						Fields: Fields{
							Summary: "Already processed",
							Labels:  []string{"pilot"},
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL, testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{TriggerLabel: "pilot"}

	var callCount int
	poller := NewPoller(client, config, 30*time.Second,
		WithOnJiraIssue(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			callCount++
			return &IssueResult{Success: true}, nil
		}),
	)

	poller.markProcessed("TEST-1")

	ctx := context.Background()
	poller.checkForNewIssues(ctx)

	if callCount != 0 {
		t.Errorf("expected callback not to be called for already processed issue, got %d calls", callCount)
	}
}

func TestPollerStart_CancelsOnContextDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := SearchResponse{Issues: []*Issue{}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClient(server.URL, testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{TriggerLabel: "pilot"}
	poller := NewPoller(client, config, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- poller.Start(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected nil error on cancel, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("poller did not stop after context cancellation")
	}
}

func TestPollerHasStatusLabel(t *testing.T) {
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)
	config := &Config{TriggerLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second)

	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"no labels", []string{}, false},
		{"pilot only", []string{"pilot"}, false},
		{"in-progress", []string{"pilot", "pilot-in-progress"}, true},
		{"done", []string{"pilot", "pilot-done"}, true},
		{"failed", []string{"pilot", "pilot-failed"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{Fields: Fields{Labels: tt.labels}}
			got := poller.hasStatusLabel(issue)
			if got != tt.want {
				t.Errorf("hasStatusLabel() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPoller_SanitizeCalledInLivePath is the ASCII-smuggling guard.
// Invisible Unicode injected into issue Summary/Description must be stripped
// before reaching the IssueHandler callback.
func TestPoller_SanitizeCalledInLivePath(t *testing.T) {
	zwsp := "​" // U+200B ZERO WIDTH SPACE
	dirtyTitle := "Fix the" + zwsp + " bug"
	dirtyDesc := "Do the" + zwsp + " thing"

	var capturedTitle, capturedDesc string

	config := &Config{TriggerLabel: "pilot"}
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)

	poller := NewPoller(client, config, 30*time.Second,
		WithOnJiraIssue(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			capturedTitle = issue.Fields.Summary
			capturedDesc = string(issue.Fields.Description)
			return &IssueResult{Success: true}, nil
		}),
	)

	issue := &Issue{
		ID:  "10001",
		Key: "TEST-99",
		Fields: Fields{
			Summary:     dirtyTitle,
			Description: ADFText(dirtyDesc),
			Labels:      []string{"pilot"},
		},
	}

	ctx := context.Background()
	poller.semaphore <- struct{}{}
	poller.activeWg.Add(1)
	go poller.processIssueAsync(ctx, issue)
	poller.activeWg.Wait()

	if strings.Contains(capturedTitle, zwsp) {
		t.Errorf("Summary still contains invisible Unicode after sanitize: %q", capturedTitle)
	}
	if strings.Contains(capturedDesc, zwsp) {
		t.Errorf("Description still contains invisible Unicode after sanitize: %q", capturedDesc)
	}
	if capturedTitle == "" {
		t.Error("capturedTitle is empty — handler was not called")
	}
}

func TestPoller_OnPRCreated(t *testing.T) {
	config := &Config{TriggerLabel: "pilot"}
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)

	var prCallbackCalled int
	var capturedEvent core.PRCreatedEvent

	poller := NewPoller(client, config, 30*time.Second,
		WithOnJiraIssue(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			return &IssueResult{
				Success:    true,
				PRNumber:   42,
				PRURL:      "https://github.com/org/repo/pull/42",
				HeadSHA:    "abc123",
				BranchName: "pilot/TEST-99",
			}, nil
		}),
		WithOnPRCreated(func(ev core.PRCreatedEvent) {
			prCallbackCalled++
			capturedEvent = ev
		}),
	)

	issue := &Issue{ID: "10001", Key: "TEST-99", Fields: Fields{Summary: "Test Issue", Labels: []string{"pilot"}}}

	ctx := context.Background()
	poller.semaphore <- struct{}{}
	poller.activeWg.Add(1)
	go poller.processIssueAsync(ctx, issue)
	poller.activeWg.Wait()

	if prCallbackCalled != 1 {
		t.Errorf("OnPRCreated called %d times, want 1", prCallbackCalled)
	}
	if capturedEvent.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", capturedEvent.PRNumber)
	}
	if capturedEvent.PRURL != "https://github.com/org/repo/pull/42" {
		t.Errorf("PRURL = %q, want %q", capturedEvent.PRURL, "https://github.com/org/repo/pull/42")
	}
	if capturedEvent.IssueID != "10001" {
		t.Errorf("IssueID = %q, want %q", capturedEvent.IssueID, "10001")
	}
	if capturedEvent.HeadSHA != "abc123" {
		t.Errorf("HeadSHA = %q, want %q", capturedEvent.HeadSHA, "abc123")
	}
	if capturedEvent.BranchName != "pilot/TEST-99" {
		t.Errorf("BranchName = %q, want %q", capturedEvent.BranchName, "pilot/TEST-99")
	}
}

func TestPoller_OnPRCreated_NotCalledOnFailure(t *testing.T) {
	config := &Config{TriggerLabel: "pilot"}
	client := NewClient("https://example.atlassian.net", testutil.FakeJiraUsername, testutil.FakeJiraAPIToken, PlatformCloud)

	var prCallbackCalled int

	poller := NewPoller(client, config, 30*time.Second,
		WithOnJiraIssue(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			return nil, fmt.Errorf("processing failed")
		}),
		WithOnPRCreated(func(ev core.PRCreatedEvent) {
			prCallbackCalled++
		}),
	)

	issue := &Issue{ID: "10001", Key: "TEST-99", Fields: Fields{Summary: "Test Issue", Labels: []string{"pilot"}}}

	ctx := context.Background()
	poller.semaphore <- struct{}{}
	poller.activeWg.Add(1)
	go poller.processIssueAsync(ctx, issue)
	poller.activeWg.Wait()

	if prCallbackCalled != 0 {
		t.Errorf("OnPRCreated called %d times on failure, want 0", prCallbackCalled)
	}
}
