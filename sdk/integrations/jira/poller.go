package jira

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

// Status labels for tracking issue progress.
const (
	LabelInProgress = "pilot-in-progress"
	LabelDone       = "pilot-done"
	LabelFailed     = "pilot-failed"
)

// IssueResult is returned by the issue handler.
type IssueResult struct {
	Success    bool
	PRNumber   int
	PRURL      string
	HeadSHA    string
	BranchName string
	Error      error
}

// Poller polls Jira for issues with the pilot label.
type Poller struct {
	client      *Client
	config      *Config
	interval    time.Duration
	processed   map[string]bool
	mu          sync.RWMutex
	onIssue     func(ctx context.Context, issue *Issue) (*IssueResult, error)
	onPRCreated func(ev core.PRCreatedEvent)
	logger      *slog.Logger
	pilotLabel  string

	processedStore core.ProcessedStore

	maxConcurrent int
	semaphore     chan struct{}
	activeWg      sync.WaitGroup
	stopping      atomic.Bool
	wgMu          sync.Mutex
}

// PollerOption configures a Poller.
type PollerOption func(*Poller)

// WithOnJiraIssue sets the callback for new issues.
func WithOnJiraIssue(fn func(ctx context.Context, issue *Issue) (*IssueResult, error)) PollerOption {
	return func(p *Poller) {
		p.onIssue = fn
	}
}

// WithJiraPollerLogger sets the logger for the poller.
func WithJiraPollerLogger(logger *slog.Logger) PollerOption {
	return func(p *Poller) {
		p.logger = logger
	}
}

// WithProcessedStore sets the persistent store for processed issue tracking.
func WithProcessedStore(store core.ProcessedStore) PollerOption {
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

// WithOnPRCreated sets the callback for when a PR is created for an issue.
func WithOnPRCreated(fn func(ev core.PRCreatedEvent)) PollerOption {
	return func(p *Poller) {
		p.onPRCreated = fn
	}
}

// NewPoller creates a new Jira issue poller.
func NewPoller(client *Client, config *Config, interval time.Duration, opts ...PollerOption) *Poller {
	pilotLabel := config.TriggerLabel
	if pilotLabel == "" {
		pilotLabel = "pilot"
	}

	p := &Poller{
		client:     client,
		config:     config,
		interval:   interval,
		processed:  make(map[string]bool),
		logger:     slog.Default(),
		pilotLabel: pilotLabel,
	}

	for _, opt := range opts {
		opt(p)
	}

	if p.processedStore != nil {
		loaded, err := p.processedStore.Load("jira", "")
		if err != nil {
			p.logger.Warn("Failed to load processed issues from store", slog.Any("error", err))
		} else if len(loaded) > 0 {
			p.mu.Lock()
			for key := range loaded {
				p.processed[key] = true
			}
			p.mu.Unlock()
			p.logger.Info("Loaded processed issues from store", slog.Int("count", len(loaded)))
		}
	}

	if p.maxConcurrent < 1 {
		p.maxConcurrent = 2
	}
	p.semaphore = make(chan struct{}, p.maxConcurrent)

	return p
}

// Start begins polling for issues.
func (p *Poller) Start(ctx context.Context) error {
	p.logger.Info("Starting Jira poller",
		slog.String("label", p.pilotLabel),
		slog.String("project", p.config.ProjectKey),
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
			p.logger.Info("Jira poller stopping, waiting for active tasks...")
			p.wgMu.Lock()
			p.stopping.Store(true)
			p.wgMu.Unlock()
			p.activeWg.Wait()
			p.logger.Info("Jira poller stopped")
			return nil
		case <-ticker.C:
			p.checkForNewIssues(ctx)
		}
	}
}

// buildJQL constructs the JQL query for finding pilot issues.
func (p *Poller) buildJQL() string {
	var parts []string

	parts = append(parts, fmt.Sprintf("labels = \"%s\"", p.pilotLabel))

	if p.config.ProjectKey != "" {
		parts = append(parts, fmt.Sprintf("project = \"%s\"", p.config.ProjectKey))
	}

	parts = append(parts, "statusCategory != Done")

	return strings.Join(parts, " AND ") + " ORDER BY created ASC"
}

