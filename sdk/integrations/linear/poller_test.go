package linear

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockProcessedStore implements ProcessedStore for testing.
type mockProcessedStore struct {
	mu        sync.Mutex
	processed map[string]string // id -> result
}

func newMockProcessedStore() *mockProcessedStore {
	return &mockProcessedStore{
		processed: make(map[string]string),
	}
}

func (m *mockProcessedStore) Mark(source, repo, issueID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.processed[issueID] = "processed"
	return nil
}

func (m *mockProcessedStore) Unmark(source, repo, issueID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.processed, issueID)
	return nil
}

func (m *mockProcessedStore) IsProcessed(source, repo, issueID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.processed[issueID]
	return ok, nil
}

func (m *mockProcessedStore) Load(source, repo string) (map[string]time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]time.Time)
	for id := range m.processed {
		result[id] = time.Now()
	}
	return result, nil
}

func TestPoller_LoadsProcessedFromStore(t *testing.T) {
	store := newMockProcessedStore()

	store.processed["linear-issue-1"] = "processed"
	store.processed["linear-issue-2"] = "processed"

	config := &WorkspaceConfig{
		TeamID:       "TEST",
		TriggerLabel: "pilot",
	}

	poller := NewPoller(nil, config, 30, WithProcessedStore(store))

	if poller.ProcessedCount() != 2 {
		t.Errorf("ProcessedCount = %d, want 2", poller.ProcessedCount())
	}

	if !poller.IsProcessed("linear-issue-1") {
		t.Error("linear-issue-1 should be processed")
	}
	if !poller.IsProcessed("linear-issue-2") {
		t.Error("linear-issue-2 should be processed")
	}
	if poller.IsProcessed("linear-issue-3") {
		t.Error("linear-issue-3 should not be processed")
	}
}

func TestPoller_MarkProcessed_PersistsToStore(t *testing.T) {
	store := newMockProcessedStore()
	config := &WorkspaceConfig{
		TeamID:       "TEST",
		TriggerLabel: "pilot",
	}

	poller := NewPoller(nil, config, 30, WithProcessedStore(store))

	poller.markProcessed("new-linear-issue")

	if !poller.IsProcessed("new-linear-issue") {
		t.Error("new-linear-issue should be processed in memory")
	}

	store.mu.Lock()
	_, exists := store.processed["new-linear-issue"]
	store.mu.Unlock()
	if !exists {
		t.Error("new-linear-issue should be persisted to store")
	}
}

func TestPoller_ClearProcessed_RemovesFromStore(t *testing.T) {
	store := newMockProcessedStore()
	store.processed["issue-to-clear"] = "processed"

	config := &WorkspaceConfig{
		TeamID:       "TEST",
		TriggerLabel: "pilot",
	}

	poller := NewPoller(nil, config, 30, WithProcessedStore(store))

	if !poller.IsProcessed("issue-to-clear") {
		t.Error("issue-to-clear should be processed initially")
	}

	poller.ClearProcessed("issue-to-clear")

	if poller.IsProcessed("issue-to-clear") {
		t.Error("issue-to-clear should not be processed after clearing")
	}

	store.mu.Lock()
	_, exists := store.processed["issue-to-clear"]
	store.mu.Unlock()
	if exists {
		t.Error("issue-to-clear should be removed from store")
	}
}

func TestPoller_WithoutStore_StillWorks(t *testing.T) {
	config := &WorkspaceConfig{
		TeamID:       "TEST",
		TriggerLabel: "pilot",
	}

	poller := NewPoller(nil, config, 30)

	poller.markProcessed("memory-only-issue")

	if !poller.IsProcessed("memory-only-issue") {
		t.Error("memory-only-issue should be processed")
	}

	poller.ClearProcessed("memory-only-issue")

	if poller.IsProcessed("memory-only-issue") {
		t.Error("memory-only-issue should not be processed after clearing")
	}
}

