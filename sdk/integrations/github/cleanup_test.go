package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// fakeLister implements core.ActiveExecutionLister for tests.
type fakeLister struct {
	mu  sync.Mutex
	ids []string
}

func (f *fakeLister) ListActiveTaskIDs(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ids...), nil
}

func TestNewCleaner_InvalidRepo(t *testing.T) {
	_, err := NewCleaner(nil, &fakeLister{}, "badrepo", &StaleLabelCleanupConfig{})
	if err == nil {
		t.Fatal("expected error for invalid repo format")
	}
}

func TestCleaner_Cleanup_RemovesStaleInProgressLabel(t *testing.T) {
	staleUpdatedAt := time.Now().Add(-2 * time.Hour)
	issue := &Issue{
		Number:    10,
		Title:     "stale issue",
		State:     StateOpen,
		Labels:    []Label{{Name: LabelInProgress}},
		UpdatedAt: staleUpdatedAt,
	}

	var removedLabels []string
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode([]*Issue{issue})
		case r.Method == http.MethodDelete:
			parts := splitURLPath(r.URL.Path)
			label := parts[len(parts)-1]
			mu.Lock()
			removedLabels = append(removedLabels, label)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && contains(r.URL.Path, "/comments"):
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	lister := &fakeLister{} // no active task IDs

	cleaner, err := NewCleaner(client, lister, "owner/repo", &StaleLabelCleanupConfig{
		Interval:  time.Hour,
		Threshold: 1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewCleaner: %v", err)
	}

	if err := cleaner.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	mu.Lock()
	got := removedLabels
	mu.Unlock()

	found := false
	for _, l := range got {
		if l == LabelInProgress {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %s to be removed, removed labels: %v", LabelInProgress, got)
	}
}

func TestCleaner_Cleanup_SkipsActiveExecution(t *testing.T) {
	staleUpdatedAt := time.Now().Add(-2 * time.Hour)
	issue := &Issue{
		Number:    42,
		Title:     "active issue",
		State:     StateOpen,
		Labels:    []Label{{Name: LabelInProgress}},
		UpdatedAt: staleUpdatedAt,
	}

	var removedLabels []string
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/issues":
			_ = json.NewEncoder(w).Encode([]*Issue{issue})
		case r.Method == http.MethodDelete:
			parts := splitURLPath(r.URL.Path)
			mu.Lock()
			removedLabels = append(removedLabels, parts[len(parts)-1])
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	// Issue 42 is active → taskID = "GH-42"
	lister := &fakeLister{ids: []string{"GH-42"}}

	cleaner, err := NewCleaner(client, lister, "owner/repo", &StaleLabelCleanupConfig{
		Interval:  time.Hour,
		Threshold: 1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewCleaner: %v", err)
	}

	if err := cleaner.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	mu.Lock()
	got := removedLabels
	mu.Unlock()

	for _, l := range got {
		if l == LabelInProgress {
			t.Errorf("should not have removed %s for active execution", LabelInProgress)
		}
	}
}

func TestCleaner_Stop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]*Issue{})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(testutil.FakeGitHubToken, server.URL)
	cleaner, err := NewCleaner(client, &fakeLister{}, "owner/repo", &StaleLabelCleanupConfig{
		Interval:  100 * time.Millisecond,
		Threshold: 1 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewCleaner: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go cleaner.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	cleaner.Stop()
}

func splitURLPath(p string) []string {
	var parts []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return parts
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
