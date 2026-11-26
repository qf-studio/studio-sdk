package plane

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// Status labels for tracking work item progress.
const (
	LabelPilot      = "pilot"
	LabelInProgress = "pilot-in-progress"
	LabelDone       = "pilot-done"
	LabelFailed     = "pilot-failed"
)

// labelSet holds the UUIDs of the pilot-related labels of one project.
type labelSet struct {
	pilot      string
	inProgress string
	done       string
	failed     string
}

// labelLookupFailure is a kind of label lookup failure of a project.
type labelLookupFailure int

const (
	labelListFailed labelLookupFailure = iota
	pilotLabelNotFound
)

// labelLookupKey keys the label lookup failures by project and kind.
type labelLookupKey struct {
	projectID string
	kind      labelLookupFailure
}

// Poller polls Plane.so for work items with the pilot label.
type Poller struct {
	client   *Client
	config   *Config
	interval time.Duration

	processed map[string]bool // Work item UUID → processed
	mu        sync.RWMutex

	onIssue     func(ctx context.Context, issue *WorkItem) (*core.IssueResult, error)
	onPRCreated func(ev core.PRCreatedEvent)
	logger      *slog.Logger

	// Label UUID cache (resolved by name on startup, and at each poll for
	// projects that have no set yet). Plane labels are per-project, so the
	// same label name has another UUID in each project.
	// Maps project ID → label set; only projects with the pilot label have one.
	labelIDs map[string]labelSet
	// Project and kind of each label lookup failure seen once (later failures
	// of the same kind log at Debug).
	labelLookupFailed map[labelLookupKey]bool
	labelMu           sync.RWMutex // protects labelIDs + labelLookupFailed

	// Persistent processed store (optional)
	processedStore core.ProcessedStore

	// State UUID cache (resolved on startup by group)
	// Maps project ID → state UUID for started/completed groups.
	startedStateIDs   map[string]string
	completedStateIDs map[string]string

	// Parallel execution configuration
	maxConcurrent int
	semaphore     chan struct{}
	activeWg      sync.WaitGroup
	stopping      atomic.Bool
	wgMu          sync.Mutex // protects stopping + activeWg Add/Wait coordination
}

// PollerOption configures a Poller.
type PollerOption func(*Poller)

// WithOnIssue sets the callback for new work items.
func WithOnIssue(fn func(ctx context.Context, issue *WorkItem) (*core.IssueResult, error)) PollerOption {
	return func(p *Poller) {
		p.onIssue = fn
	}
}

// WithPollerLogger sets the logger for the poller.
func WithPollerLogger(logger *slog.Logger) PollerOption {
	return func(p *Poller) {
		p.logger = logger
	}
}

// WithProcessedStore sets the persistent store for processed work item tracking.
// On startup, processed items are loaded from the store to prevent re-processing after hot upgrade.
func WithProcessedStore(store core.ProcessedStore) PollerOption {
	return func(p *Poller) {
		p.processedStore = store
	}
}

// WithMaxConcurrent sets the maximum number of parallel work item executions.
func WithMaxConcurrent(n int) PollerOption {
	return func(p *Poller) {
		if n < 1 {
			n = 1
		}
		p.maxConcurrent = n
	}
}

// WithOnPRCreated sets the callback for when a PR is created for a work item.
func WithOnPRCreated(fn func(ev core.PRCreatedEvent)) PollerOption {
	return func(p *Poller) {
		p.onPRCreated = fn
	}
}

// NewPoller creates a new Plane work item poller.
func NewPoller(client *Client, config *Config, interval time.Duration, opts ...PollerOption) *Poller {
	p := &Poller{
		client:    client,
		config:    config,
		interval:  interval,
		processed: make(map[string]bool),
		logger:    slog.Default(),

		labelIDs:          make(map[string]labelSet),
		labelLookupFailed: make(map[labelLookupKey]bool),
	}

	for _, opt := range opts {
		opt(p)
	}

	// Load processed items from persistent store if available
	if p.processedStore != nil {
		loaded, err := p.processedStore.Load("plane", "")
		if err != nil {
			p.logger.Warn("Failed to load processed issues from store", slog.Any("error", err))
		} else if len(loaded) > 0 {
			p.mu.Lock()
			for id := range loaded {
				p.processed[id] = true
			}
			p.mu.Unlock()
			p.logger.Info("Loaded processed issues from store", slog.Int("count", len(loaded)))
		}
	}

	// Initialize parallel semaphore
	if p.maxConcurrent < 1 {
		p.maxConcurrent = 2 // default
	}
	p.semaphore = make(chan struct{}, p.maxConcurrent)

	return p
}

