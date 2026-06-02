package asana

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func TestNewPoller(t *testing.T) {
	client := NewClient(testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{
		TriggerLabel: "pilot",
	}
	poller := NewPoller(client, config, 30*time.Second)

	if poller.config.TriggerLabel != "pilot" {
		t.Errorf("expected TriggerLabel 'pilot', got '%s'", poller.config.TriggerLabel)
	}

	if poller.interval != 30*time.Second {
		t.Errorf("expected interval 30s, got %v", poller.interval)
	}

	if len(poller.processed) != 0 {
		t.Error("expected empty processed map")
	}
}

func TestPollerWithOptions(t *testing.T) {
	client := NewClient(testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{TriggerLabel: "pilot"}

	var callbackCalled bool
	handler := func(ctx context.Context, task *Task) (*TaskResult, error) {
		callbackCalled = true
		return &TaskResult{Success: true}, nil
	}

	poller := NewPoller(client, config, 30*time.Second,
		WithOnAsanaTask(handler),
		WithMaxConcurrent(3),
	)

	if poller.onTask == nil {
		t.Error("expected onTask handler to be set")
	}

	if poller.maxConcurrent != 3 {
		t.Errorf("expected maxConcurrent 3, got %d", poller.maxConcurrent)
	}

	if cap(poller.semaphore) != 3 {
		t.Errorf("expected semaphore capacity 3, got %d", cap(poller.semaphore))
	}

	_, _ = poller.onTask(context.Background(), &Task{})
	if !callbackCalled {
		t.Error("expected callback to be called")
	}
}

func TestPollerMarkProcessed(t *testing.T) {
	client := NewClient(testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{TriggerLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second)

	if poller.IsProcessed("123456") {
		t.Error("expected 123456 NOT to be processed initially")
	}

	poller.markProcessed("123456")

	if !poller.IsProcessed("123456") {
		t.Error("expected 123456 to be processed after marking")
	}

	if poller.ProcessedCount() != 1 {
		t.Errorf("expected processed count 1, got %d", poller.ProcessedCount())
	}

	poller.Reset()

	if poller.IsProcessed("123456") {
		t.Error("expected 123456 NOT to be processed after reset")
	}

	if poller.ProcessedCount() != 0 {
		t.Errorf("expected processed count 0 after reset, got %d", poller.ProcessedCount())
	}
}

func TestPollerClearProcessed(t *testing.T) {
	client := NewClient(testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{TriggerLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second)

	poller.markProcessed("123456")
	poller.markProcessed("789012")

	if poller.ProcessedCount() != 2 {
		t.Errorf("expected processed count 2, got %d", poller.ProcessedCount())
	}

	poller.ClearProcessed("123456")

	if poller.IsProcessed("123456") {
		t.Error("expected 123456 NOT to be processed after clearing")
	}
	if !poller.IsProcessed("789012") {
		t.Error("expected 789012 to still be processed")
	}
	if poller.ProcessedCount() != 1 {
		t.Errorf("expected processed count 1 after clearing one, got %d", poller.ProcessedCount())
	}
}

func TestPollerConcurrentAccess(t *testing.T) {
	client := NewClient(testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{TriggerLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			gid := string(rune('0' + n%10))
			poller.markProcessed(gid)
			_ = poller.IsProcessed(gid)
			_ = poller.ProcessedCount()
		}(i)
	}
	wg.Wait()

	count := poller.ProcessedCount()
	if count == 0 {
		t.Error("expected some processed items")
	}
}

func TestPollerHasTag(t *testing.T) {
	client := NewClient(testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{TriggerLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second)

	tests := []struct {
		name    string
		tags    []Tag
		tagName string
		want    bool
	}{
		{"no tags", []Tag{}, "pilot", false},
		{"exact match", []Tag{{Name: "pilot"}}, "pilot", true},
		{"case insensitive", []Tag{{Name: "PILOT"}}, "pilot", true},
		{"not found", []Tag{{Name: "other"}}, "pilot", false},
		{"multiple tags", []Tag{{Name: "pilot"}, {Name: "high-priority"}}, "pilot", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &Task{Tags: tt.tags}
			got := poller.hasTag(task, tt.tagName)
			if got != tt.want {
				t.Errorf("hasTag() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPollerHasStatusTag(t *testing.T) {
	client := NewClient(testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{TriggerLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second)

	tests := []struct {
		name string
		tags []Tag
		want bool
	}{
		{"no tags", []Tag{}, false},
		{"pilot only", []Tag{{Name: "pilot"}}, false},
		{"in-progress", []Tag{{Name: "pilot"}, {Name: "pilot-in-progress"}}, true},
		{"done", []Tag{{Name: "pilot"}, {Name: "pilot-done"}}, true},
		{"failed", []Tag{{Name: "pilot"}, {Name: "pilot-failed"}}, true},
		{"case insensitive", []Tag{{Name: "PILOT-IN-PROGRESS"}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &Task{Tags: tt.tags}
			got := poller.hasStatusTag(task)
			if got != tt.want {
				t.Errorf("hasStatusTag() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPollerCacheTagGIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/workspaces/"+testutil.FakeAsanaWorkspaceID+"/tags" {
			resp := PagedResponse[Tag]{
				Data: []Tag{
					{GID: "tag-1", Name: "pilot"},
					{GID: "tag-2", Name: "pilot-in-progress"},
					{GID: "tag-3", Name: "pilot-done"},
					{GID: "tag-4", Name: "pilot-failed"},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{TriggerLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second)

	ctx := context.Background()
	err := poller.cacheTagGIDs(ctx)
	if err != nil {
		t.Fatalf("cacheTagGIDs() failed: %v", err)
	}

	if poller.pilotTagGID != "tag-1" {
		t.Errorf("expected pilotTagGID 'tag-1', got '%s'", poller.pilotTagGID)
	}
	if poller.inProgressTagGID != "tag-2" {
		t.Errorf("expected inProgressTagGID 'tag-2', got '%s'", poller.inProgressTagGID)
	}
	if poller.doneTagGID != "tag-3" {
		t.Errorf("expected doneTagGID 'tag-3', got '%s'", poller.doneTagGID)
	}
	if poller.failedTagGID != "tag-4" {
		t.Errorf("expected failedTagGID 'tag-4', got '%s'", poller.failedTagGID)
	}
}

func TestPollerCacheTagGIDs_MissingPilotTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/workspaces/"+testutil.FakeAsanaWorkspaceID+"/tags" {
			resp := PagedResponse[Tag]{
				Data: []Tag{
					{GID: "tag-1", Name: "other-tag"},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{TriggerLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second)

	ctx := context.Background()
	err := poller.cacheTagGIDs(ctx)
	if err == nil {
		t.Error("expected error when pilot tag is not found")
	}
}

func TestPollerCheckForNewTasks(t *testing.T) {
	var processedTask *Task

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/workspaces/"+testutil.FakeAsanaWorkspaceID+"/tags" {
			resp := PagedResponse[Tag]{
				Data: []Tag{
					{GID: "tag-pilot", Name: "pilot"},
					{GID: "tag-ip", Name: "pilot-in-progress"},
					{GID: "tag-done", Name: "pilot-done"},
					{GID: "tag-failed", Name: "pilot-failed"},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if r.URL.Path == "/tags/tag-pilot/tasks" {
			resp := PagedResponse[Task]{
				Data: []Task{
					{
						GID:       "task-1",
						Name:      "First task",
						Notes:     "Test description",
						Completed: false,
						Tags:      []Tag{{Name: "pilot"}},
						CreatedAt: time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
					},
					{
						GID:       "task-2",
						Name:      "Second task (in progress)",
						Notes:     "Already being worked on",
						Completed: false,
						Tags:      []Tag{{Name: "pilot"}, {Name: "pilot-in-progress"}},
						CreatedAt: time.Date(2024, 1, 2, 10, 0, 0, 0, time.UTC),
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusOK)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{TriggerLabel: "pilot"}
	poller := NewPoller(client, config, 30*time.Second,
		WithOnAsanaTask(func(ctx context.Context, task *Task) (*TaskResult, error) {
			processedTask = task
			return &TaskResult{Success: true}, nil
		}),
	)

	ctx := context.Background()

	if err := poller.cacheTagGIDs(ctx); err != nil {
		t.Fatalf("cacheTagGIDs() failed: %v", err)
	}

	poller.checkForNewTasks(ctx)
	poller.WaitForActive()

	if processedTask == nil {
		t.Fatal("expected a task to be processed")
	}

	if processedTask.GID != "task-1" {
		t.Errorf("expected task-1 to be processed, got %s", processedTask.GID)
	}

	if !poller.IsProcessed("task-1") {
		t.Error("expected task-1 to be marked as processed")
	}
}

func TestPollerCheckForNewTasks_SkipsAlreadyProcessed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/workspaces/"+testutil.FakeAsanaWorkspaceID+"/tags" {
			resp := PagedResponse[Tag]{
				Data: []Tag{{GID: "tag-pilot", Name: "pilot"}},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if r.URL.Path == "/tags/tag-pilot/tasks" {
			resp := PagedResponse[Task]{
				Data: []Task{
					{
						GID:       "task-1",
						Name:      "Already processed",
						Completed: false,
						Tags:      []Tag{{Name: "pilot"}},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{TriggerLabel: "pilot"}

	var callCount int
	poller := NewPoller(client, config, 30*time.Second,
		WithOnAsanaTask(func(ctx context.Context, task *Task) (*TaskResult, error) {
			callCount++
			return &TaskResult{Success: true}, nil
		}),
	)

	ctx := context.Background()

	if err := poller.cacheTagGIDs(ctx); err != nil {
		t.Fatalf("cacheTagGIDs() failed: %v", err)
	}

	poller.markProcessed("task-1")
	poller.checkForNewTasks(ctx)

	if callCount != 0 {
		t.Errorf("expected callback not to be called for already processed task, got %d calls", callCount)
	}
}

func TestPollerStart_CancelsOnContextDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/workspaces/"+testutil.FakeAsanaWorkspaceID+"/tags" {
			resp := PagedResponse[Tag]{
				Data: []Tag{{GID: "tag-pilot", Name: "pilot"}},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		resp := PagedResponse[Task]{Data: []Task{}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
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

func TestGetActiveTasksByTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		resp := PagedResponse[Task]{
			Data: []Task{
				{GID: "task-1", Name: "Active task", Completed: false},
				{GID: "task-2", Name: "Completed task", Completed: true},
				{GID: "task-3", Name: "Another active", Completed: false},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)

	ctx := context.Background()
	tasks, err := client.GetActiveTasksByTag(ctx, "tag-123")
	if err != nil {
		t.Fatalf("GetActiveTasksByTag() failed: %v", err)
	}

	if len(tasks) != 2 {
		t.Errorf("expected 2 active tasks, got %d", len(tasks))
	}

	for _, task := range tasks {
		if task.Completed {
			t.Errorf("found completed task %s in results", task.GID)
		}
	}
}

func TestPollerWithProcessedStore(t *testing.T) {
	client := NewClient(testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{TriggerLabel: "pilot"}
	store := newMockProcessedStore()

	_ = store.Mark("asana", "", "task-1")
	_ = store.Mark("asana", "", "task-2")

	poller := NewPoller(client, config, 30*time.Second,
		WithProcessedStore(store),
	)

	if !poller.IsProcessed("task-1") {
		t.Error("expected task-1 to be loaded from store")
	}
	if !poller.IsProcessed("task-2") {
		t.Error("expected task-2 to be loaded from store")
	}
	if poller.ProcessedCount() != 2 {
		t.Errorf("expected 2 processed tasks, got %d", poller.ProcessedCount())
	}
}

func TestPollerMaxConcurrentDefaults(t *testing.T) {
	client := NewClient(testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{TriggerLabel: "pilot"}

	poller := NewPoller(client, config, 30*time.Second)
	if poller.maxConcurrent != 2 {
		t.Errorf("expected default maxConcurrent 2, got %d", poller.maxConcurrent)
	}

	poller = NewPoller(client, config, 30*time.Second,
		WithMaxConcurrent(0),
	)
	if poller.maxConcurrent != 1 {
		t.Errorf("expected corrected maxConcurrent 1, got %d", poller.maxConcurrent)
	}
}

func TestPollerDrainAndWaitForActive(t *testing.T) {
	client := NewClient(testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{TriggerLabel: "pilot"}

	taskProcessed := make(chan struct{}, 1)
	poller := NewPoller(client, config, 30*time.Second,
		WithOnAsanaTask(func(ctx context.Context, task *Task) (*TaskResult, error) {
			time.Sleep(50 * time.Millisecond)
			taskProcessed <- struct{}{}
			return &TaskResult{Success: true}, nil
		}),
		WithMaxConcurrent(1),
	)

	go poller.processTaskAsync(context.Background(), &Task{GID: "test-task"})

	done := make(chan struct{})
	go func() {
		poller.WaitForActive()
		close(done)
	}()

	select {
	case <-taskProcessed:
	case <-time.After(200 * time.Millisecond):
		t.Error("task should have been processed")
	}

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Error("WaitForActive should have returned")
	}

	poller.stopping.Store(false)

	go poller.processTaskAsync(context.Background(), &Task{GID: "test-task-2"})

	drainDone := make(chan struct{})
	go func() {
		poller.Drain()
		close(drainDone)
	}()

	select {
	case <-drainDone:
	case <-time.After(200 * time.Millisecond):
		t.Error("Drain should have returned")
	}
}

func TestPollerClearProcessedWithStore(t *testing.T) {
	client := NewClient(testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{TriggerLabel: "pilot"}
	store := newMockProcessedStore()

	poller := NewPoller(client, config, 30*time.Second,
		WithProcessedStore(store),
	)

	poller.markProcessed("task-1")

	if !poller.IsProcessed("task-1") {
		t.Error("expected task-1 to be processed in memory")
	}
	processed, _ := store.IsProcessed("asana", "", "task-1")
	if !processed {
		t.Error("expected task-1 to be processed in store")
	}

	poller.ClearProcessed("task-1")

	if poller.IsProcessed("task-1") {
		t.Error("expected task-1 to be cleared from memory")
	}
	processed, _ = store.IsProcessed("asana", "", "task-1")
	if processed {
		t.Error("expected task-1 to be cleared from store")
	}
}

func TestPollerParallelExecution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/workspaces/"+testutil.FakeAsanaWorkspaceID+"/tags" {
			resp := PagedResponse[Tag]{
				Data: []Tag{{GID: "tag-pilot", Name: "pilot"}},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		if r.URL.Path == "/tags/tag-pilot/tasks" {
			resp := PagedResponse[Task]{
				Data: []Task{
					{
						GID:       "task-1",
						Name:      "Parallel task 1",
						Completed: false,
						Tags:      []Tag{{Name: "pilot"}},
						CreatedAt: time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
					},
					{
						GID:       "task-2",
						Name:      "Parallel task 2",
						Completed: false,
						Tags:      []Tag{{Name: "pilot"}},
						CreatedAt: time.Date(2024, 1, 2, 10, 0, 0, 0, time.UTC),
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.URL, testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{TriggerLabel: "pilot"}

	var processedTasks []string
	var mu sync.Mutex

	poller := NewPoller(client, config, 30*time.Second,
		WithOnAsanaTask(func(ctx context.Context, task *Task) (*TaskResult, error) {
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			processedTasks = append(processedTasks, task.GID)
			mu.Unlock()
			return &TaskResult{Success: true}, nil
		}),
		WithMaxConcurrent(2),
	)

	ctx := context.Background()

	if err := poller.cacheTagGIDs(ctx); err != nil {
		t.Fatalf("cacheTagGIDs() failed: %v", err)
	}

	poller.checkForNewTasks(ctx)
	poller.WaitForActive()

	mu.Lock()
	defer mu.Unlock()

	if len(processedTasks) != 2 {
		t.Errorf("expected 2 processed tasks, got %d", len(processedTasks))
	}

	expectedTasks := map[string]bool{"task-1": false, "task-2": false}
	for _, gid := range processedTasks {
		if _, exists := expectedTasks[gid]; !exists {
			t.Errorf("unexpected processed task: %s", gid)
		}
		expectedTasks[gid] = true
	}

	for gid, processed := range expectedTasks {
		if !processed {
			t.Errorf("task %s was not processed", gid)
		}
	}
}

// TestPollerSanitizeCalledInLivePath verifies that sanitizeTaskInPlace is called
// before the task handler receives the task — ASCII smuggling guard.
func TestPollerSanitizeCalledInLivePath(t *testing.T) {
	// zero-width space (U+200B) injected between "clean" and "title"
	const smuggled = "clean​title"
	const want = "cleantitle"

	client := NewClient(testutil.FakeAsanaToken, testutil.FakeAsanaWorkspaceID)
	config := &Config{TriggerLabel: "pilot"}

	var gotName string
	var mu sync.Mutex
	poller := NewPoller(client, config, 30*time.Second,
		WithOnAsanaTask(func(ctx context.Context, task *Task) (*TaskResult, error) {
			mu.Lock()
			gotName = task.Name
			mu.Unlock()
			return &TaskResult{Success: true}, nil
		}),
	)

	// Pre-acquire semaphore slot and increment WaitGroup as checkForNewTasks would.
	poller.semaphore <- struct{}{}
	poller.wgMu.Lock()
	poller.activeWg.Add(1)
	poller.wgMu.Unlock()

	task := &Task{GID: "gid-1", Name: smuggled, Notes: "notes"}
	go poller.processTaskAsync(context.Background(), task)

	poller.WaitForActive()

	mu.Lock()
	defer mu.Unlock()

	if gotName != want {
		t.Errorf("sanitize not applied: got %q, want %q", gotName, want)
	}
}

// mockProcessedStore implements core.ProcessedStore for testing.
type mockProcessedStore struct {
	processed map[string]bool
	mu        sync.RWMutex
}

func newMockProcessedStore() *mockProcessedStore {
	return &mockProcessedStore{
		processed: make(map[string]bool),
	}
}

func (m *mockProcessedStore) Mark(source, repo, issueID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.processed[issueID] = true
	return nil
}

func (m *mockProcessedStore) Unmark(source, repo, issueID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.processed, issueID)
	return nil
}

func (m *mockProcessedStore) IsProcessed(source, repo, issueID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.processed[issueID], nil
}

func (m *mockProcessedStore) Load(source, repo string) (map[string]time.Time, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]time.Time)
	for k := range m.processed {
		result[k] = time.Time{}
	}
	return result, nil
}