// recoverOrphanedIssues finds issues with pilot-in-progress label from a previous run
// and removes the label so they can be picked up again.
func (p *Poller) recoverOrphanedIssues(ctx context.Context) {
	var parts []string
	parts = append(parts, fmt.Sprintf("labels = \"%s\"", LabelInProgress))
	if p.config.ProjectKey != "" {
		parts = append(parts, fmt.Sprintf("project = \"%s\"", p.config.ProjectKey))
	}
	parts = append(parts, "statusCategory != Done")
	jql := strings.Join(parts, " AND ")

	issues, err := p.client.SearchIssues(ctx, jql, 50)
	if err != nil {
		p.logger.Warn("Failed to check for orphaned issues", slog.Any("error", err))
		return
	}

	if len(issues) == 0 {
		return
	}

	p.logger.Info("Recovering orphaned in-progress issues", slog.Int("count", len(issues)))

	for _, issue := range issues {
		if err := p.client.RemoveLabel(ctx, issue.Key, LabelInProgress); err != nil {
			p.logger.Warn("Failed to remove in-progress label from orphaned issue",
				slog.String("key", issue.Key),
				slog.Any("error", err),
			)
			continue
		}
		p.ClearProcessed(issue.Key)
		p.logger.Info("Recovered orphaned issue",
			slog.String("key", issue.Key),
			slog.String("summary", issue.Fields.Summary),
		)
	}
}

func (p *Poller) checkForNewIssues(ctx context.Context) {
	jql := p.buildJQL()
	issues, err := p.client.SearchIssues(ctx, jql, 50)
	if err != nil {
		p.logger.Warn("Failed to fetch issues", slog.Any("error", err))
		return
	}

	sort.Slice(issues, func(i, j int) bool {
		ti, _ := time.Parse("2006-01-02T15:04:05.000-0700", issues[i].Fields.Created)
		tj, _ := time.Parse("2006-01-02T15:04:05.000-0700", issues[j].Fields.Created)
		return ti.Before(tj)
	})

	for _, issue := range issues {
		p.mu.RLock()
		processed := p.processed[issue.Key]
		p.mu.RUnlock()

		if processed {
			continue
		}

		if p.hasStatusLabel(issue) {
			if p.client.HasLabel(issue, LabelDone) {
				p.markProcessed(issue.Key)
			}
			continue
		}

		p.markProcessed(issue.Key)

		select {
		case <-ctx.Done():
			return
		case p.semaphore <- struct{}{}:
		}

		p.logger.Info("Dispatching Jira issue for parallel execution",
			slog.String("key", issue.Key),
			slog.String("summary", issue.Fields.Summary),
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

	// Strip invisible Unicode from untrusted fields before downstream consumers see them.
	sanitizeIssueInPlace(issue, p.logger)

	if err := p.client.AddLabel(ctx, issue.Key, LabelInProgress); err != nil {
		p.logger.Warn("Failed to add in-progress label",
			slog.String("key", issue.Key),
			slog.Any("error", err),
		)
	}

	result, err := p.onIssue(ctx, issue)
	if err != nil {
		p.logger.Error("Failed to process issue",
			slog.String("key", issue.Key),
			slog.Any("error", err),
		)
		_ = p.client.RemoveLabel(ctx, issue.Key, LabelInProgress)
		_ = p.client.AddLabel(ctx, issue.Key, LabelFailed)
		return
	}

	_ = p.client.RemoveLabel(ctx, issue.Key, LabelInProgress)

	if result != nil && result.Success {
		_ = p.client.AddLabel(ctx, issue.Key, LabelDone)
	}

	if result != nil && result.PRNumber > 0 && p.onPRCreated != nil {
		p.onPRCreated(core.PRCreatedEvent{
			PRNumber:   result.PRNumber,
			PRURL:      result.PRURL,
			IssueID:    issue.ID,
			HeadSHA:    result.HeadSHA,
			BranchName: result.BranchName,
		})
	}
}

func (p *Poller) hasStatusLabel(issue *Issue) bool {
	return p.client.HasLabel(issue, LabelInProgress) ||
		p.client.HasLabel(issue, LabelDone) ||
		p.client.HasLabel(issue, LabelFailed)
}

func (p *Poller) markProcessed(key string) {
	p.mu.Lock()
	p.processed[key] = true
	p.mu.Unlock()

	if p.processedStore != nil {
		if err := p.processedStore.Mark("jira", "", key); err != nil {
			p.logger.Warn("Failed to persist processed issue", slog.String("issue", key), slog.Any("error", err))
		}
	}
}

// IsProcessed checks if an issue has been processed.
func (p *Poller) IsProcessed(key string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.processed[key]
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
	p.processed = make(map[string]bool)
	p.mu.Unlock()
}

// ClearProcessed removes a specific issue from the processed map.
func (p *Poller) ClearProcessed(key string) {
	p.mu.Lock()
	delete(p.processed, key)
	p.mu.Unlock()

	if p.processedStore != nil {
		if err := p.processedStore.Unmark("jira", "", key); err != nil {
			p.logger.Warn("Failed to unmark issue in store",
				slog.String("key", key),
				slog.Any("error", err))
		}
	}

	p.logger.Debug("Cleared issue from processed map", slog.String("key", key))
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