// Start begins polling for work items.
func (p *Poller) Start(ctx context.Context) error {
	// Cache label UUIDs on startup
	if err := p.cacheLabelIDs(ctx); err != nil {
		return fmt.Errorf("failed to cache label IDs: %w", err)
	}

	// Cache state UUIDs for state transitions
	p.cacheStateIDs(ctx)

	p.logger.Info("Starting Plane poller",
		slog.String("workspace", p.config.WorkspaceSlug),
		slog.Int("projects", len(p.config.ProjectIDs)),
		slog.Duration("interval", p.interval),
		slog.Int("max_concurrent", p.maxConcurrent),
	)

	// Recover orphaned in-progress items from previous run
	p.recoverOrphanedIssues(ctx)

	// Initial check
	p.checkForNewIssues(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("Plane poller stopping, waiting for active tasks...")
			p.wgMu.Lock()
			p.stopping.Store(true)
			p.wgMu.Unlock()
			p.activeWg.Wait()
			p.logger.Info("Plane poller stopped")
			return nil
		case <-ticker.C:
			p.checkForNewIssues(ctx)
		}
	}
}

// pilotLabelName returns the name of the label that marks a work item for pickup.
func (p *Poller) pilotLabelName() string {
	if p.config.TriggerLabel != "" {
		return p.config.TriggerLabel
	}
	return LabelPilot
}

// cacheLabelIDs fetches and caches the UUIDs for pilot-related labels of each configured project.
// Plane labels are per-project, so each project gets its own label set.
// It returns an error only when no configured project has the pilot label.
func (p *Poller) cacheLabelIDs(ctx context.Context) error {
	found := false
	for _, projectID := range p.config.ProjectIDs {
		if p.lookUpLabels(ctx, projectID) {
			found = true
		}
	}

	if !found {
		return fmt.Errorf("pilot label %q not found in any configured project", p.pilotLabelName())
	}
	return nil
}

// lookUpLabels fetches the labels of one project and stores its label set
// when the project has the pilot label. The first failure of each kind of a
// project logs a Warn; a repeat of the same kind logs at Debug. It reports whether the set was stored.
func (p *Poller) lookUpLabels(ctx context.Context, projectID string) bool {
	pilotLabelName := p.pilotLabelName()

	labels, err := p.client.ListLabels(ctx, p.config.WorkspaceSlug, projectID)
	if err != nil {
		p.logLabelLookupFailure("Failed to list labels for project",
			projectID, labelListFailed,
			slog.String("project_id", projectID),
			slog.Any("error", err),
		)
		return false
	}

	var set labelSet
	for _, label := range labels {
		switch {
		case strings.EqualFold(label.Name, pilotLabelName):
			set.pilot = label.ID
		case strings.EqualFold(label.Name, LabelInProgress):
			set.inProgress = label.ID
		case strings.EqualFold(label.Name, LabelDone):
			set.done = label.ID
		case strings.EqualFold(label.Name, LabelFailed):
			set.failed = label.ID
		}
	}

	if set.pilot == "" {
		p.logLabelLookupFailure("Pilot label not found in project; project is skipped",
			projectID, pilotLabelNotFound,
			slog.String("project_id", projectID),
			slog.String("label", pilotLabelName),
		)
		return false
	}

	p.labelMu.Lock()
	p.labelIDs[projectID] = set
	p.labelMu.Unlock()

	p.logger.Debug("Cached label IDs",
		slog.String("project_id", projectID),
		slog.String("pilot", set.pilot),
		slog.String("in_progress", set.inProgress),
		slog.String("done", set.done),
		slog.String("failed", set.failed),
	)
	return true
}

// logLabelLookupFailure logs a Warn on the first label lookup failure of each
// kind of a project and a Debug line on each repeat of that kind.
func (p *Poller) logLabelLookupFailure(msg, projectID string, kind labelLookupFailure, attrs ...any) {
	key := labelLookupKey{projectID: projectID, kind: kind}
	p.labelMu.Lock()
	repeat := p.labelLookupFailed[key]
	p.labelLookupFailed[key] = true
	p.labelMu.Unlock()

	if repeat {
		p.logger.Debug(msg, attrs...)
		return
	}
	p.logger.Warn(msg, attrs...)
}

