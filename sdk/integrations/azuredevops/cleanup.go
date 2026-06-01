package azuredevops

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// Cleaner handles automatic cleanup of stale pilot-in-progress tags.
// When Pilot crashes or is killed, tags remain on work items. This cleaner
// periodically checks for such orphaned tags and removes them.
type Cleaner struct {
	client        *Client
	lister        core.ActiveExecutionLister
	interval      time.Duration
	threshold     time.Duration
	logger        *slog.Logger
	workItemTypes []string

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
}

// CleanerOption configures a Cleaner.
type CleanerOption func(*Cleaner)

// WithCleanerLogger sets the logger for the cleaner.
func WithCleanerLogger(logger *slog.Logger) CleanerOption {
	return func(c *Cleaner) { c.logger = logger }
}

// WithCleanerWorkItemTypes sets the work item types to clean.
func WithCleanerWorkItemTypes(types []string) CleanerOption {
	return func(c *Cleaner) { c.workItemTypes = types }
}

// NewCleaner creates a new stale tag cleaner.
// lister is used to cross-reference active executions so live work items are never cleaned.
func NewCleaner(client *Client, lister core.ActiveExecutionLister, config *StaleLabelCleanupConfig, opts ...CleanerOption) *Cleaner {
	interval := config.Interval
	if interval == 0 {
		interval = 30 * time.Minute
	}

	threshold := config.Threshold
	if threshold == 0 {
		threshold = 1 * time.Hour
	}

	c := &Cleaner{
		client:        client,
		lister:        lister,
		interval:      interval,
		threshold:     threshold,
		logger:        slog.Default(),
		stopCh:        make(chan struct{}),
		workItemTypes: []string{"Bug", "Task", "User Story"},
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Start begins the periodic cleanup loop.
// It runs in the background and can be stopped with Stop().
func (c *Cleaner) Start(ctx context.Context) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.stopCh = make(chan struct{})
	c.mu.Unlock()

	c.logger.Info("Starting stale tag cleaner",
		slog.Duration("interval", c.interval),
		slog.Duration("threshold", c.threshold),
	)

	if err := c.Cleanup(ctx); err != nil {
		c.logger.Warn("Initial cleanup failed", slog.Any("error", err))
	}

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("Stale tag cleaner stopped (context cancelled)")
			return
		case <-c.stopCh:
			c.logger.Info("Stale tag cleaner stopped")
			return
		case <-ticker.C:
			if err := c.Cleanup(ctx); err != nil {
				c.logger.Warn("Cleanup failed", slog.Any("error", err))
			}
		}
	}
}

// Stop stops the periodic cleanup loop.
func (c *Cleaner) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		close(c.stopCh)
		c.running = false
	}
}

// Cleanup performs a single cleanup pass:
// 1. Lists all work items with pilot-in-progress tag.
// 2. Cross-references with active executions via the lister.
// 3. Removes tag from work items with no matching active execution older than threshold.
func (c *Cleaner) Cleanup(ctx context.Context) error {
	c.logger.Debug("Running stale tag cleanup")

	workItems, err := c.client.ListWorkItems(ctx, &ListWorkItemsOptions{
		Tags:          []string{TagInProgress},
		States:        []string{StateNew, StateActive},
		WorkItemTypes: c.workItemTypes,
	})
	if err != nil {
		return fmt.Errorf("failed to list work items with in-progress tag: %w", err)
	}

	if len(workItems) == 0 {
		c.logger.Debug("No work items with in-progress tag found")
		return nil
	}

	c.logger.Debug("Found work items with in-progress tag", slog.Int("count", len(workItems)))

	ids, err := c.lister.ListActiveTaskIDs(ctx)
	if err != nil {
		return fmt.Errorf("failed to get active executions: %w", err)
	}

	activeTaskIDs := make(map[string]bool)
	for _, id := range ids {
		activeTaskIDs[id] = true
	}

	c.logger.Debug("Active executions found", slog.Int("count", len(ids)))

	cleanedCount := 0
	for _, wi := range workItems {
		// Task IDs are formatted as "AZDO-<id>" for Azure DevOps.
		taskID := fmt.Sprintf("AZDO-%d", wi.ID)
		if activeTaskIDs[taskID] {
			c.logger.Debug("Work item has active execution, skipping",
				slog.Int("id", wi.ID),
				slog.String("task_id", taskID),
			)
			continue
		}

		if time.Since(wi.GetChangedDate()) < c.threshold {
			c.logger.Debug("Work item recently updated, skipping",
				slog.Int("id", wi.ID),
				slog.Duration("age", time.Since(wi.GetChangedDate())),
			)
			continue
		}

		c.logger.Info("Removing stale in-progress tag",
			slog.Int("id", wi.ID),
			slog.String("title", wi.GetTitle()),
			slog.Duration("age", time.Since(wi.GetChangedDate())),
		)

		if err := c.client.RemoveWorkItemTag(ctx, wi.ID, TagInProgress); err != nil {
			c.logger.Warn("Failed to remove stale tag",
				slog.Int("id", wi.ID),
				slog.Any("error", err),
			)
			continue
		}

		comment := "🧹 **Pilot cleanup**: Removed stale `pilot-in-progress` tag.\n\n" +
			"This work item was marked as in-progress but no active Pilot execution was found. " +
			"This can happen if Pilot was interrupted or crashed. The work item is now available for processing again."

		if _, err := c.client.AddWorkItemComment(ctx, wi.ID, comment); err != nil {
			c.logger.Warn("Failed to add cleanup comment",
				slog.Int("id", wi.ID),
				slog.Any("error", err),
			)
		}

		cleanedCount++
	}

	if cleanedCount > 0 {
		c.logger.Info("Stale tag cleanup completed",
			slog.Int("cleaned", cleanedCount),
			slog.Int("total_checked", len(workItems)),
		)
	}

	return nil
}

// CleanupStaleTags performs a single cleanup without starting the periodic loop.
func (c *Cleaner) CleanupStaleTags(ctx context.Context) error {
	return c.Cleanup(ctx)
}