func TestPoller_Reset_ClearsMemoryOnly(t *testing.T) {
	store := newMockProcessedStore()
	config := &WorkspaceConfig{
		TeamID:       "TEST",
		TriggerLabel: "pilot",
	}

	poller := NewPoller(nil, config, 30, WithProcessedStore(store))

	poller.markProcessed("persistent-issue")

	store.mu.Lock()
	_, exists := store.processed["persistent-issue"]
	store.mu.Unlock()
	if !exists {
		t.Fatal("persistent-issue should be in store")
	}

	poller.Reset()

	if poller.IsProcessed("persistent-issue") {
		t.Error("persistent-issue should not be in memory after Reset")
	}

	store.mu.Lock()
	_, exists = store.processed["persistent-issue"]
	store.mu.Unlock()
	if !exists {
		t.Error("persistent-issue should still be in store after Reset")
	}
}

func TestPoller_WithMaxConcurrent(t *testing.T) {
	config := &WorkspaceConfig{
		TeamID:       "TEST",
		TriggerLabel: "pilot",
	}

	poller := NewPoller(nil, config, 30, WithMaxConcurrent(5))

	if poller.maxConcurrent != 5 {
		t.Errorf("maxConcurrent = %d, want 5", poller.maxConcurrent)
	}

	if cap(poller.semaphore) != 5 {
		t.Errorf("semaphore capacity = %d, want 5", cap(poller.semaphore))
	}
}

func TestPoller_WithMaxConcurrent_DefaultsToTwo(t *testing.T) {
	config := &WorkspaceConfig{
		TeamID:       "TEST",
		TriggerLabel: "pilot",
	}

	poller := NewPoller(nil, config, 30)

	if poller.maxConcurrent != 2 {
		t.Errorf("default maxConcurrent = %d, want 2", poller.maxConcurrent)
	}
}

func TestPoller_WithMaxConcurrent_MinimumOne(t *testing.T) {
	config := &WorkspaceConfig{
		TeamID:       "TEST",
		TriggerLabel: "pilot",
	}

	poller := NewPoller(nil, config, 30, WithMaxConcurrent(0))

	if poller.maxConcurrent != 1 {
		t.Errorf("maxConcurrent with 0 = %d, want 1 (minimum)", poller.maxConcurrent)
	}

	poller2 := NewPoller(nil, config, 30, WithMaxConcurrent(-5))

	if poller2.maxConcurrent != 1 {
		t.Errorf("maxConcurrent with -5 = %d, want 1 (minimum)", poller2.maxConcurrent)
	}
}

func TestPoller_Drain(t *testing.T) {
	config := &WorkspaceConfig{
		TeamID:       "TEST",
		TriggerLabel: "pilot",
	}

	poller := NewPoller(nil, config, 30)

	poller.semaphore <- struct{}{}
	poller.activeWg.Add(1)

	drainDone := make(chan struct{})
	go func() {
		poller.Drain()
		close(drainDone)
	}()

	time.Sleep(10 * time.Millisecond)

	if !poller.stopping.Load() {
		t.Error("stopping should be true after Drain called")
	}

	<-poller.semaphore
	poller.activeWg.Done()

	select {
	case <-drainDone:
		// Good
	case <-time.After(100 * time.Millisecond):
		t.Error("Drain should complete after active tasks finish")
	}
}

func TestPoller_WaitForActive(t *testing.T) {
	config := &WorkspaceConfig{
		TeamID:       "TEST",
		TriggerLabel: "pilot",
	}

	poller := NewPoller(nil, config, 30)

	poller.semaphore <- struct{}{}
	poller.activeWg.Add(1)

	waitDone := make(chan struct{})
	go func() {
		poller.WaitForActive()
		close(waitDone)
	}()

	time.Sleep(10 * time.Millisecond)

	if !poller.stopping.Load() {
		t.Error("stopping should be true after WaitForActive called")
	}

	<-poller.semaphore
	poller.activeWg.Done()

	select {
	case <-waitDone:
		// Good
	case <-time.After(100 * time.Millisecond):
		t.Error("WaitForActive should complete after active tasks finish")
	}
}

