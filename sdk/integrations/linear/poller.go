package linear

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// IssueResult is returned by the issue handler
type IssueResult struct {
	Success    bool
	PRNumber   int
	PRURL      string
	HeadSHA    string
	BranchName string
	Error      error
}

// ProcessedStore persists which Linear issues have been processed across restarts.
type ProcessedStore interface {
	Mark(source, repo, issueID string) error
	Unmark(source, repo, issueID string) error
	IsProcessed(source, repo, issueID string) (bool, error)
	Load(source, repo string) (map[string]time.Time, error)
}

// Poller polls Linear for issues with a specific label
type Poller struct {
	client    *Client
	config    *WorkspaceConfig
	interval  time.Duration
	processed map[string]bool
	mu        sync.RWMutex
	onIssue   func(ctx context.Context, issue *Issue) (*IssueResult, error)
	logger    *slog.Logger

	// Labels cache
	pilotLabelID      string
	inProgressLabelID string
	doneLabelID       string
	failedLabelID     string

	// Persistent processed store (optional)
	processedStore ProcessedStore

	// OnPRCreated is called when a PR is created after issue processing
	OnPRCreated func(prNumber int, prURL string, issueNumber int, headSHA string, branchName string, issueNodeID string)

	// Parallel execution configuration
	maxConcurrent int
	semaphore     chan struct{}
	activeWg      sync.WaitGroup
	stopping      atomic.Bool
	wgMu          sync.Mutex
}

// PollerOption configures a Poller
type PollerOption func(*Poller)

// WithOnLinearIssue sets the callback for new issues
func WithOnLinearIssue(fn func(ctx context.Context, issue *Issue) (*IssueResult, error)) PollerOption {
	return func(p *Poller) {
		p.onIssue = fn
	}
}

// WithOnPRCreated sets the callback for PR creation events.
func WithOnPRCreated(fn func(prNumber int, prURL string, issueNumber int, headSHA string, branchName string, issueNodeID string)) PollerOption {
	return func(p *Poller) {
		p.OnPRCreated = fn
	}
}

// WithPollerLogger sets the logger for the poller
func WithPollerLogger(logger *slog.Logger) PollerOption {
	return func(p *Poller) {
		p.logger = logger
	}
}

