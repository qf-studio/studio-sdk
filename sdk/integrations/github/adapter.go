// Package github provides a Studio SDK adapter for GitHub (https://github.com).
// It implements sdk/core.Adapter, sdk/core.Pollable, and sdk/core.WebhookCapable.
//
// Usage:
//
//	cfg := github.DefaultConfig()
//	cfg.Token = os.Getenv("GITHUB_TOKEN")
//	cfg.Repo = "owner/repo"
//
//	a := github.New(cfg)
//	core.Register(a)
package github

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// Compile-time interface assertions.
var (
	_ core.Adapter        = (*Adapter)(nil)
	_ core.Pollable       = (*Adapter)(nil)
	_ core.WebhookCapable = (*Adapter)(nil)
)

// Adapter implements core.Adapter, core.Pollable, and core.WebhookCapable for GitHub.
type Adapter struct {
	config *Config
	client *Client // optional client injected via WithAdapterClient; nil means NewPoller constructs one from config.Token
}

// AdapterOption configures an Adapter created via New.
type AdapterOption func(*Adapter)

// WithAdapterClient injects a pre-built *Client for NewPoller to use instead
// of constructing one from Config.Token. Use this to route the adapter's
// internal poller, MergeWaiter, and board sync/source through a client built
// with NewClientWithTokenFunc, so long-lived hosts under GitHub App auth
// (installation tokens expire hourly) don't freeze a boot-time token inside
// the adapter. A nil client is a no-op: NewPoller falls back to constructing
// a static-token client from Config.Token, matching pre-injection behavior.
func WithAdapterClient(client *Client) AdapterOption {
	return func(a *Adapter) {
		if client != nil {
			a.client = client
		}
	}
}

