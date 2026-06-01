package azuredevops

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func TestDefaultMergeWaiterConfig(t *testing.T) {
	config := DefaultMergeWaiterConfig()

	if config.PollInterval != 30*time.Second {
		t.Errorf("expected PollInterval 30s, got %v", config.PollInterval)
	}

	if config.Timeout != 1*time.Hour {
		t.Errorf("expected Timeout 1h, got %v", config.Timeout)
	}
}

func TestNewMergeWaiter(t *testing.T) {
	client := NewClient(testutil.FakeAzureDevOpsPAT, "org", "project")

	waiter := NewMergeWaiter(client, nil)
	if waiter.config.PollInterval != 30*time.Second {
		t.Error("expected default config when nil passed")
	}

	customConfig := &MergeWaiterConfig{
		PollInterval: 10 * time.Second,
		Timeout:      30 * time.Minute,
	}
	waiter = NewMergeWaiter(client, customConfig)
	if waiter.config.PollInterval != 10*time.Second {
		t.Error("expected custom poll interval")
	}
}

func TestMergeWaiterCheckPRStatusMerged(t *testing.T) {
	pr := PullRequest{
		PullRequestID: 42,
		Status:        PRStateCompleted,
		MergeStatus:   MergeStatusSucceeded,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", server.URL)
	waiter := NewMergeWaiter(client, DefaultMergeWaiterConfig())

	ctx := context.Background()
	result, err := waiter.checkPRStatus(ctx, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Merged {
		t.Error("expected Merged to be true")
	}
	if result.Abandoned {
		t.Error("expected Abandoned to be false")
	}
	if result.HasConflicts {
		t.Error("expected HasConflicts to be false")
	}
}

func TestMergeWaiterCheckPRStatusAbandoned(t *testing.T) {
	pr := PullRequest{
		PullRequestID: 42,
		Status:        PRStateAbandoned,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", server.URL)
	waiter := NewMergeWaiter(client, DefaultMergeWaiterConfig())

	ctx := context.Background()
	result, err := waiter.checkPRStatus(ctx, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Merged {
		t.Error("expected Merged to be false")
	}
	if !result.Abandoned {
		t.Error("expected Abandoned to be true")
	}
}

func TestMergeWaiterCheckPRStatusConflicts(t *testing.T) {
	pr := PullRequest{
		PullRequestID: 42,
		Status:        PRStateActive,
		MergeStatus:   MergeStatusConflicts,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", server.URL)
	waiter := NewMergeWaiter(client, DefaultMergeWaiterConfig())

	ctx := context.Background()
	result, err := waiter.checkPRStatus(ctx, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Merged {
		t.Error("expected Merged to be false")
	}
	if !result.HasConflicts {
		t.Error("expected HasConflicts to be true")
	}
}

func TestMergeWaiterCheckPRStatusActive(t *testing.T) {
	pr := PullRequest{
		PullRequestID: 42,
		Status:        PRStateActive,
		MergeStatus:   MergeStatusQueued,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", server.URL)
	waiter := NewMergeWaiter(client, DefaultMergeWaiterConfig())

	ctx := context.Background()
	result, err := waiter.checkPRStatus(ctx, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Merged || result.Abandoned || result.HasConflicts {
		t.Error("expected no terminal state for active PR")
	}

	if result.Message == "" {
		t.Error("expected non-empty message")
	}
}

func TestMergeWaiterWaitForMergeImmediate(t *testing.T) {
	pr := PullRequest{
		PullRequestID: 42,
		Status:        PRStateCompleted,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", server.URL)
	waiter := NewMergeWaiter(client, &MergeWaiterConfig{
		PollInterval: 100 * time.Millisecond,
		Timeout:      5 * time.Second,
	})

	ctx := context.Background()
	result, err := waiter.WaitForMerge(ctx, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Merged {
		t.Error("expected Merged to be true")
	}
}

func TestMergeWaiterWaitForMergeTimeout(t *testing.T) {
	pr := PullRequest{
		PullRequestID: 42,
		Status:        PRStateActive,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", server.URL)
	waiter := NewMergeWaiter(client, &MergeWaiterConfig{
		PollInterval: 50 * time.Millisecond,
		Timeout:      200 * time.Millisecond,
	})

	ctx := context.Background()
	result, err := waiter.WaitForMerge(ctx, 42)

	if err != ErrMergeTimeout {
		t.Errorf("expected ErrMergeTimeout, got %v", err)
	}

	if !result.TimedOut {
		t.Error("expected TimedOut to be true")
	}
}

func TestMergeWaiterWaitForMergeContextCancelled(t *testing.T) {
	pr := PullRequest{
		PullRequestID: 42,
		Status:        PRStateActive,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", server.URL)
	waiter := NewMergeWaiter(client, &MergeWaiterConfig{
		PollInterval: 100 * time.Millisecond,
		Timeout:      10 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	_, err := waiter.WaitForMerge(ctx, 42)

	if err == nil {
		t.Error("expected error, got nil")
	} else if err != context.Canceled && !errors.Is(err, context.Canceled) {
		if !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("expected context cancellation error, got %v", err)
		}
	}
}

func TestMergeWaiterWaitWithCallback(t *testing.T) {
	callCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		pr := PullRequest{
			PullRequestID: 42,
			Status:        PRStateActive,
		}

		if callCount > 1 {
			pr.Status = PRStateCompleted
		}

		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeAzureDevOpsPAT, "org", "project", server.URL)
	waiter := NewMergeWaiter(client, &MergeWaiterConfig{
		PollInterval: 50 * time.Millisecond,
		Timeout:      5 * time.Second,
	})

	var callbackCount int
	callback := func(result *MergeWaitResult) {
		callbackCount++
	}

	ctx := context.Background()
	result, err := waiter.WaitWithCallback(ctx, 42, callback)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Merged {
		t.Error("expected Merged to be true")
	}

	if callbackCount < 2 {
		t.Errorf("expected at least 2 callback calls, got %d", callbackCount)
	}
}