func TestPoller_ParallelDispatch(t *testing.T) {
	config := &WorkspaceConfig{
		TeamID:       "TEST",
		TriggerLabel: "pilot",
	}

	var callCount int32
	var maxConcurrent int32
	var currentConcurrent int32
	var mu sync.Mutex

	poller := NewPoller(nil, config, 30*time.Second,
		WithMaxConcurrent(3),
		WithOnLinearIssue(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			mu.Lock()
			current := atomic.AddInt32(&currentConcurrent, 1)
			if current > atomic.LoadInt32(&maxConcurrent) {
				atomic.StoreInt32(&maxConcurrent, current)
			}
			mu.Unlock()

			atomic.AddInt32(&callCount, 1)
			time.Sleep(50 * time.Millisecond)

			atomic.AddInt32(&currentConcurrent, -1)
			return &IssueResult{Success: true}, nil
		}),
	)

	now := time.Now()
	issues := []*Issue{
		{ID: "issue-1", Identifier: "TST-1", Title: "Issue 1", CreatedAt: now},
		{ID: "issue-2", Identifier: "TST-2", Title: "Issue 2", CreatedAt: now},
		{ID: "issue-3", Identifier: "TST-3", Title: "Issue 3", CreatedAt: now},
	}

	ctx := context.Background()

	for _, issue := range issues {
		poller.markProcessed(issue.ID)
		poller.semaphore <- struct{}{}
		poller.activeWg.Add(1)
		go poller.processIssueAsync(ctx, issue)
	}

	poller.activeWg.Wait()

	if got := atomic.LoadInt32(&callCount); got != 3 {
		t.Errorf("callCount = %d, want 3", got)
	}

	if got := atomic.LoadInt32(&maxConcurrent); got < 2 {
		t.Logf("maxConcurrent = %d (some concurrency expected with 3 max)", got)
	}
}

func TestPoller_OnPRCreated(t *testing.T) {
	config := &WorkspaceConfig{
		TeamID:       "TEST",
		TriggerLabel: "pilot",
	}

	var prCallbackCalled int32
	var capturedPRNumber int
	var capturedPRURL string

	poller := NewPoller(nil, config, 30*time.Second,
		WithOnLinearIssue(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			return &IssueResult{
				Success:    true,
				PRNumber:   42,
				PRURL:      "https://github.com/org/repo/pull/42",
				HeadSHA:    "abc123",
				BranchName: "pilot/TST-1",
			}, nil
		}),
		WithOnPRCreated(func(prNumber int, prURL string, issueNumber int, headSHA string, branchName string, issueNodeID string) {
			atomic.AddInt32(&prCallbackCalled, 1)
			capturedPRNumber = prNumber
			capturedPRURL = prURL
		}),
	)

	ctx := context.Background()
	issue := &Issue{ID: "issue-1", Identifier: "TST-1", Title: "Test Issue"}

	poller.semaphore <- struct{}{}
	poller.activeWg.Add(1)
	go poller.processIssueAsync(ctx, issue)
	poller.activeWg.Wait()

	if got := atomic.LoadInt32(&prCallbackCalled); got != 1 {
		t.Errorf("OnPRCreated called %d times, want 1", got)
	}
	if capturedPRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", capturedPRNumber)
	}
	if capturedPRURL != "https://github.com/org/repo/pull/42" {
		t.Errorf("PRURL = %q, want %q", capturedPRURL, "https://github.com/org/repo/pull/42")
	}
}

func TestPoller_OnPRCreated_NotCalledOnFailure(t *testing.T) {
	config := &WorkspaceConfig{
		TeamID:       "TEST",
		TriggerLabel: "pilot",
	}

	var prCallbackCalled int32

	poller := NewPoller(nil, config, 30*time.Second,
		WithOnLinearIssue(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			return nil, fmt.Errorf("processing failed")
		}),
		WithOnPRCreated(func(prNumber int, prURL string, issueNumber int, headSHA string, branchName string, issueNodeID string) {
			atomic.AddInt32(&prCallbackCalled, 1)
		}),
	)

	ctx := context.Background()
	issue := &Issue{ID: "issue-1", Identifier: "TST-1", Title: "Test Issue"}

	poller.semaphore <- struct{}{}
	poller.activeWg.Add(1)
	go poller.processIssueAsync(ctx, issue)
	poller.activeWg.Wait()

	if got := atomic.LoadInt32(&prCallbackCalled); got != 0 {
		t.Errorf("OnPRCreated called %d times on failure, want 0", got)
	}
}
