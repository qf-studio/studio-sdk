package azuredevops

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/util/skipreason"
)

// ExecutionMode determines how work items are processed.
type ExecutionMode string

const (
	ExecutionModeSequential ExecutionMode = "sequential"
	ExecutionModeParallel   ExecutionMode = "parallel"
)

// WorkItemResult is returned by the work item handler with PR information.
type WorkItemResult struct {
	Success    bool
	PRNumber   int
	PRURL      string
	HeadSHA    string
	BranchName string
	Error      error
}

// Poller polls Azure DevOps for work items with a specific tag.
type Poller struct {
	client               *Client
	tag                  string
	interval             time.Duration
	processed            map[int]bool
	mu                   sync.RWMutex
	onWorkItem           func(ctx context.Context, wi *WorkItem) error
	onWorkItemWithResult func(ctx context.Context, wi *WorkItem) (*WorkItemResult, error)
	// OnPRCreated is called when a PR is created after work item processing.
	// Parameters: prID, prURL, workItemID, headSHA, branchName.
	OnPRCreated func(prID int, prURL string, workItemID int, headSHA string, branchName string)
	logger      *slog.Logger

	workItemTypes []string

	executionMode  ExecutionMode
	mergeWaiter    *MergeWaiter
	waitForMerge   bool
	prTimeout      time.Duration
	prPollInterval time.Duration

	processedStore core.ProcessedStore

	maxConcurrent int
	semaphore     chan struct{}
	activeWg      sync.WaitGroup
	stopping      atomic.Bool
	wgMu          sync.Mutex // protects stopping + activeWg Add/Wait coordination

	pollerMetrics skipreason.PollerMetricsRecorder
	repoKey       string // label value for the `repo` dimension in poller metrics
}

// PollerOption configures a Poller.
type PollerOption func(*Poller)

// WithPollerLogger sets the logger for the poller.
func WithPollerLogger(logger *slog.Logger) PollerOption {
	return func(p *Poller) { p.logger = logger }
}

// WithOnWorkItem sets the callback for new work items (parallel mode).
func WithOnWorkItem(fn func(ctx context.Context, wi *WorkItem) error) PollerOption {
	return func(p *Poller) { p.onWorkItem = fn }
}

// WithOnWorkItemWithResult sets the callback for new work items that returns PR info.
func WithOnWorkItemWithResult(fn func(ctx context.Context, wi *WorkItem) (*WorkItemResult, error)) PollerOption {
	return func(p *Poller) { p.onWorkItemWithResult = fn }
}

// WithExecutionMode sets the execution mode (sequential or parallel).
func WithExecutionMode(mode ExecutionMode) PollerOption {
	return func(p *Poller) { p.executionMode = mode }
}

// WithSequentialConfig configures sequential execution settings.
func WithSequentialConfig(waitForMerge bool, pollInterval, timeout time.Duration) PollerOption {
	return func(p *Poller) {
		p.waitForMerge = waitForMerge
		p.prPollInterval = pollInterval
		p.prTimeout = timeout
	}
}

// WithOnPRCreated sets the callback for PR creation events.
func WithOnPRCreated(fn func(prID int, prURL string, workItemID int, headSHA string, branchName string)) PollerOption {
	return func(p *Poller) { p.OnPRCreated = fn }
}

// WithWorkItemTypes sets the work item types to filter.
func WithWorkItemTypes(types []string) PollerOption {
	return func(p *Poller) { p.workItemTypes = types }
}

