package gitlab

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

// ExecutionMode determines how issues are processed.
type ExecutionMode string

const (
	ExecutionModeSequential ExecutionMode = "sequential"
	ExecutionModeParallel   ExecutionMode = "parallel"
)

// IssueResult is returned by the issue handler with MR information.
type IssueResult struct {
	Success    bool
	MRNumber   int
	MRURL      string
	HeadSHA    string
	BranchName string
	Error      error
}

// Poller polls GitLab for issues with a specific label.
type Poller struct {
	client            *Client
	label             string
	interval          time.Duration
	processed         map[int]bool
	mu                sync.RWMutex
	onIssue           func(ctx context.Context, issue *Issue) error
	onIssueWithResult func(ctx context.Context, issue *Issue) (*IssueResult, error)
	// OnMRCreated is called when an MR is created after issue processing.
	// Parameters: mrIID, mrURL, issueIID, headSHA, branchName.
	OnMRCreated func(mrIID int, mrURL string, issueIID int, headSHA string, branchName string)
	logger      *slog.Logger

	executionMode  ExecutionMode
	mergeWaiter    *MergeWaiter
	waitForMerge   bool
	mrTimeout      time.Duration
	mrPollInterval time.Duration

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

// WithOnIssue sets the callback for new issues (parallel mode).
func WithOnIssue(fn func(ctx context.Context, issue *Issue) error) PollerOption {
	return func(p *Poller) { p.onIssue = fn }
}

// WithOnIssueWithResult sets the callback for new issues that returns MR info.
func WithOnIssueWithResult(fn func(ctx context.Context, issue *Issue) (*IssueResult, error)) PollerOption {
	return func(p *Poller) { p.onIssueWithResult = fn }
}

// WithExecutionMode sets the execution mode (sequential or parallel).
func WithExecutionMode(mode ExecutionMode) PollerOption {
	return func(p *Poller) { p.executionMode = mode }
}

// WithSequentialConfig configures sequential execution settings.
func WithSequentialConfig(waitForMerge bool, pollInterval, timeout time.Duration) PollerOption {
	return func(p *Poller) {
		p.waitForMerge = waitForMerge
		p.mrPollInterval = pollInterval
		p.mrTimeout = timeout
	}
}

// WithOnMRCreated sets the callback for MR creation events.
func WithOnMRCreated(fn func(mrIID int, mrURL string, issueIID int, headSHA string, branchName string)) PollerOption {
	return func(p *Poller) { p.OnMRCreated = fn }
}

// WithProcessedStore sets the persistent store for processed issue tracking.
// On startup, processed issues are loaded to prevent re-processing after hot upgrade.
func WithProcessedStore(store core.ProcessedStore) PollerOption {
	return func(p *Poller) { p.processedStore = store }
}

// WithMaxConcurrent sets the maximum number of parallel issue executions.
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

// NewPoller creates a new GitLab issue poller.
func NewPoller(client *Client, label string, interval time.Duration, opts ...PollerOption) *Poller {
	p := &Poller{
		client:         client,
		label:          label,
		interval:       interval,
		processed:      make(map[int]bool),
		logger:         slog.Default(),
		executionMode:  ExecutionModeParallel,
		waitForMerge:   true,
		mrPollInterval: 30 * time.Second,
		mrTimeout:      1 * time.Hour,
	}

	for _, opt := range opts {
		opt(p)
	}

	if p.processedStore != nil {
		loaded, err := p.processedStore.Load("gitlab", p.repoKey)
		if err != nil {
			p.logger.Warn("Failed to load processed issues from store", slog.Any("error", err))
		} else if len(loaded) > 0 {
			p.mu.Lock()
			for idStr := range loaded {
				if id, parseErr := strconv.Atoi(idStr); parseErr == nil {
					p.processed[id] = true
				}
			}
			p.mu.Unlock()
			p.logger.Info("Loaded processed issues from store", slog.Int("count", len(loaded)))
		}
	}

	if p.maxConcurrent < 1 {
		p.maxConcurrent = 2
	}
	p.semaphore = make(chan struct{}, p.maxConcurrent)

	if p.executionMode == ExecutionModeSequential && p.waitForMerge {
		p.mergeWaiter = NewMergeWaiter(client, &MergeWaiterConfig{
			PollInterval: p.mrPollInterval,
			Timeout:      p.mrTimeout,
		})
	}

	return p
}

// Start begins polling for issues. Implements core.Poller.
func (p *Poller) Start(ctx context.Context) error {
	p.logger.Info("Starting GitLab poller",
		slog.String("label", p.label),
		slog.Duration("interval", p.interval),
		slog.String("mode", string(p.executionMode)),
		slog.Int("max_concurrent", p.maxConcurrent),
	)

	p.recoverOrphanedIssues(ctx)

	if p.executionMode == ExecutionModeSequential {
		p.startSequential(ctx)
	} else {
		p.startParallel(ctx)
	}
	return nil
}

// recoverOrphanedIssues finds issues with pilot-in-progress label from a previous run
// and removes the label so they can be picked up again.
func (p *Poller) recoverOrphanedIssues(ctx context.Context) {
	issues, err := p.client.ListIssues(ctx, &ListIssuesOptions{
		Labels: []string{p.label, LabelInProgress},
		State:  StateOpened,
	})
	if err != nil {
		p.logger.Warn("Failed to check for orphaned issues", slog.Any("error", err))
		return
	}

	if len(issues) == 0 {
		return
	}

	p.logger.Info("Recovering orphaned in-progress issues", slog.Int("count", len(issues)))

	for _, issue := range issues {
		if err := p.client.RemoveIssueLabel(ctx, issue.IID, LabelInProgress); err != nil {
			p.logger.Warn("Failed to remove in-progress label from orphaned issue",
				slog.Int("iid", issue.IID),
				slog.Any("error", err),
			)
			continue
		}
		p.ClearProcessed(issue.IID)
		p.logger.Info("Recovered orphaned issue",
			slog.Int("iid", issue.IID),
			slog.String("title", issue.Title),
		)
	}
}

func (p *Poller) startParallel(ctx context.Context) {
	p.checkForNewIssues(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("GitLab poller stopping, waiting for active tasks...")
			p.wgMu.Lock()
			p.stopping.Store(true)
			p.wgMu.Unlock()
			p.activeWg.Wait()
			p.logger.Info("GitLab poller stopped")
			return
		case <-ticker.C:
			p.checkForNewIssues(ctx)
		}
	}
}

