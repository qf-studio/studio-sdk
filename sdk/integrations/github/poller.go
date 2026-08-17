package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/util/skipreason"
	"github.com/qf-studio/studio-sdk/sdk/util/text"
)

// ExecutionMode determines how issues are processed.
type ExecutionMode string

const (
	// ExecutionModeSequential processes one issue at a time, waiting for PR merge.
	ExecutionModeSequential ExecutionMode = "sequential"
	// ExecutionModeParallel processes issues concurrently.
	ExecutionModeParallel ExecutionMode = "parallel"
	// ExecutionModeAuto uses parallel dispatch with scope-overlap guard:
	// non-overlapping issues run concurrently; overlapping groups run oldest-first.
	ExecutionModeAuto ExecutionMode = "auto"
)

// IssueResult is returned by the issue handler with PR information.
type IssueResult struct {
	Success    bool
	PRNumber   int
	PRURL      string
	HeadSHA    string
	BranchName string
	Error      error
}

// Poller polls GitHub for issues with a specific label.
type Poller struct {
	client    *Client
	owner     string
	repo      string
	label     string
	interval  time.Duration
	processed map[int]time.Time
	mu        sync.RWMutex

	onIssue           func(ctx context.Context, issue *Issue) error
	onIssueWithResult func(ctx context.Context, issue *Issue) (*IssueResult, error)
	// OnPRCreated is called when a PR is created after issue processing.
	// Parameters: prNumber, prURL, issueNumber, headSHA, branchName, issueNodeID.
	OnPRCreated func(prNumber int, prURL string, issueNumber int, headSHA string, branchName string, issueNodeID string)

	logger *slog.Logger

	executionMode  ExecutionMode
	mergeWaiter    *MergeWaiter
	waitForMerge   bool
	prTimeout      time.Duration
	prPollInterval time.Duration

	maxConcurrent int
	semaphore     chan struct{}
	activeWg      sync.WaitGroup
	stopping      atomic.Bool
	wgMu          sync.Mutex

	processedStore core.ProcessedStore

	// retryGracePeriod prevents rapid re-dispatch of recently-processed issues.
	retryGracePeriod time.Duration

	failedRetryCount map[int]int
	maxFailedRetries int

	retryReadyCount      map[int]int
	maxRetryReadyRetries int

	pollerMetrics skipreason.PollerMetricsRecorder

	// projectBoardSource sources candidates from a Projects V2 board column instead
	// of by label. nil means label-based discovery (default, unchanged behavior).
	projectBoardSource *ProjectBoardSource

	// boardSync moves the issue card to inProgressStatus on confirmed dispatch.
	// nil or empty inProgressStatus disables the write-back, keeping label-mode identical.
	boardSync        *ProjectBoardSync
	inProgressStatus string

	// onBoardSyncAuthError is called at most once per process lifetime (guarded by
	// boardSyncAlertOnce) when a board-sync call fails with an INSUFFICIENT_SCOPES
	// GraphQL error or a 401 AuthError, instead of the per-item WARN spam that
	// otherwise leaves an under-scoped token's cards silently stale (GH-4488).
	onBoardSyncAuthError func(error)
	boardSyncAlertOnce   sync.Once

	// unsourcedWarned tracks, by issue number, which open dispatch-labeled issues
	// are currently WARN-logged as not board-sourced. Cleared/rebuilt each check so
	// the WARN fires once per poll-session per issue (not once per tick) and clears
	// automatically once the issue is sourced correctly (GH-4488).
	unsourcedWarned map[int]bool
	unsourcedMu     sync.Mutex

	// Host-supplied execution-pipeline hooks (core.PollerDeps parity). All nil-safe:
	// a nil hook disables the corresponding gate, preserving pre-hook behavior.
	taskChecker        core.TaskChecker
	execChecker        core.ExecutionChecker
	projectPath        string
	preFlightJudge     core.PreFlightJudger
	execSaver          core.ExecutionSaver
	issueMetrics       core.IssueMetricsRecorder
	rateLimitScheduler core.RateLimitScheduler

	// defaultBranch is resolved lazily (once per poller instance) on the first
	// hasMergedWork call and cached for the poller's lifetime. defaultBranchOK
	// is false if resolution failed (e.g. API error) — hasMergedWork then falls
	// back to the base-blind legacy behavior rather than wedging polling
	// (GH-117).
	defaultBranchOnce sync.Once
	defaultBranch     string
	defaultBranchOK   bool
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

// WithOnIssueWithResult sets the callback for new issues that returns PR info.
func WithOnIssueWithResult(fn func(ctx context.Context, issue *Issue) (*IssueResult, error)) PollerOption {
	return func(p *Poller) { p.onIssueWithResult = fn }
}

// WithExecutionMode sets the execution mode.
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
// Parameters: prNumber, prURL, issueNumber, headSHA, branchName, issueNodeID.
func WithOnPRCreated(fn func(prNumber int, prURL string, issueNumber int, headSHA string, branchName string, issueNodeID string)) PollerOption {
	return func(p *Poller) { p.OnPRCreated = fn }
}

// WithProcessedStore sets the persistent store for processed issue tracking.
func WithProcessedStore(store core.ProcessedStore) PollerOption {
	return func(p *Poller) { p.processedStore = store }
}

// WithRetryGracePeriod sets the minimum time before a processed issue can be re-dispatched.
func WithRetryGracePeriod(d time.Duration) PollerOption {
	return func(p *Poller) { p.retryGracePeriod = d }
}

// WithMaxFailedRetries sets the maximum number of auto-retries for pilot-failed issues.
func WithMaxFailedRetries(n int) PollerOption {
	return func(p *Poller) {
		if n < 0 {
			n = 0
		}
		p.maxFailedRetries = n
	}
}

// WithMaxRetryReadyRetries sets the maximum number of auto-retries for pilot-retry-ready issues.
func WithMaxRetryReadyRetries(n int) PollerOption {
	return func(p *Poller) {
		if n < 0 {
			n = 0
		}
		p.maxRetryReadyRetries = n
	}
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

// WithProjectBoardSource configures the poller to source candidates from a Projects V2
// board column instead of by label. When set, FindIssuesFromProject replaces ListIssues
// as the candidate fetch in fetchCandidates; all downstream filters are unchanged.
func WithProjectBoardSource(src *ProjectBoardSource) PollerOption {
	return func(p *Poller) { p.projectBoardSource = src }
}

// WithBoardSync configures the poller to move the issue card to inProgressStatus on the
// Projects V2 board after confirmed dispatch. No-op when bs is nil or inProgressStatus is "".
func WithBoardSync(bs *ProjectBoardSync, inProgressStatus string) PollerOption {
	return func(p *Poller) {
		p.boardSync = bs
		p.inProgressStatus = inProgressStatus
	}
}

// WithBoardSyncAuthAlert sets a hook called once per process lifetime when board
// sync fails with an auth/scope-class error (INSUFFICIENT_SCOPES GraphQL error,
// or a 401 AuthError) — instead of the per-item WARN spam that otherwise leaves an
// under-scoped token's board cards silently stale. No-op when fn is nil.
func WithBoardSyncAuthAlert(fn func(error)) PollerOption {
	return func(p *Poller) { p.onBoardSyncAuthError = fn }
}

// WithTaskChecker sets the checker consulted during retry-grace evaluation so
// issues whose tasks are still queued/in-progress are not re-dispatched.
func WithTaskChecker(tc core.TaskChecker) PollerOption {
	return func(p *Poller) { p.taskChecker = tc }
}

// WithExecutionChecker sets the completed-execution guard. projectPath scopes
// its lookups (and ExecutionSaver records) to the local project checkout.
func WithExecutionChecker(ec core.ExecutionChecker, projectPath string) PollerOption {
	return func(p *Poller) {
		p.execChecker = ec
		p.projectPath = projectPath
	}
}

// WithPreFlightJudge sets the judge that evaluates issue quality before
// dispatch. Rejected issues get the needs-clarification label plus an
// explanatory comment and are NOT marked processed, so removing the label
// re-triggers dispatch.
func WithPreFlightJudge(judge core.PreFlightJudger) PollerOption {
	return func(p *Poller) { p.preFlightJudge = judge }
}

// WithExecutionSaver sets the sink for pre-flight rejection records.
func WithExecutionSaver(saver core.ExecutionSaver) PollerOption {
	return func(p *Poller) { p.execSaver = saver }
}

// WithIssueMetricsRecorder sets the recorder for issue processing outcomes.
func WithIssueMetricsRecorder(rec core.IssueMetricsRecorder) PollerOption {
	return func(p *Poller) { p.issueMetrics = rec }
}

// WithRateLimitScheduler sets the host scheduler that classifies rate-limited
// handler errors and queues timed retries. When it accepts an error the issue
// is left unmarked — the scheduler owns the retry.
func WithRateLimitScheduler(s core.RateLimitScheduler) PollerOption {
	return func(p *Poller) { p.rateLimitScheduler = s }
}

// NewPoller creates a new GitHub issue poller.
// repo must be in "owner/repo" format.
func NewPoller(client *Client, repo string, label string, interval time.Duration, opts ...PollerOption) (*Poller, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo format, expected owner/repo: %s", repo)
	}

	p := &Poller{
		client:               client,
		owner:                parts[0],
		repo:                 parts[1],
		label:                label,
		interval:             interval,
		processed:            make(map[int]time.Time),
		logger:               slog.Default(),
		executionMode:        ExecutionModeAuto,
		waitForMerge:         true,
		prPollInterval:       30 * time.Second,
		prTimeout:            1 * time.Hour,
		retryGracePeriod:     5 * time.Minute,
		failedRetryCount:     make(map[int]int),
		maxFailedRetries:     3,
		retryReadyCount:      make(map[int]int),
		maxRetryReadyRetries: 3,
	}

	for _, opt := range opts {
		opt(p)
	}

	if p.executionMode == ExecutionModeSequential && p.waitForMerge {
		p.mergeWaiter = NewMergeWaiter(client, p.owner, p.repo, &MergeWaiterConfig{
			PollInterval: p.prPollInterval,
			Timeout:      p.prTimeout,
		})
		p.mergeWaiter.logger = p.logger
	}

	if p.processedStore != nil {
		loaded, err := p.processedStore.Load("github", p.repoKey())
		if err != nil {
			p.logger.Warn("Failed to load processed issues from store", slog.Any("error", err))
		} else if len(loaded) > 0 {
			p.mu.Lock()
			for idStr, t := range loaded {
				if num, parseErr := strconv.Atoi(idStr); parseErr == nil {
					p.processed[num] = t
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

	return p, nil
}

// Start begins polling for issues. Implements core.Poller.
func (p *Poller) Start(ctx context.Context) error {
	p.logger.Info("Starting GitHub poller",
		slog.String("repo", p.owner+"/"+p.repo),
		slog.String("label", p.label),
		slog.Duration("interval", p.interval),
		slog.String("mode", string(p.executionMode)),
	)

	p.recoverOrphanedIssues(ctx)

	if p.executionMode == ExecutionModeSequential {
		p.startSequential(ctx)
	} else {
		p.startParallel(ctx)
	}
	return nil
}

func (p *Poller) recoverOrphanedIssues(ctx context.Context) {
	issues, err := p.client.ListIssues(ctx, p.owner, p.repo, &ListIssuesOptions{
		Labels: []string{p.label, LabelInProgress},
		State:  StateOpen,
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
		if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, LabelInProgress); err != nil {
			p.logger.Warn("Failed to remove in-progress label from orphaned issue",
				slog.Int("number", issue.Number),
				slog.Any("error", err),
			)
			continue
		}
		p.unmarkProcessed(issue.Number)
		p.logger.Info("Recovered orphaned issue",
			slog.Int("number", issue.Number),
			slog.String("title", issue.Title),
		)
	}
}

func (p *Poller) startParallel(ctx context.Context) {
	p.logger.Info("Running in parallel mode",
		slog.String("mode", string(p.executionMode)),
		slog.Int("max_concurrent", p.maxConcurrent),
	)

	p.checkForNewIssues(ctx)
	p.checkUnsourcedLabeledIssues(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("Parallel poller stopping, waiting for active tasks...")
			p.wgMu.Lock()
			p.stopping.Store(true)
			p.wgMu.Unlock()
			p.activeWg.Wait()
			p.logger.Info("Parallel poller stopped")
			return
		case <-ticker.C:
			p.checkForNewIssues(ctx)
			p.checkUnsourcedLabeledIssues(ctx)
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
			slog.Int("number", issue.Number),
			slog.String("title", issue.Title),
		)

		if !p.passesPreFlight(ctx, issue) {
			continue // skip dispatch WITHOUT marking processed; label removal re-triggers
		}

		p.syncBoardStatusInProgress(ctx, issue)

		result, err := p.processIssueSequential(ctx, issue)
		if err != nil {
			if p.queueRateLimitRetry(issue, err) {
				continue // not marked processed — the host scheduler owns the retry
			}
			p.logger.Error("Failed to process issue",
				slog.Int("number", issue.Number),
				slog.Any("error", err),
			)
			p.syncBoardStatusRetry(ctx, issue)
			continue
		}

		if result != nil && result.PRNumber > 0 && p.OnPRCreated != nil {
			p.logger.Info("Notifying of PR creation",
				slog.Int("pr_number", result.PRNumber),
				slog.Int("issue_number", issue.Number),
				slog.String("branch", result.BranchName),
			)
			p.OnPRCreated(result.PRNumber, result.PRURL, issue.Number, result.HeadSHA, result.BranchName, issue.NodeID)
		}

		if result != nil && result.PRNumber > 0 && p.waitForMerge && p.mergeWaiter != nil {
			p.logger.Info("Waiting for PR merge before next issue",
				slog.Int("pr_number", result.PRNumber),
				slog.String("pr_url", result.PRURL),
			)

			mergeResult, err := p.mergeWaiter.WaitWithCallback(ctx, result.PRNumber, func(r *MergeWaitResult) {
				p.logger.Debug("PR status check",
					slog.Int("pr_number", r.PRNumber),
					slog.String("status", r.Message),
				)
			})

			if err != nil {
				p.logger.Warn("Error waiting for PR merge, pausing sequential processing",
					slog.Int("pr_number", result.PRNumber),
					slog.Any("error", err),
				)
				time.Sleep(5 * time.Minute)
				continue
			}

			p.logger.Info("PR merge wait completed",
				slog.Int("pr_number", result.PRNumber),
				slog.Bool("merged", mergeResult.Merged),
				slog.Bool("closed", mergeResult.Closed),
				slog.Bool("conflicting", mergeResult.Conflicting),
				slog.Bool("timed_out", mergeResult.TimedOut),
			)

			if mergeResult.Conflicting {
				p.logger.Warn("PR has conflicts, pausing sequential processing",
					slog.Int("pr_number", result.PRNumber),
				)
				time.Sleep(5 * time.Minute)
				continue
			}

			if mergeResult.TimedOut {
				p.logger.Warn("PR merge timed out, pausing sequential processing",
					slog.Int("pr_number", result.PRNumber),
				)
				time.Sleep(5 * time.Minute)
				continue
			}

			if mergeResult.Merged {
				p.markProcessed(issue.Number)
				continue
			}

			if mergeResult.Closed {
				p.logger.Info("PR was closed without merge", slog.Int("pr_number", result.PRNumber))
				continue
			}
		}

		if result != nil && result.Success && result.PRNumber == 0 {
			p.logger.Info("Direct commit completed, proceeding to next issue",
				slog.Int("issue_number", issue.Number),
				slog.String("commit_sha", result.HeadSHA),
			)
			p.markProcessed(issue.Number)
			continue
		}

		if result != nil && !result.Success && result.PRNumber == 0 {
			p.logger.Info("Execution failed without PR, not marking as processed (retryable)",
				slog.Int("issue_number", issue.Number),
			)
			p.syncBoardStatusRetry(ctx, issue)
			continue
		}

		p.markProcessed(issue.Number)
	}
}

// fetchCandidates returns the raw candidate issues for this poll cycle. When a
// projectBoardSource is configured it sources from the board column instead of
// listing open issues by label. This is the single source-selection point used
// by BOTH dispatch paths — findOldestUnprocessedIssue (sequential) and
// checkForNewIssues (parallel/auto) — so the board source is honored regardless
// of execution mode.
func (p *Poller) fetchCandidates(ctx context.Context) ([]*Issue, error) {
	if p.projectBoardSource != nil {
		sourceStatus := p.projectBoardSource.config.SourceStatus
		if sourceStatus == "" {
			sourceStatus = "Todo"
		}
		return p.projectBoardSource.FindIssuesFromProject(ctx, sourceStatus)
	}
	return p.client.ListIssues(ctx, p.owner, p.repo, &ListIssuesOptions{
		Labels: []string{p.label},
		State:  StateOpen,
		Sort:   "created", // oldest first
	})
}

// checkUnsourcedLabeledIssues detects open, dispatch-labeled issues that a
// configured board source will never surface to fetchCandidates — because
// they're absent from the board, or on the board in a status other than
// source_status. Without this, the poller looks identical to the operator
// whether it's healthy-with-an-empty-Todo-column or has silently stopped
// seeing real work (GH-4488). No-op when board sourcing isn't active.
//
// WARNs once per issue per poll-session (the internal unsourcedWarned set is
// rebuilt on every call, so a resolved issue drops out and the WARN clears;
// a still-unresolved issue is not re-logged on the next call) and keeps the
// pilot_poller_unsourced_labeled_issues gauge (via pollerMetrics) in sync.
//
// Deliberately NOT called from checkForNewIssues/findOldestUnprocessedIssue:
// those are the hot dispatch path and unit-tested against fixed fake-server
// routes (issues-list / single-issue GET). This runs on its own cadence from
// startParallel so it can freely add a labeled-issue list call plus a
// board-wide status scan without touching the dispatch loop's request shape.
func (p *Poller) checkUnsourcedLabeledIssues(ctx context.Context) {
	if p.projectBoardSource == nil {
		return
	}

	labeled, err := p.client.ListIssues(ctx, p.owner, p.repo, &ListIssuesOptions{
		Labels: []string{p.label},
		State:  StateOpen,
	})
	if err != nil {
		p.logger.Warn("unsourced-check: failed to list labeled issues", slog.Any("error", err))
		return
	}

	statuses, err := p.projectBoardSource.BoardStatusByIssue(ctx)
	if err != nil {
		p.logger.Warn("unsourced-check: failed to read board statuses", slog.Any("error", err))
		return
	}

	sourceStatus := p.projectBoardSource.config.SourceStatus
	if sourceStatus == "" {
		sourceStatus = "Todo"
	}

	p.unsourcedMu.Lock()
	defer p.unsourcedMu.Unlock()

	stillUnsourced := make(map[int]bool)
	for _, issue := range labeled {
		status, onBoard := statuses[issue.Number]
		if onBoard && strings.EqualFold(status, sourceStatus) {
			continue // sourced correctly — nothing to warn about
		}
		stillUnsourced[issue.Number] = true
		if !p.unsourcedWarned[issue.Number] {
			p.logger.Warn("labeled issue not board-sourced — add to board status or remove label",
				slog.Int("number", issue.Number),
				slog.String("label", p.label),
				slog.String("source_status", sourceStatus),
				slog.Bool("on_board", onBoard),
				slog.String("board_status", status),
			)
		}
	}
	p.unsourcedWarned = stillUnsourced

	if p.pollerMetrics != nil {
		p.pollerMetrics.RecordUnsourcedLabeledIssues(p.repoKey(), len(stillUnsourced))
	}
}

// syncBoardStatusInProgress moves the issue card to the configured in-progress status
// on the Projects V2 board. Called once after confirmed dispatch, before execution starts.
// Logs errors but does not fail the dispatch — board sync is best-effort.
// No-op when boardSync is nil or inProgressStatus is empty.
func (p *Poller) syncBoardStatusInProgress(ctx context.Context, issue *Issue) {
	p.syncBoardStatus(ctx, issue, p.inProgressStatus)
}

// syncBoardStatusRetry moves the issue card back to the board's source column after a
// confirmed dispatch fails before producing a PR (execution error, or an unsuccessful
// run with no PR). fetchCandidates only ever reads the source column, so without this
// revert a card left in the in-progress column by syncBoardStatusInProgress becomes
// permanently invisible to the poller — the source can never re-pick it (GH-4474).
// No-op when board sourcing isn't active (label-mode dispatch has no card to revert).
func (p *Poller) syncBoardStatusRetry(ctx context.Context, issue *Issue) {
	if p.projectBoardSource == nil {
		return
	}
	status := p.projectBoardSource.config.SourceStatus
	if status == "" {
		status = "Todo" // same default as fetchCandidates
	}
	p.syncBoardStatus(ctx, issue, status)
}

// syncBoardStatus moves the issue card to the named status column on the Projects V2
// board. Logs errors but never fails the caller — board sync is best-effort. No-op
// when boardSync is nil or status is empty.
func (p *Poller) syncBoardStatus(ctx context.Context, issue *Issue, status string) {
	if p.boardSync == nil || status == "" {
		return
	}

	nodeID := issue.NodeID
	if nodeID == "" {
		var err error
		nodeID, err = p.client.GetIssueNodeID(ctx, p.owner, p.repo, issue.Number)
		if err != nil {
			p.logger.Warn("board sync: failed to resolve issue node ID",
				slog.Int("issue", issue.Number),
				slog.Any("error", err))
			p.alertBoardSyncAuthError(err)
			return
		}
	}

	if err := p.boardSync.UpdateProjectItemStatus(ctx, nodeID, status); err != nil {
		p.logger.Warn("board sync: failed to update project item status",
			slog.Int("issue", issue.Number),
			slog.String("status", status),
			slog.Any("error", err))
		p.alertBoardSyncAuthError(err)
	}
}

// classifyBoardSyncAuthError inspects a board-sync error for an auth/scope-class
// failure — an INSUFFICIENT_SCOPES GraphQL error type, or a 401 AuthError from
// the REST node-ID lookup — and returns its detail message. ok is false for
// unrelated errors (network blips, rate limits, missing card, etc.), which stay
// WARN-only rather than paging anyone.
func classifyBoardSyncAuthError(err error) (detail string, ok bool) {
	if err == nil {
		return "", false
	}
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return authErr.Message, true
	}
	msg := err.Error()
	if !strings.Contains(msg, "INSUFFICIENT_SCOPES") {
		return "", false
	}
	return msg, true
}

// alertBoardSyncAuthError fires the host-supplied onBoardSyncAuthError hook the
// first time a board-sync call fails with an auth/scope-class error — once per
// process lifetime (boardSyncAlertOnce), not once per item or per poll, so a
// persistently under-scoped token doesn't spam an alert on every dispatch.
// No-op for unrelated errors or when no hook is configured.
func (p *Poller) alertBoardSyncAuthError(err error) {
	detail, ok := classifyBoardSyncAuthError(err)
	if !ok || p.onBoardSyncAuthError == nil {
		return
	}
	p.boardSyncAlertOnce.Do(func() {
		p.onBoardSyncAuthError(fmt.Errorf("board sync auth/scope failure: %s", detail))
	})
}

func (p *Poller) findOldestUnprocessedIssue(ctx context.Context) (*Issue, error) {
	issues, err := p.fetchCandidates(ctx)
	if err != nil {
		return nil, err
	}

	var candidates []*Issue
	for _, issue := range issues {
		if issue.PullRequest != nil {
			continue
		}

		if HasLabel(issue, LabelInProgress) || HasLabel(issue, LabelDone) {
			continue
		}

		if HasLabel(issue, LabelBlocked) {
			continue
		}

		if HasLabel(issue, LabelNeedsClarification) {
			continue
		}

		if p.skipSupersededByParent(ctx, issue) {
			continue
		}

		if HasLabel(issue, LabelFailed) {
			if !p.shouldRetryFailedIssue(ctx, issue) {
				continue
			}
		}

		if HasLabel(issue, LabelRetryReady) {
			if !p.shouldRetryRetryReadyIssue(ctx, issue) {
				continue
			}
		}

		p.mu.RLock()
		processedAt, processed := p.processed[issue.Number]
		p.mu.RUnlock()

		if processed {
			if p.retryGracePeriod > 0 && time.Since(processedAt) < p.retryGracePeriod {
				p.logger.Debug("Issue within retry grace period, skipping",
					slog.Int("number", issue.Number),
					slog.Duration("elapsed", time.Since(processedAt)),
					slog.Duration("grace_period", p.retryGracePeriod))
				continue
			}

			if p.isTaskStillQueued(issue) {
				continue
			}

			p.logger.Info("Issue was processed but status labels removed, allowing retry",
				slog.Int("number", issue.Number))
			p.mu.Lock()
			delete(p.processed, issue.Number)
			p.mu.Unlock()
			if p.processedStore != nil {
				if err := p.processedStore.Unmark("github", p.repoKey(), strconv.Itoa(issue.Number)); err != nil {
					p.logger.Warn("Failed to unmark issue in store",
						slog.Int("number", issue.Number),
						slog.Any("error", err))
				}
			}

			if p.hasMergedWork(ctx, issue) {
				continue
			}
		}

		if p.hasCompletedExecution(issue) {
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

	for _, candidate := range candidates {
		if !p.hasPendingDependencies(ctx, candidate) {
			return candidate, nil
		}
		p.logger.Info("Skipping issue with pending dependencies",
			slog.Int("number", candidate.Number),
			slog.String("title", candidate.Title),
		)
	}

	return nil, nil
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

// groupByOverlappingScope partitions issues into groups where members reference
// at least one common directory. Within each group only the oldest issue is dispatched.
func groupByOverlappingScope(candidates []*Issue) [][]*Issue {
	n := len(candidates)
	if n == 0 {
		return nil
	}

	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	dirs := make([]map[string]bool, n)
	for i, c := range candidates {
		dirs[i] = extractDirectoriesFromText(text.SanitizeUntrustedString(c.Body))
	}
	for i := 0; i < n; i++ {
		if len(dirs[i]) == 0 {
			continue
		}
		for j := i + 1; j < n; j++ {
			if len(dirs[j]) == 0 {
				continue
			}
			for d := range dirs[i] {
				if dirs[j][d] {
					union(i, j)
					break
				}
			}
		}
	}

	groups := make(map[int][]*Issue)
	for i, c := range candidates {
		root := find(i)
		groups[root] = append(groups[root], c)
	}

	result := make([][]*Issue, 0, len(groups))
	for _, g := range groups {
		result = append(result, g)
	}
	return result
}

// filePathPattern matches file paths with common source-code extensions.
var filePathPattern = regexp.MustCompile(`\b((?:[\w\-]+/)+[\w\-]+\.(?:go|py|ts|tsx|js|jsx|rs|java|rb|css|scss|html|yaml|yml|json|toml|sql|sh|md))\b`)

// dirOnlyPattern matches directory-only references ending with a slash.
var dirOnlyPattern = regexp.MustCompile(`\b((?:[\w\-]+/)+[\w\-]+/)(?:\s|$|[),;:"'])`)

// extractDirectoriesFromText finds file paths and directory references in text
// and returns their unique parent directories.
func extractDirectoriesFromText(txt string) map[string]bool {
	dirs := make(map[string]bool)

	matches := filePathPattern.FindAllStringSubmatch(txt, -1)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		filePath := m[1]
		lastSlash := strings.LastIndex(filePath, "/")
		if lastSlash > 0 {
			dirs[filePath[:lastSlash]] = true
		}
	}

	dirMatches := dirOnlyPattern.FindAllStringSubmatch(txt, -1)
	for _, m := range dirMatches {
		if len(m) < 2 {
			continue
		}
		dir := strings.TrimRight(m[1], "/")
		if dir != "" {
			dirs[dir] = true
		}
	}

	return dirs
}

func (p *Poller) repoKey() string { return p.owner + "/" + p.repo }

func (p *Poller) recordSkip(reason string) {
	if p.pollerMetrics != nil {
		p.pollerMetrics.RecordPollerSkipped(p.repoKey(), reason)
	}
}

func (p *Poller) recordDispatched() {
	if p.pollerMetrics != nil {
		p.pollerMetrics.RecordPollerDispatched(p.repoKey())
	}
}

func (p *Poller) recordDeferredScopeOverlap() {
	if p.pollerMetrics != nil {
		p.pollerMetrics.RecordPollerDeferredScopeOverlap(p.repoKey())
	}
}

func (p *Poller) checkForNewIssues(ctx context.Context) {
	issues, err := p.fetchCandidates(ctx)
	if err != nil {
		p.logger.Warn("Failed to fetch issues", slog.Any("error", err))
		return
	}

	var candidates []*Issue
	for _, issue := range issues {
		if issue.PullRequest != nil {
			continue
		}

		if HasLabel(issue, LabelInProgress) {
			p.recordSkip(skipreason.ReasonInProgress)
			continue
		}

		if HasLabel(issue, LabelBlocked) {
			p.recordSkip(skipreason.ReasonBlocked)
			continue
		}

		if HasLabel(issue, LabelNeedsClarification) {
			p.recordSkip(skipreason.ReasonNeedsClarification)
			continue
		}

		if p.skipSupersededByParent(ctx, issue) {
			p.recordSkip(skipreason.ReasonSuperseded)
			continue
		}

		if HasLabel(issue, LabelFailed) {
			if !p.shouldRetryFailedIssue(ctx, issue) {
				p.recordSkip(skipreason.ReasonFailedSkip)
				continue
			}
		}

		if HasLabel(issue, LabelRetryReady) {
			if !p.shouldRetryRetryReadyIssue(ctx, issue) {
				p.recordSkip(skipreason.ReasonRetryReadySkip)
				continue
			}
		}

		if HasLabel(issue, LabelDone) {
			p.markProcessed(issue.Number)
			p.recordSkip(skipreason.ReasonDone)
			continue
		}

		p.mu.RLock()
		processedAt, processed := p.processed[issue.Number]
		p.mu.RUnlock()

		if processed {
			if p.retryGracePeriod > 0 && time.Since(processedAt) < p.retryGracePeriod {
				p.logger.Debug("Issue within retry grace period, skipping",
					slog.Int("number", issue.Number),
					slog.Duration("elapsed", time.Since(processedAt)),
					slog.Duration("grace_period", p.retryGracePeriod))
				p.recordSkip(skipreason.ReasonProcessedGrace)
				continue
			}

			if p.isTaskStillQueued(issue) {
				p.recordSkip(skipreason.ReasonTaskQueued)
				continue
			}

			p.logger.Info("Issue was processed but status labels removed, allowing retry",
				slog.Int("number", issue.Number))
			p.mu.Lock()
			delete(p.processed, issue.Number)
			p.mu.Unlock()
			if p.processedStore != nil {
				if err := p.processedStore.Unmark("github", p.repoKey(), strconv.Itoa(issue.Number)); err != nil {
					p.logger.Warn("Failed to unmark issue in store",
						slog.Int("number", issue.Number),
						slog.Any("error", err))
				}
			}

			if p.hasMergedWork(ctx, issue) {
				p.recordSkip(skipreason.ReasonHasMergedWork)
				continue
			}
		}

		if p.hasPendingDependencies(ctx, issue) {
			p.logger.Debug("Skipping issue with pending dependencies in parallel mode",
				slog.Int("number", issue.Number),
			)
			p.recordSkip(skipreason.ReasonPendingDependency)
			continue
		}

		if p.hasCompletedExecution(issue) {
			p.recordSkip(skipreason.ReasonCompletedExecution)
			continue
		}

		candidates = append(candidates, issue)
	}

	// Group by overlapping scope; dispatch only oldest per group.
	groups := groupByOverlappingScope(candidates)
	var toDispatch []*Issue
	for _, group := range groups {
		if len(group) == 1 {
			toDispatch = append(toDispatch, group[0])
		} else {
			sort.Slice(group, func(i, j int) bool {
				return group[i].CreatedAt.Before(group[j].CreatedAt)
			})
			toDispatch = append(toDispatch, group[0])
			for _, deferred := range group[1:] {
				p.logger.Info("Deferring issue due to overlapping scope with older issue",
					slog.Int("number", deferred.Number),
					slog.Int("dispatched", group[0].Number),
				)
				p.recordDeferredScopeOverlap()
			}
		}
	}

	for _, issue := range toDispatch {
		// Refresh labels before dispatch to avoid stale snapshot races.
		if fresh, ferr := p.client.GetIssue(ctx, p.owner, p.repo, issue.Number); ferr == nil && fresh != nil {
			if HasLabel(fresh, LabelDone) || HasLabel(fresh, LabelInProgress) {
				p.logger.Info("Skipping dispatch — fresh labels show issue already handled",
					slog.Int("number", issue.Number),
					slog.Bool("done", HasLabel(fresh, LabelDone)),
					slog.Bool("in_progress", HasLabel(fresh, LabelInProgress)),
				)
				p.markProcessed(issue.Number)
				p.recordSkip(skipreason.ReasonFreshLabelCheck)
				continue
			}
			// Dispatch the freshly fetched issue rather than the list-snapshot —
			// the snapshot's body/title/labels can be stale under queue depth or
			// semaphore backpressure by the time dispatch actually happens (GH-105).
			issue = fresh
		} else if ferr != nil {
			p.logger.Debug("Failed to refresh issue labels before dispatch — proceeding with snapshot",
				slog.Int("number", issue.Number),
				slog.Any("error", ferr),
			)
		}

		if !p.passesPreFlight(ctx, issue) {
			p.recordSkip(skipreason.ReasonPreFlightReject)
			continue // skip markProcessed so label removal re-triggers dispatch
		}

		p.markProcessed(issue.Number)

		select {
		case <-ctx.Done():
			return
		case p.semaphore <- struct{}{}:
		}

		p.recordDispatched()
		p.logger.Info("Dispatching issue for parallel execution",
			slog.Int("number", issue.Number),
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

		go func(issue *Issue) {
			defer p.activeWg.Done()
			defer func() { <-p.semaphore }()

			p.syncBoardStatusInProgress(ctx, issue)

			if p.onIssueWithResult != nil {
				sanitizeIssueInPlace(p.logger, issue)
				result, err := p.onIssueWithResult(ctx, issue)
				if err != nil {
					if p.queueRateLimitRetry(issue, err) {
						return // stay marked processed — the host scheduler owns the retry
					}
					p.logger.Error("Failed to process issue",
						slog.Int("number", issue.Number),
						slog.Any("error", err),
					)
					p.unmarkProcessed(issue.Number)
					p.syncBoardStatusRetry(ctx, issue)
					return
				}

				if result != nil && !result.Success && result.PRNumber == 0 {
					p.logger.Info("Execution failed without PR, unmarking for retry",
						slog.Int("number", issue.Number),
					)
					p.unmarkProcessed(issue.Number)
					p.syncBoardStatusRetry(ctx, issue)
				}

				if result != nil && result.PRNumber > 0 && p.OnPRCreated != nil {
					p.logger.Info("Notifying of PR creation (parallel path)",
						slog.Int("pr_number", result.PRNumber),
						slog.Int("issue_number", issue.Number),
						slog.String("branch", result.BranchName),
					)
					p.OnPRCreated(result.PRNumber, result.PRURL, issue.Number, result.HeadSHA, result.BranchName, issue.NodeID)
				}
			} else if p.onIssue != nil {
				sanitizeIssueInPlace(p.logger, issue)
				if err := p.onIssue(ctx, issue); err != nil {
					if p.queueRateLimitRetry(issue, err) {
						return // stay marked processed — the host scheduler owns the retry
					}
					p.logger.Error("Failed to process issue",
						slog.Int("number", issue.Number),
						slog.Any("error", err),
					)
					p.unmarkProcessed(issue.Number)
					p.syncBoardStatusRetry(ctx, issue)
				}
			}
		}(issue)
	}
}

func (p *Poller) markProcessed(number int) {
	p.mu.Lock()
	p.processed[number] = time.Now()
	p.mu.Unlock()

	if p.processedStore != nil {
		if err := p.processedStore.Mark("github", p.repoKey(), strconv.Itoa(number)); err != nil {
			p.logger.Warn("Failed to persist processed issue", slog.Int("issue", number), slog.Any("error", err))
		}
	}
}

func (p *Poller) unmarkProcessed(number int) {
	p.mu.Lock()
	delete(p.processed, number)
	p.mu.Unlock()

	if p.processedStore != nil {
		if err := p.processedStore.Unmark("github", p.repoKey(), strconv.Itoa(number)); err != nil {
			p.logger.Warn("Failed to unmark processed issue", slog.Int("issue", number), slog.Any("error", err))
		}
	}
}

// resolveDefaultBranch resolves and caches the repo's default branch, once per
// poller instance. On API error it logs a single WARN and returns ok=false —
// callers must fall back to base-blind legacy behavior rather than blocking
// polling on a repo-metadata fetch (GH-117).
func (p *Poller) resolveDefaultBranch(ctx context.Context) (branch string, ok bool) {
	p.defaultBranchOnce.Do(func() {
		repository, err := p.client.GetRepository(ctx, p.owner, p.repo)
		if err != nil || repository == nil || repository.DefaultBranch == "" {
			p.logger.Warn("Failed to resolve default branch, falling back to legacy merged-PR detection (base branch not verified)",
				slog.String("owner", p.owner),
				slog.String("repo", p.repo),
				slog.Any("error", err),
			)
			return
		}
		p.defaultBranch = repository.DefaultBranch
		p.defaultBranchOK = true
	})
	return p.defaultBranch, p.defaultBranchOK
}

// hasMergedWork checks if the issue already has merged PRs. A PR merged into a
// non-default base (e.g. a stacked PR squash-merged into its stack parent
// branch, not main) is not treated as delivery — only a merge onto the repo's
// default branch marks the issue done (GH-117). If the default branch cannot
// be resolved, this falls back to the legacy base-blind checks (fail open).
func (p *Poller) hasMergedWork(ctx context.Context, issue *Issue) bool {
	defaultBranch, haveDefaultBranch := p.resolveDefaultBranch(ctx)

	var found bool
	var err error
	if haveDefaultBranch {
		found, err = p.client.SearchMergedPRsForIssueOnBase(ctx, p.owner, p.repo, issue.Number, defaultBranch)
	} else {
		found, err = p.client.SearchMergedPRsForIssue(ctx, p.owner, p.repo, issue.Number)
	}
	if err != nil {
		p.logger.Warn("Failed to check for merged PRs",
			slog.Int("issue", issue.Number),
			slog.Any("error", err),
		)
	}

	if !found {
		branch := fmt.Sprintf("pilot/GH-%d", issue.Number)
		if haveDefaultBranch {
			result, berr := p.client.FindMergedPRByBranchOnBase(ctx, p.owner, p.repo, branch, defaultBranch)
			if berr != nil {
				p.logger.Warn("Failed to check merged PRs by branch",
					slog.Int("issue", issue.Number),
					slog.String("branch", branch),
					slog.Any("error", berr),
				)
				return false
			}
			switch {
			case result.OnDefaultBranch:
				found = true
				p.logger.Info("Merged PR found via branch lookup",
					slog.Int("issue", issue.Number),
					slog.String("branch", branch),
				)
			case result.OtherBase != "":
				p.logger.Info("Merged PR found via branch lookup but merged into non-default base, not treating as done",
					slog.Int("issue", issue.Number),
					slog.String("branch", branch),
					slog.String("base", result.OtherBase),
					slog.String("default_branch", defaultBranch),
				)
				return false
			default:
				return false
			}
		} else {
			branchFound, berr := p.client.FindMergedPRByBranch(ctx, p.owner, p.repo, branch)
			if berr != nil {
				p.logger.Warn("Failed to check merged PRs by branch",
					slog.Int("issue", issue.Number),
					slog.String("branch", branch),
					slog.Any("error", berr),
				)
				return false
			}
			if !branchFound {
				return false
			}
			found = true
			p.logger.Info("Merged PR found via branch lookup",
				slog.Int("issue", issue.Number),
				slog.String("branch", branch),
			)
		}
	}

	if !found {
		return false
	}

	p.logger.Info("Issue already has merged PRs, marking as done",
		slog.Int("issue", issue.Number),
		slog.String("title", issue.Title),
	)
	if err := p.client.AddLabels(ctx, p.owner, p.repo, issue.Number, []string{LabelDone}); err != nil {
		p.logger.Warn("Failed to add pilot-done label",
			slog.Int("issue", issue.Number),
			slog.Any("error", err),
		)
	}
	if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, LabelFailed); err != nil {
		p.logger.Debug("Failed to remove pilot-failed (may not exist)",
			slog.Int("issue", issue.Number),
			slog.Any("error", err),
		)
	}
	p.markProcessed(issue.Number)
	return true
}

func (p *Poller) shouldRetryFailedIssue(ctx context.Context, issue *Issue) bool {
	if issue.State != "open" {
		return false
	}

	if HasLabel(issue, LabelDone) {
		return false
	}

	if HasLabel(issue, LabelTitleRejected) {
		p.logger.Info("Skipping retry — pilot-title-rejected set",
			slog.Int("number", issue.Number),
		)
		return false
	}

	p.mu.RLock()
	retries := p.failedRetryCount[issue.Number]
	p.mu.RUnlock()

	if retries >= p.maxFailedRetries {
		p.logger.Warn("Issue has reached max failed retries, skipping",
			slog.Int("number", issue.Number),
			slog.Int("retries", retries),
			slog.Int("max", p.maxFailedRetries),
		)
		return false
	}

	if p.hasMergedWork(ctx, issue) {
		return false
	}

	if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, LabelFailed); err != nil {
		p.logger.Warn("Failed to remove pilot-failed label for retry",
			slog.Int("number", issue.Number),
			slog.Any("error", err),
		)
		return false
	}

	p.mu.Lock()
	p.failedRetryCount[issue.Number] = retries + 1
	p.mu.Unlock()

	p.ClearProcessed(issue.Number)

	p.logger.Info("Auto-retrying pilot-failed issue",
		slog.Int("number", issue.Number),
		slog.Int("retry", retries+1),
		slog.Int("max", p.maxFailedRetries),
	)

	return true
}

func (p *Poller) shouldRetryRetryReadyIssue(ctx context.Context, issue *Issue) bool {
	if issue.State != "open" {
		return false
	}

	if HasLabel(issue, LabelDone) {
		return false
	}

	if HasLabel(issue, LabelRetryExhausted) {
		p.logger.Warn("Issue is pilot-retry-exhausted, skipping",
			slog.Int("number", issue.Number),
		)
		return false
	}

	var currentRetryLabel, nextRetryLabel string
	switch {
	case HasLabel(issue, LabelRetry2):
		currentRetryLabel = LabelRetry2
		nextRetryLabel = LabelRetryExhausted
	case HasLabel(issue, LabelRetry1):
		currentRetryLabel = LabelRetry1
		nextRetryLabel = LabelRetry2
	default:
		nextRetryLabel = LabelRetry1
	}

	if nextRetryLabel == LabelRetryExhausted {
		if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, currentRetryLabel); err != nil {
			p.logger.Warn("Failed to remove prior retry label",
				slog.Int("number", issue.Number),
				slog.String("label", currentRetryLabel),
				slog.Any("error", err),
			)
		}
		if err := p.client.AddLabels(ctx, p.owner, p.repo, issue.Number, []string{LabelRetryExhausted}); err != nil {
			p.logger.Warn("Failed to add pilot-retry-exhausted label",
				slog.Int("number", issue.Number),
				slog.Any("error", err),
			)
		}
		_ = p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, LabelRetryReady)
		p.logger.Warn("Issue exhausted retry budget — escalated to pilot-retry-exhausted",
			slog.Int("number", issue.Number),
		)
		return false
	}

	if p.hasMergedWork(ctx, issue) {
		return false
	}

	if currentRetryLabel != "" {
		if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, currentRetryLabel); err != nil {
			p.logger.Warn("Failed to remove prior retry label",
				slog.Int("number", issue.Number),
				slog.String("label", currentRetryLabel),
				slog.Any("error", err),
			)
		}
	}
	if err := p.client.AddLabels(ctx, p.owner, p.repo, issue.Number, []string{nextRetryLabel}); err != nil {
		p.logger.Warn("Failed to add retry label",
			slog.Int("number", issue.Number),
			slog.String("label", nextRetryLabel),
			slog.Any("error", err),
		)
	}

	if err := p.client.RemoveLabel(ctx, p.owner, p.repo, issue.Number, LabelRetryReady); err != nil {
		p.logger.Warn("Failed to remove pilot-retry-ready label for retry",
			slog.Int("number", issue.Number),
			slog.Any("error", err),
		)
		return false
	}

	p.mu.Lock()
	p.retryReadyCount[issue.Number]++
	p.mu.Unlock()

	// Delete any stale completed execution row so hasCompletedExecution doesn't
	// silently no-op the re-dispatch. Must happen before ClearProcessed so the
	// issue can actually reach the dispatch gate.
	if p.execChecker != nil {
		taskID := fmt.Sprintf("GH-%d", issue.Number)
		if err := p.execChecker.InvalidateCompletion(taskID, p.projectPath); err != nil {
			p.logger.Warn("InvalidateCompletion failed on retry-ready re-dispatch — proceeding",
				slog.String("task_id", taskID),
				slog.Any("error", err),
			)
		}
	}

	p.ClearProcessed(issue.Number)

	p.logger.Info("Auto-retrying pilot-retry-ready issue",
		slog.Int("number", issue.Number),
		slog.String("retry_label", nextRetryLabel),
	)

	return true
}

// skipSupersededByParent auto-closes a sub-issue whose parent epic has already shipped.
func (p *Poller) skipSupersededByParent(ctx context.Context, issue *Issue) bool {
	parentNum := parseParentIssueNumber(issue.Body)
	if parentNum <= 0 {
		return false
	}

	parent, err := p.client.GetIssue(ctx, p.owner, p.repo, parentNum)
	if err != nil {
		p.logger.Warn("Failed to fetch parent issue, falling through to normal dispatch",
			slog.Int("issue", issue.Number),
			slog.Int("parent", parentNum),
			slog.Any("error", err),
		)
		return false
	}

	if parent.State != StateClosed || !HasLabel(parent, LabelDone) {
		return false
	}

	p.logger.Info("Auto-closing sub-issue: parent epic already shipped",
		slog.Int("issue", issue.Number),
		slog.Int("parent", parentNum),
	)

	comment := fmt.Sprintf(
		"🔁 Auto-closed by Pilot: parent epic #%d already shipped this work. "+
			"This sub-issue is redundant.",
		parentNum,
	)
	if _, cerr := p.client.AddComment(ctx, p.owner, p.repo, issue.Number, comment); cerr != nil {
		p.logger.Warn("Failed to post superseded comment",
			slog.Int("issue", issue.Number),
			slog.Any("error", cerr),
		)
	}
	if lerr := p.client.AddLabels(ctx, p.owner, p.repo, issue.Number, []string{LabelSuperseded}); lerr != nil {
		p.logger.Warn("Failed to add pilot-superseded label",
			slog.Int("issue", issue.Number),
			slog.Any("error", lerr),
		)
	}
	if uerr := p.client.UpdateIssueState(ctx, p.owner, p.repo, issue.Number, StateClosed); uerr != nil {
		p.logger.Warn("Failed to close superseded sub-issue",
			slog.Int("issue", issue.Number),
			slog.Any("error", uerr),
		)
	}

	p.markProcessed(issue.Number)
	return true
}

// hasPendingDependencies checks if any of the issue's dependencies are still open.
func (p *Poller) hasPendingDependencies(ctx context.Context, issue *Issue) bool {
	deps := ParseDependencies(text.SanitizeUntrustedString(issue.Body))
	if len(deps) == 0 {
		return false
	}

	for _, depNum := range deps {
		depIssue, err := p.client.GetIssue(ctx, p.owner, p.repo, depNum)
		if err != nil {
			p.logger.Warn("Failed to fetch dependency issue",
				slog.Int("issue", issue.Number),
				slog.Int("dependency", depNum),
				slog.Any("error", err),
			)
			return true
		}

		if depIssue.State == "open" {
			p.logger.Debug("Issue has open dependency, skipping",
				slog.Int("issue", issue.Number),
				slog.Int("dependency", depNum),
			)
			return true
		}
	}

	return false
}

// isTaskStillQueued reports whether the issue's task is still queued or
// in-progress in the host pipeline. Consulted after the retry grace period so
// a long-running execution is never re-dispatched. nil checker means "not queued".
func (p *Poller) isTaskStillQueued(issue *Issue) bool {
	if p.taskChecker == nil {
		return false
	}
	taskID := fmt.Sprintf("GH-%d", issue.Number)
	if !p.taskChecker.IsTaskQueued(taskID) {
		return false
	}
	p.logger.Debug("Issue still queued/in-progress, skipping retry",
		slog.Int("number", issue.Number),
		slog.String("task_id", taskID))
	return true
}

// hasCompletedExecution reports whether a completed execution record already
// exists for the issue — prevents re-dispatch when the done label failed to
// apply. On a positive hit the issue is marked processed. Check errors fail
// open (dispatch rather than lose work).
func (p *Poller) hasCompletedExecution(issue *Issue) bool {
	if p.execChecker == nil {
		return false
	}
	taskID := fmt.Sprintf("GH-%d", issue.Number)
	completed, err := p.execChecker.HasCompletedExecution(taskID, p.projectPath)
	if err != nil {
		p.logger.Warn("Failed to check execution status",
			slog.Int("number", issue.Number),
			slog.Any("error", err))
		return false
	}
	if !completed {
		return false
	}
	p.logger.Info("Skipping re-dispatch — completed execution exists",
		slog.Int("number", issue.Number),
		slog.String("task_id", taskID))
	p.markProcessed(issue.Number)
	return true
}

// passesPreFlight runs the pre-flight judge when configured. A judge error
// fails open (dispatch proceeds). On rejection the issue is labeled and
// commented via handlePreFlightReject and the caller must skip dispatch
// WITHOUT marking the issue processed, so removing the needs-clarification
// label re-triggers it on a later poll.
func (p *Poller) passesPreFlight(ctx context.Context, issue *Issue) bool {
	if p.preFlightJudge == nil {
		return true
	}
	verdict, err := p.preFlightJudge.JudgeIssue(ctx, issue.Title, issue.Body, "")
	if err != nil {
		p.logger.Warn("pre-flight judge error (fail-open)",
			slog.Int("issue", issue.Number),
			slog.Any("error", err))
		return true
	}
	if verdict.Accepted {
		return true
	}
	p.logger.Info("pre-flight rejected issue",
		slog.Int("issue", issue.Number),
		slog.String("decision", verdict.Decision),
		slog.String("reason", verdict.Reason))
	p.handlePreFlightReject(ctx, issue, verdict)
	return false
}

// handlePreFlightReject handles an issue rejected by the pre-flight judge:
//   - adds the needs-clarification label so the issue is filtered on later polls
//   - posts a comment explaining the decision and how to re-trigger
//   - saves a declined-preflight execution record if an ExecutionSaver is
//     wired, carrying the issue's repo identity (owner/name) when the wired
//     saver implements ExecutionSaverV2
//
// The caller must NOT mark the issue processed, so label removal re-triggers dispatch.
func (p *Poller) handlePreFlightReject(ctx context.Context, issue *Issue, verdict core.Verdict) {
	taskID := fmt.Sprintf("GH-%d", issue.Number)

	if err := p.client.AddLabels(ctx, p.owner, p.repo, issue.Number, []string{LabelNeedsClarification}); err != nil {
		p.logger.Warn("pre-flight: failed to add needs-clarification label",
			slog.Int("issue", issue.Number),
			slog.Any("error", err))
	}

	comment := fmt.Sprintf(
		"**Pre-flight check declined this issue.**\n\n"+
			"**Decision:** `%s`\n"+
			"**Reason:** %s\n"+
			"**Confidence:** %.0f%%\n\n"+
			"To re-trigger: edit the issue to address the above, then remove the `%s` label.",
		verdict.Decision, verdict.Reason, verdict.Confidence*100, LabelNeedsClarification,
	)
	if _, err := p.client.AddComment(ctx, p.owner, p.repo, issue.Number, comment); err != nil {
		p.logger.Warn("pre-flight: failed to post rejection comment",
			slog.Int("issue", issue.Number),
			slog.Any("error", err))
	}

	if p.execSaver != nil {
		var err error
		if v2, ok := p.execSaver.(core.ExecutionSaverV2); ok {
			err = v2.SaveDeclinedExecutionRecord(core.DeclinedExecutionRecord{
				TaskID:      taskID,
				ProjectPath: p.projectPath,
				Status:      "declined-preflight",
				Reason:      verdict.Reason,
				RepoOwner:   p.owner,
				RepoName:    p.repo,
			})
		} else {
			err = p.execSaver.SaveDeclinedExecution(taskID, p.projectPath, "declined-preflight", verdict.Reason)
		}
		if err != nil {
			p.logger.Warn("pre-flight: failed to save execution record",
				slog.Int("issue", issue.Number),
				slog.Any("error", err))
		}
	}
}

// queueRateLimitRetry hands a rate-limited handler error to the host
// scheduler. Returns true when the host recognized the error and queued a
// timed retry — the issue then stays out of the poller's failure path so the
// scheduler owns the retry. The issue's Title/Body are already sanitized by
// the dispatch path before the handler runs.
func (p *Poller) queueRateLimitRetry(issue *Issue, err error) bool {
	if p.rateLimitScheduler == nil || err == nil {
		return false
	}
	taskID := fmt.Sprintf("GH-%d", issue.Number)
	if !p.rateLimitScheduler.QueueRetryIfRateLimited(taskID, issue.Title, issue.Body, err.Error()) {
		return false
	}
	p.logger.Info("Task queued for retry after rate limit",
		slog.Int("issue", issue.Number),
		slog.String("task_id", taskID))
	if p.issueMetrics != nil {
		p.issueMetrics.RecordIssueProcessed("rate_limited")
	}
	return true
}

// Drain stops accepting new issues and waits for active executions to finish.
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

// IsProcessed checks if an issue has been processed.
func (p *Poller) IsProcessed(number int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.processed[number]
	return ok
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
	p.processed = make(map[int]time.Time)
	p.mu.Unlock()
}

// MarkProcessed marks an issue as processed (in-memory + persistent store),
// so the poll cycle will not dispatch it. Used by hosts that create and
// execute sub-issues themselves and must prevent independent re-dispatch.
func (p *Poller) MarkProcessed(number int) {
	p.markProcessed(number)
}

// ClearProcessed removes a single issue from the processed map (for retry).
func (p *Poller) ClearProcessed(number int) {
	p.mu.Lock()
	delete(p.processed, number)
	p.mu.Unlock()

	if p.processedStore != nil {
		if err := p.processedStore.Unmark("github", p.repoKey(), strconv.Itoa(number)); err != nil {
			p.logger.Warn("Failed to unmark issue in store",
				slog.Int("number", number),
				slog.Any("error", err))
		}
	}

	p.logger.Debug("Cleared issue from processed map", slog.Int("number", number))
}

// ExtractPRNumber extracts PR number from a GitHub PR URL.
// e.g., "https://github.com/owner/repo/pull/123" → 123.
func ExtractPRNumber(prURL string) (int, error) {
	if prURL == "" {
		return 0, fmt.Errorf("empty PR URL")
	}

	re := regexp.MustCompile(`/pulls?/(\d+)`)
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

// dependencyRegex matches common dependency patterns in issue bodies.
var dependencyRegex = regexp.MustCompile(`(?i)(?:depends\s+on|blocked\s+by|requires):?\s*#(\d+)`)

// ParseDependencies extracts issue numbers that this issue depends on from the body.
func ParseDependencies(body string) []int {
	if body == "" {
		return nil
	}

	matches := dependencyRegex.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[int]bool)
	var deps []int

	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		var num int
		if _, err := fmt.Sscanf(match[1], "%d", &num); err != nil {
			continue
		}
		if num > 0 && !seen[num] {
			seen[num] = true
			deps = append(deps, num)
		}
	}

	return deps
}

// parentRefRegex extracts parent issue references from issue body.
var parentRefRegex = regexp.MustCompile(`(?m)^Parent:\s*(?:GH-|#)(\d+)`)

// ParseParentIssueNumber extracts the parent issue number ("Parent: #N" /
// "Parent: GH-N") from an issue body. Returns 0 if no parent reference is
// found. Exported for hosts that apply parent-based gating outside the poller.
func ParseParentIssueNumber(body string) int {
	return parseParentIssueNumber(body)
}

// parseParentIssueNumber extracts the parent issue number from an issue body.
// Returns 0 if no parent reference is found.
func parseParentIssueNumber(body string) int {
	matches := parentRefRegex.FindStringSubmatch(body)
	if len(matches) < 2 {
		return 0
	}
	var num int
	_, _ = fmt.Sscanf(matches[1], "%d", &num)
	return num
}