// WithProcessedStore sets the persistent store for processed work item tracking.
// On startup, processed work items are loaded to prevent re-processing after hot upgrade.
func WithProcessedStore(store core.ProcessedStore) PollerOption {
	return func(p *Poller) { p.processedStore = store }
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

// WithPollerMetrics sets the recorder for per-repo dispatch/skip counters.
func WithPollerMetrics(rec skipreason.PollerMetricsRecorder) PollerOption {
	return func(p *Poller) { p.pollerMetrics = rec }
}

// WithPollerRepoKey sets the `repo` label value used in poller metrics.
func WithPollerRepoKey(key string) PollerOption {
	return func(p *Poller) { p.repoKey = key }
}

// NewPoller creates a new Azure DevOps work item poller.
func NewPoller(client *Client, tag string, interval time.Duration, opts ...PollerOption) *Poller {
	p := &Poller{
		client:         client,
		tag:            tag,
		interval:       interval,
		processed:      make(map[int]bool),
		logger:         slog.Default(),
		executionMode:  ExecutionModeParallel,
		waitForMerge:   true,
		prPollInterval: 30 * time.Second,
		prTimeout:      1 * time.Hour,
		workItemTypes:  []string{"Bug", "Task", "User Story"},
	}

	for _, opt := range opts {
		opt(p)
	}

	if p.processedStore != nil {
		loaded, err := p.processedStore.Load("azuredevops", p.repoKey)
		if err != nil {
			p.logger.Warn("Failed to load processed work items from store", slog.Any("error", err))
		} else if len(loaded) > 0 {
			p.mu.Lock()
			for idStr := range loaded {
				if id, parseErr := strconv.Atoi(idStr); parseErr == nil {
					p.processed[id] = true
				}
			}
			p.mu.Unlock()
			p.logger.Info("Loaded processed work items from store", slog.Int("count", len(loaded)))
		}
	}

	if p.maxConcurrent < 1 {
		p.maxConcurrent = 2
	}
	p.semaphore = make(chan struct{}, p.maxConcurrent)

	if p.executionMode == ExecutionModeSequential && p.waitForMerge {
		p.mergeWaiter = NewMergeWaiter(client, &MergeWaiterConfig{
			PollInterval: p.prPollInterval,
			Timeout:      p.prTimeout,
		})
	}

	return p
}

// Start begins polling for work items. Implements core.Poller.
func (p *Poller) Start(ctx context.Context) error {
	p.logger.Info("Starting Azure DevOps poller",
		slog.String("tag", p.tag),
		slog.Duration("interval", p.interval),
		slog.String("mode", string(p.executionMode)),
		slog.Int("max_concurrent", p.maxConcurrent),
	)

	p.recoverOrphanedWorkItems(ctx)

	if p.executionMode == ExecutionModeSequential {
		p.startSequential(ctx)
	} else {
		p.startParallel(ctx)
	}
	return nil
}

// recoverOrphanedWorkItems finds work items with pilot-in-progress tag from a previous run
// and removes the tag so they can be picked up again.
func (p *Poller) recoverOrphanedWorkItems(ctx context.Context) {
	workItems, err := p.client.ListWorkItems(ctx, &ListWorkItemsOptions{
		Tags:          []string{TagInProgress},
		States:        []string{StateNew, StateActive},
		WorkItemTypes: p.workItemTypes,
	})
	if err != nil {
		p.logger.Warn("Failed to check for orphaned work items", slog.Any("error", err))
		return
	}

	if len(workItems) == 0 {
		return
	}

	p.logger.Info("Recovering orphaned in-progress work items", slog.Int("count", len(workItems)))

	for _, wi := range workItems {
		if err := p.client.RemoveWorkItemTag(ctx, wi.ID, TagInProgress); err != nil {
			p.logger.Warn("Failed to remove in-progress tag from orphaned work item",
				slog.Int("id", wi.ID),
				slog.Any("error", err),
			)
			continue
		}
		p.ClearProcessed(wi.ID)
		p.logger.Info("Recovered orphaned work item",
			slog.Int("id", wi.ID),
			slog.String("title", wi.GetTitle()),
		)
	}
}

func (p *Poller) startParallel(ctx context.Context) {
	p.checkForNewWorkItems(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("Azure DevOps poller stopping, waiting for active tasks...")
			p.wgMu.Lock()
			p.stopping.Store(true)
			p.wgMu.Unlock()
			p.activeWg.Wait()
			p.logger.Info("Azure DevOps poller stopped")
			return
		case <-ticker.C:
			p.checkForNewWorkItems(ctx)
		}
	}
}

