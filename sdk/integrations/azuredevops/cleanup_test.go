package azuredevops

import (
	"context"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// fakeActiveLister implements core.ActiveExecutionLister for testing.
type fakeActiveLister struct {
	ids []string
	err error
}

func (f *fakeActiveLister) ListActiveTaskIDs(_ context.Context) ([]string, error) {
	return f.ids, f.err
}

func TestNewCleaner(t *testing.T) {
	client := NewClient(testutil.FakeAzureDevOpsPAT, "org", "project")
	lister := &fakeActiveLister{}
	cfg := &StaleLabelCleanupConfig{
		Interval:  10 * time.Minute,
		Threshold: 2 * time.Hour,
	}

	c := NewCleaner(client, lister, cfg)
	if c == nil {
		t.Fatal("NewCleaner returned nil")
	}
	if c.interval != 10*time.Minute {
		t.Errorf("interval = %v, want 10m", c.interval)
	}
	if c.threshold != 2*time.Hour {
		t.Errorf("threshold = %v, want 2h", c.threshold)
	}
}

func TestNewCleaner_Defaults(t *testing.T) {
	client := NewClient(testutil.FakeAzureDevOpsPAT, "org", "project")
	lister := &fakeActiveLister{}
	cfg := &StaleLabelCleanupConfig{} // zero values → defaults

	c := NewCleaner(client, lister, cfg)
	if c.interval != 30*time.Minute {
		t.Errorf("default interval = %v, want 30m", c.interval)
	}
	if c.threshold != 1*time.Hour {
		t.Errorf("default threshold = %v, want 1h", c.threshold)
	}
}

func TestCleaner_Stop_NotRunning(t *testing.T) {
	client := NewClient(testutil.FakeAzureDevOpsPAT, "org", "project")
	lister := &fakeActiveLister{}
	cfg := &StaleLabelCleanupConfig{Interval: time.Minute, Threshold: time.Hour}

	c := NewCleaner(client, lister, cfg)
	// Stop on non-running cleaner must not panic.
	c.Stop()
}

func TestCleaner_WithCleanerLogger(t *testing.T) {
	client := NewClient(testutil.FakeAzureDevOpsPAT, "org", "project")
	lister := &fakeActiveLister{}
	cfg := &StaleLabelCleanupConfig{Interval: time.Minute, Threshold: time.Hour}

	// Verify that WithCleanerLogger option is accepted without panic.
	c := NewCleaner(client, lister, cfg, WithCleanerLogger(nil))
	if c == nil {
		t.Fatal("NewCleaner with WithCleanerLogger returned nil")
	}
}

func TestCleaner_WithCleanerWorkItemTypes(t *testing.T) {
	client := NewClient(testutil.FakeAzureDevOpsPAT, "org", "project")
	lister := &fakeActiveLister{}
	cfg := &StaleLabelCleanupConfig{Interval: time.Minute, Threshold: time.Hour}

	types := []string{"Bug", "Feature"}
	c := NewCleaner(client, lister, cfg, WithCleanerWorkItemTypes(types))
	if len(c.workItemTypes) != 2 {
		t.Errorf("expected 2 work item types, got %d", len(c.workItemTypes))
	}
}
