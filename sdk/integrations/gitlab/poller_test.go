package gitlab

import (
	"strconv"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func TestExtractMRNumber(t *testing.T) {
	tests := []struct {
		name    string
		mrURL   string
		want    int
		wantErr bool
	}{
		{
			name:    "standard GitLab MR URL",
			mrURL:   "https://gitlab.com/namespace/project/-/merge_requests/123",
			want:    123,
			wantErr: false,
		},
		{
			name:    "GitLab MR URL without dash",
			mrURL:   "https://gitlab.com/namespace/project/merge_requests/456",
			want:    456,
			wantErr: false,
		},
		{
			name:    "self-hosted GitLab",
			mrURL:   "https://gitlab.example.com/org/repo/-/merge_requests/789",
			want:    789,
			wantErr: false,
		},
		{
			name:    "nested group",
			mrURL:   "https://gitlab.com/org/subgroup/project/-/merge_requests/42",
			want:    42,
			wantErr: false,
		},
		{
			name:    "empty URL",
			mrURL:   "",
			want:    0,
			wantErr: true,
		},
		{
			name:    "invalid URL - no MR number",
			mrURL:   "https://gitlab.com/namespace/project/-/issues/123",
			want:    0,
			wantErr: true,
		},
		{
			name:    "invalid URL - random string",
			mrURL:   "not-a-url",
			want:    0,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractMRNumber(tt.mrURL)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractMRNumber() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ExtractMRNumber() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestNewPoller(t *testing.T) {
	client := NewClient(testutil.FakeGitLabToken, "namespace/project")
	label := "pilot"
	interval := 30 * time.Second

	poller := NewPoller(client, label, interval)

	if poller == nil {
		t.Fatal("NewPoller returned nil")
	}

	if poller.label != label {
		t.Errorf("poller.label = %s, want %s", poller.label, label)
	}

	if poller.interval != interval {
		t.Errorf("poller.interval = %v, want %v", poller.interval, interval)
	}

	if poller.executionMode != ExecutionModeParallel {
		t.Errorf("poller.executionMode = %v, want %v", poller.executionMode, ExecutionModeParallel)
	}
}

func TestNewPollerWithOptions(t *testing.T) {
	client := NewClient(testutil.FakeGitLabToken, "namespace/project")
	label := "pilot"
	interval := 30 * time.Second

	poller := NewPoller(client, label, interval,
		WithExecutionMode(ExecutionModeSequential),
		WithSequentialConfig(true, 30*time.Second, 1*time.Hour),
	)

	if poller.executionMode != ExecutionModeSequential {
		t.Errorf("poller.executionMode = %v, want %v", poller.executionMode, ExecutionModeSequential)
	}

	if !poller.waitForMerge {
		t.Error("poller.waitForMerge = false, want true")
	}

	if poller.mrPollInterval != 30*time.Second {
		t.Errorf("poller.mrPollInterval = %v, want 30s", poller.mrPollInterval)
	}

	if poller.mrTimeout != 1*time.Hour {
		t.Errorf("poller.mrTimeout = %v, want 1h", poller.mrTimeout)
	}

	if poller.mergeWaiter == nil {
		t.Error("poller.mergeWaiter is nil, expected non-nil for sequential mode")
	}
}

func TestPoller_IsProcessed(t *testing.T) {
	client := NewClient(testutil.FakeGitLabToken, "namespace/project")
	poller := NewPoller(client, "pilot", 30*time.Second)

	if poller.IsProcessed(42) {
		t.Error("expected issue 42 to not be processed initially")
	}

	poller.markProcessed(42)

	if !poller.IsProcessed(42) {
		t.Error("expected issue 42 to be processed after marking")
	}

	if poller.IsProcessed(43) {
		t.Error("expected issue 43 to not be processed")
	}
}

func TestPoller_ProcessedCount(t *testing.T) {
	client := NewClient(testutil.FakeGitLabToken, "namespace/project")
	poller := NewPoller(client, "pilot", 30*time.Second)

	if poller.ProcessedCount() != 0 {
		t.Errorf("ProcessedCount() = %d, want 0", poller.ProcessedCount())
	}

	poller.markProcessed(1)
	poller.markProcessed(2)
	poller.markProcessed(3)

	if poller.ProcessedCount() != 3 {
		t.Errorf("ProcessedCount() = %d, want 3", poller.ProcessedCount())
	}
}

func TestPoller_Reset(t *testing.T) {
	client := NewClient(testutil.FakeGitLabToken, "namespace/project")
	poller := NewPoller(client, "pilot", 30*time.Second)

	poller.markProcessed(1)
	poller.markProcessed(2)

	if poller.ProcessedCount() != 2 {
		t.Errorf("ProcessedCount() before reset = %d, want 2", poller.ProcessedCount())
	}

	poller.Reset()

	if poller.ProcessedCount() != 0 {
		t.Errorf("ProcessedCount() after reset = %d, want 0", poller.ProcessedCount())
	}

	if poller.IsProcessed(1) {
		t.Error("expected issue 1 to not be processed after reset")
	}
}

func TestExecutionModeConstants(t *testing.T) {
	if ExecutionModeSequential != "sequential" {
		t.Errorf("ExecutionModeSequential = %s, want 'sequential'", ExecutionModeSequential)
	}
	if ExecutionModeParallel != "parallel" {
		t.Errorf("ExecutionModeParallel = %s, want 'parallel'", ExecutionModeParallel)
	}
}

func TestNewPollerWithMaxConcurrent(t *testing.T) {
	client := NewClient(testutil.FakeGitLabToken, "namespace/project")

	poller := NewPoller(client, "pilot", 30*time.Second)
	if poller.maxConcurrent != 2 {
		t.Errorf("default maxConcurrent = %d, want 2", poller.maxConcurrent)
	}

	poller = NewPoller(client, "pilot", 30*time.Second, WithMaxConcurrent(5))
	if poller.maxConcurrent != 5 {
		t.Errorf("custom maxConcurrent = %d, want 5", poller.maxConcurrent)
	}

	if cap(poller.semaphore) != 5 {
		t.Errorf("semaphore capacity = %d, want 5", cap(poller.semaphore))
	}

	poller = NewPoller(client, "pilot", 30*time.Second, WithMaxConcurrent(0))
	if poller.maxConcurrent != 1 {
		t.Errorf("zero maxConcurrent should become 1, got %d", poller.maxConcurrent)
	}

	poller = NewPoller(client, "pilot", 30*time.Second, WithMaxConcurrent(-1))
	if poller.maxConcurrent != 1 {
		t.Errorf("negative maxConcurrent should become 1, got %d", poller.maxConcurrent)
	}
}

func TestPoller_ClearProcessed(t *testing.T) {
	client := NewClient(testutil.FakeGitLabToken, "namespace/project")
	poller := NewPoller(client, "pilot", 30*time.Second)

	poller.markProcessed(42)
	if !poller.IsProcessed(42) {
		t.Error("expected issue 42 to be processed after marking")
	}

	poller.ClearProcessed(42)
	if poller.IsProcessed(42) {
		t.Error("expected issue 42 to not be processed after clearing")
	}

	// Clearing a non-existent ID should not panic.
	poller.ClearProcessed(999)
}

func TestPoller_DrainAndWaitForActive(t *testing.T) {
	client := NewClient(testutil.FakeGitLabToken, "namespace/project")
	poller := NewPoller(client, "pilot", 30*time.Second, WithMaxConcurrent(2))

	poller.WaitForActive()
	if !poller.stopping.Load() {
		t.Error("expected stopping flag to be true after WaitForActive")
	}

	poller.stopping.Store(false)

	poller.Drain()
	if !poller.stopping.Load() {
		t.Error("expected stopping flag to be true after Drain")
	}
}

func TestPoller_hasStatusLabel(t *testing.T) {
	client := NewClient(testutil.FakeGitLabToken, "namespace/project")
	poller := NewPoller(client, "pilot", 30*time.Second)

	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{
			name:   "no status labels",
			labels: []string{"pilot", "bug"},
			want:   false,
		},
		{
			name:   "in-progress label",
			labels: []string{"pilot", LabelInProgress},
			want:   true,
		},
		{
			name:   "done label",
			labels: []string{"pilot", LabelDone},
			want:   true,
		},
		{
			name:   "failed label",
			labels: []string{"pilot", LabelFailed},
			want:   true,
		},
		{
			name:   "empty labels",
			labels: []string{},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &Issue{
				IID:    1,
				Labels: tt.labels,
			}
			got := poller.hasStatusLabel(issue)
			if got != tt.want {
				t.Errorf("hasStatusLabel() = %v, want %v", got, tt.want)
			}
		})
	}
}

// mockProcessedStore implements core.ProcessedStore for testing.
type mockProcessedStore struct {
	processed map[int]bool
}

func newMockProcessedStore() *mockProcessedStore {
	return &mockProcessedStore{processed: make(map[int]bool)}
}

func (m *mockProcessedStore) Mark(source, repo, issueID string) error {
	if id, err := strconv.Atoi(issueID); err == nil {
		m.processed[id] = true
	}
	return nil
}

func (m *mockProcessedStore) Unmark(source, repo, issueID string) error {
	if id, err := strconv.Atoi(issueID); err == nil {
		delete(m.processed, id)
	}
	return nil
}

func (m *mockProcessedStore) IsProcessed(source, repo, issueID string) (bool, error) {
	if id, err := strconv.Atoi(issueID); err == nil {
		return m.processed[id], nil
	}
	return false, nil
}

func (m *mockProcessedStore) Load(source, repo string) (map[string]time.Time, error) {
	result := make(map[string]time.Time)
	for k := range m.processed {
		result[strconv.Itoa(k)] = time.Now()
	}
	return result, nil
}

func TestPollerWithProcessedStore(t *testing.T) {
	client := NewClient(testutil.FakeGitLabToken, "namespace/project")
	store := newMockProcessedStore()

	store.processed[100] = true
	store.processed[200] = true

	poller := NewPoller(client, "pilot", 30*time.Second, WithProcessedStore(store))

	if poller.ProcessedCount() != 2 {
		t.Errorf("expected 2 processed issues loaded, got %d", poller.ProcessedCount())
	}

	if !poller.IsProcessed(100) {
		t.Error("expected issue 100 to be processed (loaded from store)")
	}

	if !poller.IsProcessed(200) {
		t.Error("expected issue 200 to be processed (loaded from store)")
	}

	poller.markProcessed(300)

	if !store.processed[300] {
		t.Error("expected issue 300 to be persisted to store")
	}

	poller.ClearProcessed(100)

	if store.processed[100] {
		t.Error("expected issue 100 to be removed from store")
	}
}