func (p *Poller) startSequential(ctx context.Context) {
	p.logger.Info("Running in sequential mode",
		slog.Bool("wait_for_merge", p.waitForMerge),
		slog.Duration("pr_timeout", p.prTimeout),
	)

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("Sequential poller stopped")
			return
		default:
		}

		wi, err := p.findOldestUnprocessedWorkItem(ctx)
		if err != nil {
			p.logger.Warn("Failed to find work items", slog.Any("error", err))
			time.Sleep(p.interval)
			continue
		}

		if wi == nil {
			p.logger.Debug("No unprocessed work items found, waiting...")
			select {
			case <-ctx.Done():
				return
			case <-time.After(p.interval):
				continue
			}
		}

		p.logger.Info("Processing work item in sequential mode",
			slog.Int("id", wi.ID),
			slog.String("title", wi.GetTitle()),
		)

		result, err := p.processWorkItemSequential(ctx, wi)
		if err != nil {
			p.logger.Error("Failed to process work item",
				slog.Int("id", wi.ID),
				slog.Any("error", err),
			)
			p.markProcessed(wi.ID)
			continue
		}

		if result != nil && result.PRNumber > 0 && p.OnPRCreated != nil {
			p.logger.Info("Notifying of PR creation",
				slog.Int("pr_id", result.PRNumber),
				slog.Int("work_item_id", wi.ID),
				slog.String("branch", result.BranchName),
			)
			p.OnPRCreated(result.PRNumber, result.PRURL, wi.ID, result.HeadSHA, result.BranchName)
		}

		if result != nil && result.PRNumber > 0 && p.waitForMerge && p.mergeWaiter != nil {
			p.logger.Info("Waiting for PR merge before next work item",
				slog.Int("pr_id", result.PRNumber),
				slog.String("pr_url", result.PRURL),
			)

			mergeResult, err := p.mergeWaiter.WaitWithCallback(ctx, result.PRNumber, func(r *MergeWaitResult) {
				p.logger.Debug("PR status check",
					slog.Int("pr_id", r.PRNumber),
					slog.String("status", r.Message),
				)
			})

			if err != nil {
				p.logger.Warn("Error waiting for PR merge, pausing sequential processing",
					slog.Int("pr_id", result.PRNumber),
					slog.Any("error", err),
				)
				time.Sleep(5 * time.Minute)
				continue
			}

			p.logger.Info("PR merge wait completed",
				slog.Int("pr_id", result.PRNumber),
				slog.Bool("merged", mergeResult.Merged),
				slog.Bool("abandoned", mergeResult.Abandoned),
				slog.Bool("has_conflicts", mergeResult.HasConflicts),
				slog.Bool("timed_out", mergeResult.TimedOut),
			)

			if mergeResult.HasConflicts {
				p.logger.Warn("PR has conflicts, pausing sequential processing",
					slog.Int("pr_id", result.PRNumber),
					slog.String("pr_url", result.PRURL),
				)
				time.Sleep(5 * time.Minute)
				continue
			}

			if mergeResult.TimedOut {
				p.logger.Warn("PR merge timed out, pausing sequential processing",
					slog.Int("pr_id", result.PRNumber),
					slog.String("pr_url", result.PRURL),
				)
				time.Sleep(5 * time.Minute)
				continue
			}

			if mergeResult.Merged {
				p.markProcessed(wi.ID)
				continue
			}

			if mergeResult.Abandoned {
				p.logger.Info("PR was abandoned without merge", slog.Int("pr_id", result.PRNumber))
				continue
			}
		}

		if result != nil && result.Success && result.PRNumber == 0 {
			p.logger.Info("Direct commit completed, proceeding to next work item",
				slog.Int("work_item_id", wi.ID),
				slog.String("commit_sha", result.HeadSHA),
			)
			p.markProcessed(wi.ID)
			continue
		}

		p.markProcessed(wi.ID)
	}
}

