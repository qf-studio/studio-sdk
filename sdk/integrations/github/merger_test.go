package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func TestMergeWaiter_WaitForMerge_Merged(t *testing.T) {
	merged := true
	pr := &PullRequest{
		Number:  42,
		State:   "closed",
		Merged:  true,
		HTMLURL: "https://github.com/owner/repo/pull/42",
		Mergeable: &merged,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	waiter := NewMergeWaiter(client, "owner", "repo", &MergeWaiterConfig{
		PollInterval: 10 * time.Millisecond,
		Timeout:      1 * time.Second,
	})

	result, err := waiter.WaitForMerge(context.Background(), 42)
	if err != nil {
		t.Fatalf("WaitForMerge: %v", err)
	}
	if !result.Merged {
		t.Errorf("expected Merged=true, got %v", result)
	}
}

func TestMergeWaiter_WaitForMerge_Closed(t *testing.T) {
	pr := &PullRequest{
		Number:  42,
		State:   "closed",
		Merged:  false,
		HTMLURL: "https://github.com/owner/repo/pull/42",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	waiter := NewMergeWaiter(client, "owner", "repo", &MergeWaiterConfig{
		PollInterval: 10 * time.Millisecond,
		Timeout:      1 * time.Second,
	})

	result, err := waiter.WaitForMerge(context.Background(), 42)
	if err != nil {
		t.Fatalf("WaitForMerge: %v", err)
	}
	if !result.Closed {
		t.Errorf("expected Closed=true, got %v", result)
	}
}

func TestMergeWaiter_WaitForMerge_Conflicts(t *testing.T) {
	notMergeable := false
	pr := &PullRequest{
		Number:    42,
		State:     "open",
		HTMLURL:   "https://github.com/owner/repo/pull/42",
		Mergeable: &notMergeable,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	waiter := NewMergeWaiter(client, "owner", "repo", &MergeWaiterConfig{
		PollInterval: 10 * time.Millisecond,
		Timeout:      1 * time.Second,
	})

	result, err := waiter.WaitForMerge(context.Background(), 42)
	if err != nil {
		t.Fatalf("WaitForMerge: %v", err)
	}
	if !result.Conflicting {
		t.Errorf("expected Conflicting=true, got %v", result)
	}
}

func TestMergeWaiter_WaitForMerge_Timeout(t *testing.T) {
	pr := &PullRequest{
		Number:  42,
		State:   "open",
		HTMLURL: "https://github.com/owner/repo/pull/42",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pr)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	waiter := NewMergeWaiter(client, "owner", "repo", &MergeWaiterConfig{
		PollInterval: 10 * time.Millisecond,
		Timeout:      50 * time.Millisecond,
	})

	result, err := waiter.WaitForMerge(context.Background(), 42)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !result.TimedOut {
		t.Errorf("expected TimedOut=true, got %v", result)
	}
}

func TestMergeWaiter_DefaultConfig(t *testing.T) {
	cfg := DefaultMergeWaiterConfig()
	if cfg == nil {
		t.Fatal("DefaultMergeWaiterConfig returned nil")
	}
	if cfg.PollInterval == 0 {
		t.Error("PollInterval should not be 0")
	}
	if cfg.Timeout == 0 {
		t.Error("Timeout should not be 0")
	}
}