func (p *Poller) startSequential(ctx context.Context) {
	p.logger.Info("Running in sequential mode",
		slog.Bool("wait_for_merge", p.waitForMerge),
		slog.Duration("mr_timeout", p.mrTimeout),
	)

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("Sequential poller stopped")
			return
		default:
		}

		issue, err := p.findOldestUnprocessedIssue(ctx)
		if err != nil {
			p.logger.Warn("Failed to find issues", slog.Any("error", err))
			time.Sleep(p.interval)
			continue
		}

		if issue == nil {
			p.logger.Debug("No unprocessed issues found, waiting...")
			select {
			case <-ctx.Done():
				return
			case <-time.After(p.interval):
				continue
			}
		}

		p.logger.Info("Processing issue in sequential mode",
			slog.Int("iid", issue.IID),
			slog.String("title", issue.Title),
		)

		result, err := p.processIssueSequential(ctx, issue)
		if err != nil {
			p.logger.Error("Failed to process issue",
				slog.Int("iid", issue.IID),
				slog.Any("error", err),
			)
			p.markProcessed(issue.IID)
			continue
		}

		if result != nil && result.MRNumber > 0 && p.OnMRCreated != nil {
			p.logger.Info("Notifying of MR creation",
				slog.Int("mr_iid", result.MRNumber),
				slog.Int("issue_iid", issue.IID),
				slog.String("branch", result.BranchName),
			)
			p.OnMRCreated(result.MRNumber, result.MRURL, issue.IID, result.HeadSHA, result.BranchName)
		}

		if result != nil && result.MRNumber > 0 && p.waitForMerge && p.mergeWaiter != nil {
			p.logger.Info("Waiting for MR merge before next issue",
				slog.Int("mr_iid", result.MRNumber),
				slog.String("mr_url", result.MRURL),
			)

			mergeResult, err := p.mergeWaiter.WaitWithCallback(ctx, result.MRNumber, func(r *MergeWaitResult) {
				p.logger.Debug("MR status check",
					slog.Int("mr_iid", r.MRNumber),
					slog.String("status", r.Message),
				)
			})

			if err != nil {
				p.logger.Warn("Error waiting for MR merge, pausing sequential processing",
					slog.Int("mr_iid", result.MRNumber),
					slog.Any("error", err),
				)
				time.Sleep(5 * time.Minute)
				continue
			}

			p.logger.Info("MR merge wait completed",
				slog.Int("mr_iid", result.MRNumber),
				slog.Bool("merged", mergeResult.Merged),
				slog.Bool("closed", mergeResult.Closed),
				slog.Bool("has_conflicts", mergeResult.HasConflicts),
				slog.Bool("timed_out", mergeResult.TimedOut),
			)

			if mergeResult.HasConflicts {
				p.logger.Warn("MR has conflicts, pausing sequential processing",
					slog.Int("mr_iid", result.MRNumber),
					slog.String("mr_url", result.MRURL),
				)
				time.Sleep(5 * time.Minute)
				continue
			}

			if mergeResult.TimedOut {
				p.logger.Warn("MR merge timed out, pausing sequential processing",
					slog.Int("mr_iid", result.MRNumber),
					slog.String("mr_url", result.MRURL),
				)
				time.Sleep(5 * time.Minute)
				continue
			}

			if mergeResult.Merged {
				p.markProcessed(issue.IID)
				continue
			}

			if mergeResult.Closed {
				p.logger.Info("MR was closed without merge", slog.Int("mr_iid", result.MRNumber))
				continue
			}
		}

		if result != nil && result.Success && result.MRNumber == 0 {
			p.logger.Info("Direct commit completed, proceeding to next issue",
				slog.Int("issue_iid", issue.IID),
				slog.String("commit_sha", result.HeadSHA),
			)
			p.markProcessed(issue.IID)
			continue
		}

		p.markProcessed(issue.IID)
	}
}