func (p *Poller) findOldestUnprocessedWorkItem(ctx context.Context) (*WorkItem, error) {
	workItems, err := p.client.ListWorkItems(ctx, &ListWorkItemsOptions{
		Tags:          []string{p.tag},
		States:        []string{StateNew, StateActive},
		WorkItemTypes: p.workItemTypes,
	})
	if err != nil {
		return nil, err
	}

	var candidates []*WorkItem
	for _, wi := range workItems {
		p.mu.RLock()
		processed := p.processed[wi.ID]
		p.mu.RUnlock()

		if processed {
			continue
		}

		if HasTag(wi, TagInProgress) || HasTag(wi, TagDone) {
			p.recordSkip(skipreason.ReasonStatusTag)
			continue
		}

		candidates = append(candidates, wi)
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].GetCreatedDate().Before(candidates[j].GetCreatedDate())
	})

	return candidates[0], nil
}

func (p *Poller) processWorkItemSequential(ctx context.Context, wi *WorkItem) (*WorkItemResult, error) {
	if p.onWorkItemWithResult != nil {
		return p.onWorkItemWithResult(ctx, wi)
	}

	if p.onWorkItem != nil {
		err := p.onWorkItem(ctx, wi)
		if err != nil {
			return &WorkItemResult{Success: false, Error: err}, err
		}
		return &WorkItemResult{Success: true}, nil
	}

	return nil, fmt.Errorf("no work item handler configured")
}

func (p *Poller) recordSkip(reason string) {
	if p.pollerMetrics != nil {
		p.pollerMetrics.RecordPollerSkipped(p.repoKey, reason)
	}
}

func (p *Poller) recordDispatched() {
	if p.pollerMetrics != nil {
		p.pollerMetrics.RecordPollerDispatched(p.repoKey)
	}
}

func (p *Poller) checkForNewWorkItems(ctx context.Context) {
	workItems, err := p.client.ListWorkItems(ctx, &ListWorkItemsOptions{
		Tags:          []string{p.tag},
		States:        []string{StateNew, StateActive},
		WorkItemTypes: p.workItemTypes,
	})
	if err != nil {
		p.logger.Warn("Failed to fetch work items", slog.Any("error", err))
		return
	}

	sort.Slice(workItems, func(i, j int) bool {
		return workItems[i].GetCreatedDate().Before(workItems[j].GetCreatedDate())
	})

	for _, wi := range workItems {
		p.mu.RLock()
		processed := p.processed[wi.ID]
		p.mu.RUnlock()

		if processed {
			continue
		}

		if p.hasStatusTag(wi) {
			p.markProcessed(wi.ID)
			p.recordSkip(skipreason.ReasonStatusTag)
			continue
		}

		p.markProcessed(wi.ID)

		select {
		case <-ctx.Done():
			return
		case p.semaphore <- struct{}{}:
		}

		p.recordDispatched()
		p.logger.Info("Dispatching Azure DevOps work item for parallel execution",
			slog.Int("id", wi.ID),
			slog.String("title", wi.GetTitle()),
		)

		p.wgMu.Lock()
		if p.stopping.Load() {
			p.wgMu.Unlock()
			<-p.semaphore
			return
		}
		p.activeWg.Add(1)
		p.wgMu.Unlock()

		go p.processWorkItemAsync(ctx, wi)
	}
}