// projectLabels returns the label set of a project, if it has one.
func (p *Poller) projectLabels(projectID string) (labelSet, bool) {
	p.labelMu.RLock()
	defer p.labelMu.RUnlock()
	set, ok := p.labelIDs[projectID]
	return set, ok
}

// cacheStateIDs fetches and caches the UUIDs for started/completed state groups per project.
// Plane states are per-project; we cache them to avoid API calls on every transition.
func (p *Poller) cacheStateIDs(ctx context.Context) {
	p.startedStateIDs = make(map[string]string)
	p.completedStateIDs = make(map[string]string)

	for _, projectID := range p.config.ProjectIDs {
		states, err := p.client.ListStates(ctx, p.config.WorkspaceSlug, projectID)
		if err != nil {
			p.logger.Warn("Failed to list states for project",
				slog.String("project_id", projectID),
				slog.Any("error", err),
			)
			continue
		}

		for _, s := range states {
			switch s.Group {
			case StateGroupStarted:
				if p.startedStateIDs[projectID] == "" {
					p.startedStateIDs[projectID] = s.ID
				}
			case StateGroupCompleted:
				if p.completedStateIDs[projectID] == "" {
					p.completedStateIDs[projectID] = s.ID
				}
			}
		}
	}

	p.logger.Debug("Cached state IDs",
		slog.Int("projects_with_started", len(p.startedStateIDs)),
		slog.Int("projects_with_completed", len(p.completedStateIDs)),
	)
}

// recoverOrphanedIssues finds work items with pilot-in-progress label from a previous run
// and removes the label so they can be picked up again.
// This handles restart/crash scenarios where items were left orphaned.
func (p *Poller) recoverOrphanedIssues(ctx context.Context) {
	for _, projectID := range p.config.ProjectIDs {
		p.recoverOrphanedIssuesOfProject(ctx, projectID)
	}
}

// recoverOrphanedIssuesOfProject removes the pilot-in-progress label from the
// work items of one project that has a label set.
func (p *Poller) recoverOrphanedIssuesOfProject(ctx context.Context, projectID string) {
	labels, ok := p.projectLabels(projectID)
	if !ok || labels.inProgress == "" {
		return
	}

	items, err := p.client.ListWorkItems(ctx, p.config.WorkspaceSlug, projectID, labels.inProgress)
	if err != nil {
		p.logger.Warn("Failed to check for orphaned issues",
			slog.String("project_id", projectID),
			slog.Any("error", err),
		)
		return
	}

	if len(items) == 0 {
		return
	}

	p.logger.Info("Recovering orphaned in-progress issues",
		slog.String("project_id", projectID),
		slog.Int("count", len(items)),
	)

	for _, item := range items {
		if err := p.client.RemoveLabel(ctx, p.config.WorkspaceSlug, projectID, item.ID, labels.inProgress); err != nil {
			p.logger.Warn("Failed to remove in-progress label from orphaned issue",
				slog.String("id", item.ID),
				slog.Any("error", err),
			)
			continue
		}
		// Also clear from processed map/store so the first poll cycle picks it up.
		p.ClearProcessed(item.ID)
		p.logger.Info("Recovered orphaned issue",
			slog.String("id", item.ID),
			slog.String("name", item.Name),
		)
	}
}