// WithProcessedStore sets the persistent store for processed issue tracking.
func WithProcessedStore(store ProcessedStore) PollerOption {
	return func(p *Poller) {
		p.processedStore = store
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

// NewPoller creates a new Linear issue poller
func NewPoller(client *Client, config *WorkspaceConfig, interval time.Duration, opts ...PollerOption) *Poller {
	p := &Poller{
		client:    client,
		config:    config,
		interval:  interval,
		processed: make(map[string]bool),
		logger:    slog.Default(),
	}

	for _, opt := range opts {
		opt(p)
	}

	if p.processedStore != nil {
		loaded, err := p.processedStore.Load("linear", "")
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

	if p.maxConcurrent < 1 {
		p.maxConcurrent = 2 // default
	}
	p.semaphore = make(chan struct{}, p.maxConcurrent)

	return p
}

// Start begins polling for issues
func (p *Poller) Start(ctx context.Context) error {
	if err := p.cacheLabelIDs(ctx); err != nil {
		return fmt.Errorf("failed to cache label IDs: %w", err)
	}

	p.logger.Info("Starting Linear poller",
		slog.String("team", p.config.TeamID),
		slog.String("label", p.config.TriggerLabel),
		slog.Duration("interval", p.interval),
		slog.Int("max_concurrent", p.maxConcurrent),
	)

	p.recoverOrphanedIssues(ctx)

	p.checkForNewIssues(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("Linear poller stopping, waiting for active tasks...")
			p.wgMu.Lock()
			p.stopping.Store(true)
			p.wgMu.Unlock()
			p.activeWg.Wait()
			p.logger.Info("Linear poller stopped")
			return nil
		case <-ticker.C:
			p.checkForNewIssues(ctx)
		}
	}
}

func (p *Poller) cacheLabelIDs(ctx context.Context) error {
	var err error

	p.pilotLabelID, err = p.client.GetLabelByName(ctx, p.config.TeamID, p.config.TriggerLabel)
	if err != nil {
		return fmt.Errorf("pilot label: %w", err)
	}

	p.inProgressLabelID, err = p.client.GetOrCreateLabel(ctx, p.config.TeamID, "pilot-in-progress", "#0066FF")
	if err != nil {
		p.logger.Warn("Failed to get/create pilot-in-progress label", slog.Any("error", err))
	}

	p.doneLabelID, err = p.client.GetOrCreateLabel(ctx, p.config.TeamID, "pilot-done", "#00AA55")
	if err != nil {
		p.logger.Warn("Failed to get/create pilot-done label", slog.Any("error", err))
	}

	p.failedLabelID, err = p.client.GetOrCreateLabel(ctx, p.config.TeamID, "pilot-failed", "#DD0000")
	if err != nil {
		p.logger.Warn("Failed to get/create pilot-failed label", slog.Any("error", err))
	}

	return nil
}

// recoverOrphanedIssues finds issues with pilot-in-progress label from a previous run
// and removes the label so they can be picked up again.
func (p *Poller) recoverOrphanedIssues(ctx context.Context) {
	if p.inProgressLabelID == "" {
		return
	}

	issues, err := p.client.ListIssues(ctx, &ListIssuesOptions{
		TeamID:     p.config.TeamID,
		Label:      "pilot-in-progress",
		ProjectIDs: p.config.ProjectIDs,
	})
	if err != nil {
		p.logger.Warn("Failed to check for orphaned issues", slog.Any("error", err))
		return
	}

	if len(issues) == 0 {
		return
	}

	p.logger.Info("Recovering orphaned in-progress issues",
		slog.Int("count", len(issues)),
	)

	for _, issue := range issues {
		if err := p.client.RemoveLabel(ctx, issue.ID, p.inProgressLabelID); err != nil {
			p.logger.Warn("Failed to remove in-progress label from orphaned issue",
				slog.String("identifier", issue.Identifier),
				slog.Any("error", err),
			)
			continue
		}
		p.ClearProcessed(issue.ID)
		p.logger.Info("Recovered orphaned issue",
			slog.String("identifier", issue.Identifier),
			slog.String("title", issue.Title),
		)
	}
}

func (p *Poller) checkForNewIssues(ctx context.Context) {
	issues, err := p.client.ListIssues(ctx, &ListIssuesOptions{
		TeamID:     p.config.TeamID,
		Label:      p.config.TriggerLabel,
		ProjectIDs: p.config.ProjectIDs,
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
		processed := p.processed[issue.ID]
		p.mu.RUnlock()

		if processed {
			continue
		}

		if p.hasStatusLabel(issue) {
			p.markProcessed(issue.ID)
			continue
		}

		p.markProcessed(issue.ID)

		select {
		case <-ctx.Done():
			return
		case p.semaphore <- struct{}{}:
		}

		p.logger.Info("Dispatching Linear issue for parallel execution",
			slog.String("identifier", issue.Identifier),
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

// processIssueAsync handles a single issue in a goroutine.
func (p *Poller) processIssueAsync(ctx context.Context, issue *Issue) {
	defer p.activeWg.Done()
	defer func() { <-p.semaphore }()

	if p.onIssue == nil {
		return
	}

	// Strip invisible Unicode from untrusted fields before downstream
	// consumers see them.
	sanitizeIssueInPlace(issue, p.logger)

	if p.inProgressLabelID != "" {
		_ = p.client.AddLabel(ctx, issue.ID, p.inProgressLabelID)
	}

	result, err := p.onIssue(ctx, issue)
	if err != nil {
		p.logger.Error("Failed to process issue",
			slog.String("identifier", issue.Identifier),
			slog.Any("error", err),
		)
		if p.inProgressLabelID != "" {
			_ = p.client.RemoveLabel(ctx, issue.ID, p.inProgressLabelID)
		}
		if p.failedLabelID != "" {
			_ = p.client.AddLabel(ctx, issue.ID, p.failedLabelID)
		}
		return
	}

	if p.inProgressLabelID != "" {
		_ = p.client.RemoveLabel(ctx, issue.ID, p.inProgressLabelID)
	}

	if result != nil && result.Success && p.doneLabelID != "" {
		_ = p.client.AddLabel(ctx, issue.ID, p.doneLabelID)
	}

	if result != nil && result.PRNumber > 0 && p.OnPRCreated != nil {
		p.OnPRCreated(result.PRNumber, result.PRURL, 0, result.HeadSHA, result.BranchName, issue.Identifier)
	}
}

func (p *Poller) hasStatusLabel(issue *Issue) bool {
	for _, label := range issue.Labels {
		switch label.Name {
		case "pilot-in-progress", "pilot-done", "pilot-failed":
			return true
		}
	}
	return false
}

func (p *Poller) markProcessed(id string) {
	p.mu.Lock()
	p.processed[id] = true
	p.mu.Unlock()

	if p.processedStore != nil {
		if err := p.processedStore.Mark("linear", "", id); err != nil {
			p.logger.Warn("Failed to persist processed issue", slog.String("issue", id), slog.Any("error", err))
		}
	}
}

// IsProcessed checks if an issue has been processed
func (p *Poller) IsProcessed(id string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.processed[id]
}

// ProcessedCount returns the number of processed issues
func (p *Poller) ProcessedCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.processed)
}

// Reset clears the processed issues map
func (p *Poller) Reset() {
	p.mu.Lock()
	p.processed = make(map[string]bool)
	p.mu.Unlock()
}

// ClearProcessed removes a single issue from the processed map.
func (p *Poller) ClearProcessed(id string) {
	p.mu.Lock()
	delete(p.processed, id)
	p.mu.Unlock()

	if p.processedStore != nil {
		if err := p.processedStore.Unmark("linear", "", id); err != nil {
			p.logger.Warn("Failed to unmark issue in store",
				slog.String("id", id),
				slog.Any("error", err))
		}
	}

	p.logger.Debug("Cleared issue from processed map",
		slog.String("id", id))
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

// WaitForActive waits for all active parallel goroutines to finish.
func (p *Poller) WaitForActive() {
	p.wgMu.Lock()
	p.stopping.Store(true)
	p.wgMu.Unlock()
	p.activeWg.Wait()
}