func (p *Poller) processWorkItemAsync(ctx context.Context, wi *WorkItem) {
	defer p.activeWg.Done()
	defer func() { <-p.semaphore }()

	if p.onWorkItemWithResult == nil && p.onWorkItem == nil {
		return
	}

	if err := p.client.AddWorkItemTag(ctx, wi.ID, TagInProgress); err != nil {
		p.logger.Warn("Failed to add in-progress tag",
			slog.Int("id", wi.ID),
			slog.Any("error", err),
		)
	}

	if p.onWorkItemWithResult != nil {
		result, err := p.onWorkItemWithResult(ctx, wi)
		if err != nil {
			p.logger.Error("Failed to process work item",
				slog.Int("id", wi.ID),
				slog.Any("error", err),
			)
			_ = p.client.RemoveWorkItemTag(ctx, wi.ID, TagInProgress)
			_ = p.client.AddWorkItemTag(ctx, wi.ID, TagFailed)
			p.ClearProcessed(wi.ID)
			return
		}

		if result != nil && !result.Success && result.PRNumber == 0 {
			p.logger.Info("Execution failed without PR, unmarking for retry",
				slog.Int("id", wi.ID),
			)
			p.ClearProcessed(wi.ID)
		}

		if result != nil && result.PRNumber > 0 && p.OnPRCreated != nil {
			p.OnPRCreated(result.PRNumber, result.PRURL, wi.ID, result.HeadSHA, result.BranchName)
		}

		_ = p.client.RemoveWorkItemTag(ctx, wi.ID, TagInProgress)
		_ = p.client.AddWorkItemTag(ctx, wi.ID, TagDone)
		return
	}

	err := p.onWorkItem(ctx, wi)
	if err != nil {
		p.logger.Error("Failed to process work item",
			slog.Int("id", wi.ID),
			slog.Any("error", err),
		)
		_ = p.client.RemoveWorkItemTag(ctx, wi.ID, TagInProgress)
		_ = p.client.AddWorkItemTag(ctx, wi.ID, TagFailed)
		return
	}

	_ = p.client.RemoveWorkItemTag(ctx, wi.ID, TagInProgress)
	_ = p.client.AddWorkItemTag(ctx, wi.ID, TagDone)
}

func (p *Poller) hasStatusTag(wi *WorkItem) bool {
	return HasTag(wi, TagInProgress) ||
		HasTag(wi, TagDone) ||
		HasTag(wi, TagFailed)
}

func (p *Poller) markProcessed(id int) {
	p.mu.Lock()
	p.processed[id] = true
	p.mu.Unlock()

	if p.processedStore != nil {
		if err := p.processedStore.Mark("azuredevops", p.repoKey, strconv.Itoa(id)); err != nil {
			p.logger.Warn("Failed to persist processed work item", slog.Int("id", id), slog.Any("error", err))
		}
	}
}

// IsProcessed checks if a work item has been processed.
func (p *Poller) IsProcessed(id int) bool {
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
	p.processed = make(map[int]bool)
	p.mu.Unlock()
}

// ClearProcessed removes a single work item from the processed map (for retry).
func (p *Poller) ClearProcessed(id int) {
	p.mu.Lock()
	delete(p.processed, id)
	p.mu.Unlock()

	if p.processedStore != nil {
		if err := p.processedStore.Unmark("azuredevops", p.repoKey, strconv.Itoa(id)); err != nil {
			p.logger.Warn("Failed to unmark work item in store",
				slog.Int("id", id),
				slog.Any("error", err))
		}
	}

	p.logger.Debug("Cleared work item from processed map", slog.Int("id", id))
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

// WaitForActive waits for all active parallel goroutines to finish. Used in tests.
func (p *Poller) WaitForActive() {
	p.wgMu.Lock()
	p.stopping.Store(true)
	p.wgMu.Unlock()
	p.activeWg.Wait()
}

// ExtractPRNumber extracts PR ID from an Azure DevOps PR URL.
// e.g., "https://dev.azure.com/org/project/_git/repo/pullrequest/123" → 123.
func ExtractPRNumber(prURL string) (int, error) {
	if prURL == "" {
		return 0, fmt.Errorf("empty PR URL")
	}

	re := regexp.MustCompile(`/pullrequest/(\d+)`)
	matches := re.FindStringSubmatch(prURL)
	if len(matches) < 2 {
		return 0, fmt.Errorf("could not extract PR number from URL: %s", prURL)
	}

	var num int
	if _, err := fmt.Sscanf(matches[1], "%d", &num); err != nil {
		return 0, fmt.Errorf("invalid PR number in URL: %s", prURL)
	}

	return num, nil
}