func (p *Poller) checkForNewIssues(ctx context.Context) {
	var allItems []WorkItem

	for _, projectID := range p.config.ProjectIDs {
		labels, ok := p.projectLabels(projectID)
		if !ok {
			// Label list failed at startup or the pilot label was missing: look up again.
			if !p.lookUpLabels(ctx, projectID) {
				continue
			}
			p.logger.Info("Found pilot label for project; project is now polled",
				slog.String("project_id", projectID),
			)
			labels, _ = p.projectLabels(projectID)
			// The start-up recovery did not see this project: run it now.
			p.recoverOrphanedIssuesOfProject(ctx, projectID)
		}
		// Never list without a label filter: that would return every work item.
		if labels.pilot == "" {
			continue
		}

		items, err := p.client.ListWorkItems(ctx, p.config.WorkspaceSlug, projectID, labels.pilot)
		if err != nil {
			p.logger.Warn("Failed to fetch work items",
				slog.String("project_id", projectID),
				slog.Any("error", err),
			)
			continue
		}
		// Status labels are keyed on the project: a work item with no project
		// key belongs to the project that listed it.
		for i := range items {
			if items[i].ProjectID == "" {
				items[i].ProjectID = projectID
			}
		}
		allItems = append(allItems, items...)
	}

	// Sort by creation date (oldest first)
	sort.Slice(allItems, func(i, j int) bool {
		return allItems[i].CreatedAt.Before(allItems[j].CreatedAt)
	})

	for _, item := range allItems {
		// Skip if already processed
		p.mu.RLock()
		processed := p.processed[item.ID]
		p.mu.RUnlock()

		if processed {
			continue
		}

		// Skip if has status label (in-progress, done, or failed)
		if p.hasStatusLabel(&item) {
			// Only mark as processed if it has done label (allow retry of failed)
			if labels, ok := p.projectLabels(item.ProjectID); ok && labels.done != "" && HasLabelID(&item, labels.done) {
				p.markProcessed(item.ID)
			}
			continue
		}

		// Mark processed immediately to prevent duplicate dispatch on next tick
		p.markProcessed(item.ID)

		// Acquire semaphore slot (blocks if max_concurrent reached)
		select {
		case <-ctx.Done():
			return
		case p.semaphore <- struct{}{}:
		}

		p.logger.Info("Dispatching Plane work item for parallel execution",
			slog.String("id", item.ID),
			slog.String("name", item.Name),
			slog.String("project", item.ProjectID),
		)

		// Use mutex to coordinate stopping flag check with WaitGroup Add
		p.wgMu.Lock()
		if p.stopping.Load() {
			p.wgMu.Unlock()
			<-p.semaphore // release slot we acquired
			return
		}
		p.activeWg.Add(1)
		p.wgMu.Unlock()

		go p.processIssueAsync(ctx, item)
	}
}

// processIssueAsync handles a single work item in a goroutine.
func (p *Poller) processIssueAsync(ctx context.Context, item WorkItem) {
	defer p.activeWg.Done()
	defer func() { <-p.semaphore }() // release slot

	if p.onIssue == nil {
		return
	}

	// Strip invisible Unicode from untrusted fields before downstream
	// consumers see them. See sanitize.go for the shared helper.
	sanitizeWorkItemInPlace(p.logger, &item)

	// Status labels are per-project: use the label set of the work item's project.
	labels, _ := p.projectLabels(item.ProjectID)

	// Add in-progress label
	if labels.inProgress != "" {
		_ = p.client.AddLabel(ctx, p.config.WorkspaceSlug, item.ProjectID, item.ID, labels.inProgress)
	}

	// Transition to started state on dispatch
	if stateID := p.startedStateIDs[item.ProjectID]; stateID != "" {
		if err := p.client.UpdateIssueState(ctx, p.config.WorkspaceSlug, item.ProjectID, item.ID, stateID); err != nil {
			p.logger.Warn("Failed to transition work item to started state",
				slog.String("id", item.ID),
				slog.Any("error", err),
			)
		}
	}

	result, err := p.onIssue(ctx, &item)
	if err != nil {
		p.logger.Error("Failed to process work item",
			slog.String("id", item.ID),
			slog.Any("error", err),
		)
		// Remove in-progress label, add failed label
		if labels.inProgress != "" {
			_ = p.client.RemoveLabel(ctx, p.config.WorkspaceSlug, item.ProjectID, item.ID, labels.inProgress)
		}
		if labels.failed != "" {
			_ = p.client.AddLabel(ctx, p.config.WorkspaceSlug, item.ProjectID, item.ID, labels.failed)
		}
		return
	}

	// Remove in-progress label
	if labels.inProgress != "" {
		_ = p.client.RemoveLabel(ctx, p.config.WorkspaceSlug, item.ProjectID, item.ID, labels.inProgress)
	}

	// Add done label on success
	if result != nil && result.Success && labels.done != "" {
		_ = p.client.AddLabel(ctx, p.config.WorkspaceSlug, item.ProjectID, item.ID, labels.done)
	}

	// Transition to completed state on success
	if result != nil && result.Success {
		if stateID := p.completedStateIDs[item.ProjectID]; stateID != "" {
			if err := p.client.UpdateIssueState(ctx, p.config.WorkspaceSlug, item.ProjectID, item.ID, stateID); err != nil {
				p.logger.Warn("Failed to transition work item to completed state",
					slog.String("id", item.ID),
					slog.Any("error", err),
				)
			}
		}
	}

	// Post PR URL as comment with execution metrics and dedup
	if result != nil && result.Success && result.PRNumber > 0 {
		commentHTML := fmt.Sprintf(
			`<p>✅ PR created: <a href="%s">#%d</a></p>`,
			result.PRURL, result.PRNumber,
		)
		externalID := fmt.Sprintf("pilot-pr-%d-%s", result.PRNumber, item.ID)
		if err := p.client.AddCommentWithTracking(
			ctx, p.config.WorkspaceSlug, item.ProjectID, item.ID,
			commentHTML, "pilot", externalID,
		); err != nil {
			p.logger.Warn("Failed to post PR comment on work item",
				slog.String("id", item.ID),
				slog.Int("pr_number", result.PRNumber),
				slog.Any("error", err),
			)
		}
	}

	// Fire OnPRCreated callback
	if result != nil && result.PRNumber > 0 && p.onPRCreated != nil {
		p.onPRCreated(core.PRCreatedEvent{
			PRNumber:   result.PRNumber,
			PRURL:      result.PRURL,
			IssueID:    item.ID,
			HeadSHA:    result.HeadSHA,
			BranchName: result.BranchName,
		})
	}
}