func (p *Poller) findOldestUnprocessedIssue(ctx context.Context) (*Issue, error) {
	issues, err := p.client.ListIssues(ctx, &ListIssuesOptions{
		Labels:  []string{p.label},
		State:   StateOpened,
		Sort:    "asc",
		OrderBy: "created_at",
	})
	if err != nil {
		return nil, err
	}

	var candidates []*Issue
	for _, issue := range issues {
		p.mu.RLock()
		processed := p.processed[issue.IID]
		p.mu.RUnlock()

		if processed {
			continue
		}

		if HasLabel(issue, LabelInProgress) || HasLabel(issue, LabelDone) {
			p.recordSkip(skipreason.ReasonStatusLabel)
			continue
		}

		candidates = append(candidates, issue)
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})

	return candidates[0], nil
}

func (p *Poller) processIssueSequential(ctx context.Context, issue *Issue) (*IssueResult, error) {
	sanitizeIssueInPlace(p.logger, issue)

	if p.onIssueWithResult != nil {
		return p.onIssueWithResult(ctx, issue)
	}

	if p.onIssue != nil {
		err := p.onIssue(ctx, issue)
		if err != nil {
			return &IssueResult{Success: false, Error: err}, err
		}
		return &IssueResult{Success: true}, nil
	}

	return nil, fmt.Errorf("no issue handler configured")
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

func (p *Poller) checkForNewIssues(ctx context.Context) {
	issues, err := p.client.ListIssues(ctx, &ListIssuesOptions{
		Labels:  []string{p.label},
		State:   StateOpened,
		Sort:    "asc",
		OrderBy: "created_at",
	})
	if err != nil {
		p.logger.Warn("Failed to fetch issues", slog.Any("error", err))
		return
	}

	sort.Slice(issues, func(i, j int) bool {
		return issues[i].CreatedAt.Before(issues[j].CreatedAt)
	})

	for _, issue := range issues {
		p.mu.RLock()
		processed := p.processed[issue.IID]
		p.mu.RUnlock()

		if processed {
			continue
		}

		if p.hasStatusLabel(issue) {
			p.markProcessed(issue.IID)
			p.recordSkip(skipreason.ReasonStatusLabel)
			continue
		}

		p.markProcessed(issue.IID)

		select {
		case <-ctx.Done():
			return
		case p.semaphore <- struct{}{}:
		}

		p.recordDispatched()
		p.logger.Info("Dispatching GitLab issue for parallel execution",
			slog.Int("iid", issue.IID),
			slog.String("title", issue.Title),
		)

		p.wgMu.Lock()
		if p.stopping.Load() {
			p.wgMu.Unlock()
			<-p.semaphore
			return
		}
		p.activeWg.Add(1)
		p.wgMu.Unlock()

		go p.processIssueAsync(ctx, issue)
	}
}

func (p *Poller) processIssueAsync(ctx context.Context, issue *Issue) {
	defer p.activeWg.Done()
	defer func() { <-p.semaphore }()

	if p.onIssueWithResult == nil && p.onIssue == nil {
		return
	}

	sanitizeIssueInPlace(p.logger, issue)

	if err := p.client.AddIssueLabels(ctx, issue.IID, []string{LabelInProgress}); err != nil {
		p.logger.Warn("Failed to add in-progress label",
			slog.Int("iid", issue.IID),
			slog.Any("error", err),
		)
	}

	if p.onIssueWithResult != nil {
		result, err := p.onIssueWithResult(ctx, issue)
		if err != nil {
			p.logger.Error("Failed to process issue",
				slog.Int("iid", issue.IID),
				slog.Any("error", err),
			)
			_ = p.client.RemoveIssueLabel(ctx, issue.IID, LabelInProgress)
			_ = p.client.AddIssueLabels(ctx, issue.IID, []string{LabelFailed})
			p.ClearProcessed(issue.IID)
			return
		}

		if result != nil && !result.Success && result.MRNumber == 0 {
			p.logger.Info("Execution failed without MR, unmarking for retry",
				slog.Int("iid", issue.IID),
			)
			p.ClearProcessed(issue.IID)
		}

		if result != nil && result.MRNumber > 0 && p.OnMRCreated != nil {
			p.OnMRCreated(result.MRNumber, result.MRURL, issue.IID, result.HeadSHA, result.BranchName)
		}

		_ = p.client.RemoveIssueLabel(ctx, issue.IID, LabelInProgress)
		_ = p.client.AddIssueLabels(ctx, issue.IID, []string{LabelDone})
		return
	}

	err := p.onIssue(ctx, issue)
	if err != nil {
		p.logger.Error("Failed to process issue",
			slog.Int("iid", issue.IID),
			slog.Any("error", err),
		)
		_ = p.client.RemoveIssueLabel(ctx, issue.IID, LabelInProgress)
		_ = p.client.AddIssueLabels(ctx, issue.IID, []string{LabelFailed})
		return
	}

	_ = p.client.RemoveIssueLabel(ctx, issue.IID, LabelInProgress)
	_ = p.client.AddIssueLabels(ctx, issue.IID, []string{LabelDone})
}

func (p *Poller) hasStatusLabel(issue *Issue) bool {
	return HasLabel(issue, LabelInProgress) ||
		HasLabel(issue, LabelDone) ||
		HasLabel(issue, LabelFailed)
}

func (p *Poller) markProcessed(iid int) {
	p.mu.Lock()
	p.processed[iid] = true
	p.mu.Unlock()

	if p.processedStore != nil {
		if err := p.processedStore.Mark("gitlab", p.repoKey, strconv.Itoa(iid)); err != nil {
			p.logger.Warn("Failed to persist processed issue", slog.Int("iid", iid), slog.Any("error", err))
		}
	}
}

// IsProcessed checks if an issue has been processed.
func (p *Poller) IsProcessed(iid int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.processed[iid]
}

// ProcessedCount returns the number of processed issues.
func (p *Poller) ProcessedCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.processed)
}