// New creates a new GitHub adapter from the given configuration.
// Call core.Register(github.New(cfg)) to make it available via the global registry.
// By default NewPoller constructs its client from cfg.Token; pass
// WithAdapterClient to inject a client instead (e.g. a TokenFunc-backed
// client for rotating credentials).
func New(cfg *Config, opts ...AdapterOption) *Adapter {
	a := &Adapter{config: cfg}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Name returns the adapter identifier.
func (a *Adapter) Name() string { return "github" }

// WebhookSource returns the source key used for webhook routing.
func (a *Adapter) WebhookSource() string { return "github" }

// NewPoller creates a core.Poller backed by the GitHub polling mechanism.
// It bridges core.PollerDeps (IssueHandler, ProcessedStore, OnPRCreated) into
// the GitHub-specific Poller, converting *Issue ↔ core.IssueEvent at the boundary.
func (a *Adapter) NewPoller(deps core.PollerDeps) core.Poller {
	client := a.client
	if client == nil {
		client = NewClient(a.config.Token)
	}

	label := a.config.TriggerLabel
	if label == "" {
		label = "pilot"
	}

	interval := 30 * time.Second
	if a.config.Polling != nil && a.config.Polling.Interval > 0 {
		interval = a.config.Polling.Interval
	}

	repo := a.config.Repo
	if repo == "" {
		repo = "unknown/unknown"
	}

	opts := []PollerOption{
		WithOnIssueWithResult(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			ev := toIssueEvent(issue, repo)
			res, err := deps.Handler.HandleIssue(ctx, ev)
			if err != nil {
				return nil, err
			}
			if res == nil {
				return &IssueResult{Success: true}, nil
			}
			return &IssueResult{
				Success:    res.Success,
				PRNumber:   res.PRNumber,
				PRURL:      res.PRURL,
				HeadSHA:    res.HeadSHA,
				BranchName: res.BranchName,
			}, nil
		}),
	}

	if deps.ProcessedStore != nil {
		opts = append(opts, WithProcessedStore(deps.ProcessedStore))
	}
	if deps.MaxConcurrent > 0 {
		opts = append(opts, WithMaxConcurrent(deps.MaxConcurrent))
	}
	if deps.OnPRCreated != nil {
		fn := deps.OnPRCreated
		opts = append(opts, WithOnPRCreated(func(prNumber int, prURL string, issueNumber int, headSHA string, branchName string, issueNodeID string) {
			fn(core.PRCreatedEvent{
				PRNumber:    prNumber,
				PRURL:       prURL,
				IssueID:     strconv.Itoa(issueNumber),
				HeadSHA:     headSHA,
				BranchName:  branchName,
				IssueNodeID: issueNodeID,
			})
		}))
	}
	if deps.TaskChecker != nil {
		opts = append(opts, WithTaskChecker(deps.TaskChecker))
	}
	if deps.ExecutionChecker != nil {
		opts = append(opts, WithExecutionChecker(deps.ExecutionChecker, deps.ProjectPath))
	}
	if deps.PreFlightJudge != nil {
		opts = append(opts, WithPreFlightJudge(deps.PreFlightJudge))
	}
	if deps.ExecutionSaver != nil {
		opts = append(opts, WithExecutionSaver(deps.ExecutionSaver))
	}
	if deps.IssueMetricsRecorder != nil {
		opts = append(opts, WithIssueMetricsRecorder(deps.IssueMetricsRecorder))
	}
	if deps.RateLimitScheduler != nil {
		opts = append(opts, WithRateLimitScheduler(deps.RateLimitScheduler))
	}
	if deps.Logger != nil {
		opts = append(opts, WithPollerLogger(deps.Logger))
	}
	if deps.PollerMetrics != nil {
		opts = append(opts, WithPollerMetrics(deps.PollerMetrics))
	}
	if deps.BoardSyncAuthAlert != nil {
		opts = append(opts, WithBoardSyncAuthAlert(deps.BoardSyncAuthAlert))
	}

	// Board layer is config-driven, not a PollerDeps hook: construct the board
	// source/sync from ProjectBoardConfig so hosts get board mode by config alone.
	if pb := a.config.ProjectBoard; pb != nil && pb.Enabled {
		parts := strings.SplitN(repo, "/", 2)
		if len(parts) == 2 {
			ownerName, repoName := parts[0], parts[1]
			if bs := NewProjectBoardSync(client, pb, ownerName); bs != nil && pb.GetStatuses().InProgress != "" {
				opts = append(opts, WithBoardSync(bs, pb.GetStatuses().InProgress))
			}
			if pb.SourceEnabled {
				opts = append(opts, WithProjectBoardSource(NewProjectBoardSource(client, pb, ownerName, repoName)))
			}
		}
	}

	// NewPoller can only fail on invalid repo format; DefaultConfig guarantees "unknown/unknown"
	// as a fallback so this is always valid after the check above.
	poller, err := NewPoller(client, repo, label, interval, opts...)
	if err != nil {
		// If repo is still malformed, return a no-op poller so the adapter is
		// usable in registries even if misconfigured.
		return &nopPoller{err: err}
	}
	return poller
}

// toIssueEvent converts a GitHub Issue to a normalized core.IssueEvent.
// The issue is expected to have already been sanitized in place (see
// sanitizeIssueInPlace, called in the poll/webhook path before dispatch).
func toIssueEvent(issue *Issue, repo string) core.IssueEvent {
	// Extract owner from "owner/repo"
	parts := strings.SplitN(repo, "/", 2)
	projectID := repo
	if len(parts) == 2 {
		projectID = parts[1]
	}

	labelNames := make([]string, 0, len(issue.Labels))
	for _, l := range issue.Labels {
		labelNames = append(labelNames, l.Name)
	}

	return core.IssueEvent{
		Action:     "created",
		IssueID:    strconv.Itoa(issue.Number),
		SequenceID: "GH-" + strconv.Itoa(issue.Number),
		Title:      issue.Title,
		Body:       issue.Body,
		Labels:     labelNames,
		Priority:   core.NormalizePriority(int(extractPriority(issue.Labels))),
		ProjectID:  projectID,
	}
}

// nopPoller is returned when adapter configuration is invalid.
type nopPoller struct {
	err error
}

func (n *nopPoller) Start(_ context.Context) error { return n.err }
