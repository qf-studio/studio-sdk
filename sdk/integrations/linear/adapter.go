// Package linear provides a Studio SDK adapter for Linear (https://linear.app).
// It implements sdk/core.Adapter, sdk/core.Pollable, and sdk/core.WebhookCapable.
//
// Usage:
//
//	cfg := linear.DefaultConfig()
//	cfg.APIKey = os.Getenv("LINEAR_API_KEY")
//	cfg.TeamID = os.Getenv("LINEAR_TEAM_ID")
//
//	a := linear.New(cfg)
//	core.Register(a)
package linear

import (
	"context"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// Compile-time interface assertions.
var (
	_ core.Adapter        = (*Adapter)(nil)
	_ core.Pollable       = (*Adapter)(nil)
	_ core.WebhookCapable = (*Adapter)(nil)
)

// Adapter implements core.Adapter, core.Pollable, and core.WebhookCapable for Linear.
type Adapter struct {
	config *Config
}

// New creates a new Linear adapter from the given configuration.
// Call core.Register(linear.New(cfg)) to make it available via the global registry.
func New(cfg *Config) *Adapter {
	return &Adapter{config: cfg}
}

// Name returns the adapter identifier.
func (a *Adapter) Name() string { return "linear" }

// WebhookSource returns the source key used for webhook routing.
func (a *Adapter) WebhookSource() string { return "linear" }

// NewPoller creates a core.Poller backed by the Linear polling mechanism.
// It bridges core.PollerDeps (IssueHandler, ProcessedStore, OnPRCreated) into
// the Linear-specific Poller, converting *Issue ↔ core.IssueEvent at the boundary.
func (a *Adapter) NewPoller(deps core.PollerDeps) core.Poller {
	workspaces := a.config.GetWorkspaces()
	if len(workspaces) == 0 {
		return &nopPoller{}
	}

	ws := workspaces[0]
	client := NewClient(ws.APIKey)

	label := ws.TriggerLabel
	if label == "" {
		label = "pilot"
	}

	interval := 30 * time.Second
	if a.config.Polling != nil && a.config.Polling.Interval > 0 {
		interval = a.config.Polling.Interval
	}

	opts := []PollerOption{
		WithOnLinearIssue(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
			ev := toIssueEvent(issue)
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
		opts = append(opts, WithOnPRCreated(func(prNumber int, prURL string, _ int, headSHA string, branchName string, issueNodeID string) {
			fn(core.PRCreatedEvent{
				PRNumber:   prNumber,
				PRURL:      prURL,
				IssueID:    issueNodeID,
				HeadSHA:    headSHA,
				BranchName: branchName,
			})
		}))
	}

	return NewPoller(client, ws, interval, opts...)
}

// toIssueEvent converts a Linear Issue to a normalized core.IssueEvent.
// The issue is expected to have already been sanitized in place (see
// sanitizeIssueInPlace, called in the poll/webhook path before dispatch).
func toIssueEvent(issue *Issue) core.IssueEvent {
	labelNames := make([]string, 0, len(issue.Labels))
	for _, l := range issue.Labels {
		labelNames = append(labelNames, l.Name)
	}

	return core.IssueEvent{
		Action:     "created",
		IssueID:    issue.ID,
		SequenceID: "LIN-" + issue.Identifier,
		Title:      issue.Title,
		Body:       issue.Description,
		Labels:     labelNames,
		Priority:   core.NormalizePriority(issue.Priority),
		ProjectID:  issue.Team.ID,
	}
}

// nopPoller is returned when adapter configuration is invalid.
type nopPoller struct{}

func (n *nopPoller) Start(_ context.Context) error { return nil }