// Reset clears the processed issues map.
func (p *Poller) Reset() {
	p.mu.Lock()
	p.processed = make(map[int]bool)
	p.mu.Unlock()
}

// ClearProcessed removes a single issue from the processed map (for retry).
func (p *Poller) ClearProcessed(iid int) {
	p.mu.Lock()
	delete(p.processed, iid)
	p.mu.Unlock()

	if p.processedStore != nil {
		if err := p.processedStore.Unmark("gitlab", p.repoKey, strconv.Itoa(iid)); err != nil {
			p.logger.Warn("Failed to unmark issue in store",
				slog.Int("iid", iid),
				slog.Any("error", err))
		}
	}

	p.logger.Debug("Cleared issue from processed map", slog.Int("iid", iid))
}

// Drain stops accepting new issues and waits for active executions to finish.
// Used during hot upgrade to let in-flight work complete before process restart.
func (p *Poller) Drain() {
	p.logger.Info("Draining poller — no new issues will be accepted")
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

// ExtractMRNumber extracts the MR IID from a GitLab MR URL.
// e.g., "https://gitlab.com/namespace/project/-/merge_requests/123" → 123.
func ExtractMRNumber(mrURL string) (int, error) {
	if mrURL == "" {
		return 0, fmt.Errorf("empty MR URL")
	}

	re := regexp.MustCompile(`/(?:-/)?merge_requests/(\d+)`)
	matches := re.FindStringSubmatch(mrURL)
	if len(matches) < 2 {
		return 0, fmt.Errorf("could not extract MR number from URL: %s", mrURL)
	}

	var num int
	if _, err := fmt.Sscanf(matches[1], "%d", &num); err != nil {
		return 0, fmt.Errorf("invalid MR number in URL: %s", mrURL)
	}

	return num, nil
}