// hasStatusLabel checks if a work item has any status label UUID of its project.
func (p *Poller) hasStatusLabel(item *WorkItem) bool {
	labels, ok := p.projectLabels(item.ProjectID)
	if !ok {
		return false
	}
	if labels.inProgress != "" && HasLabelID(item, labels.inProgress) {
		return true
	}
	if labels.done != "" && HasLabelID(item, labels.done) {
		return true
	}
	if labels.failed != "" && HasLabelID(item, labels.failed) {
		return true
	}
	return false
}

func (p *Poller) markProcessed(id string) {
	p.mu.Lock()
	p.processed[id] = true
	p.mu.Unlock()

	// Persist to store if available
	if p.processedStore != nil {
		if err := p.processedStore.Mark("plane", "", id); err != nil {
			p.logger.Warn("Failed to persist processed issue", slog.String("id", id), slog.Any("error", err))
		}
	}
}

// IsProcessed checks if a work item has been processed.
func (p *Poller) IsProcessed(id string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.processed[id]
}

// ProcessedCount returns the number of processed work items.
func (p *Poller) ProcessedCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.processed)
}

// Reset clears the processed work items map.
func (p *Poller) Reset() {
	p.mu.Lock()
	p.processed = make(map[string]bool)
	p.mu.Unlock()
}

// ClearProcessed removes a specific work item from the processed map (for retry).
// Used when pilot-failed label is removed to allow retry.
func (p *Poller) ClearProcessed(id string) {
	p.mu.Lock()
	delete(p.processed, id)
	p.mu.Unlock()

	// Also clear from persistent store
	if p.processedStore != nil {
		if err := p.processedStore.Unmark("plane", "", id); err != nil {
			p.logger.Warn("Failed to unmark issue in store",
				slog.String("id", id),
				slog.Any("error", err))
		}
	}

	p.logger.Debug("Cleared issue from processed map",
		slog.String("id", id))
}

// Drain stops accepting new work items and waits for active executions to finish.
// Used during hot upgrade to let in-flight work complete before process restart.
func (p *Poller) Drain() {
	p.logger.Info("Draining poller — no new work items will be accepted")
	p.wgMu.Lock()
	p.stopping.Store(true)
	p.wgMu.Unlock()
	p.activeWg.Wait()
	p.logger.Info("Poller drained — all active tasks completed")
}

// WaitForActive waits for all active parallel goroutines to finish.
// Used in tests to synchronize after checkForNewIssues.
func (p *Poller) WaitForActive() {
	p.wgMu.Lock()
	p.stopping.Store(true)
	p.wgMu.Unlock()
	p.activeWg.Wait()
}
