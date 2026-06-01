// Package gitlab provides a Studio SDK adapter for GitLab (https://gitlab.com).
// It implements sdk/core.Adapter, sdk/core.Pollable, and sdk/core.WebhookCapable.
//
// Usage:
//
//	cfg := gitlab.DefaultConfig()
//	cfg.Token = os.Getenv("GITLAB_TOKEN")
//	cfg.Project = "namespace/project"
//
//	a := gitlab.New(cfg)
//	core.Register(a)
package gitlab

import (
	"context"
	"strconv"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// Compile-time interface assertions.
var (
	_ core.Adapter        = (*Adapter)(nil)
	_ core.Pollable       = (*Adapter)(nil)
	_ core.WebhookCapable = (*Adapter)(nil)
)

// Adapter implements core.Adapter, core.Pollable, and core.WebhookCapable for GitLab.
type Adapter struct {
	config *Config
}

// New creates a new GitLab adapter from the given configuration.
// Call core.Register(gitlab.New(cfg)) to make it available via the global registry.
func New(cfg *Config) *Adapter {
	return &Adapter{config: cfg}
}

// Name returns the adapter identifier.
func (a *Adapter) Name() string { return "gitlab" }

// WebhookSource returns the source key used for webhook routing.
func (a *Adapter) WebhookSource() string { return "gitlab" }

// NewPoller creates a core.Poller backed by the GitLab polling mechanism.
// It bridges core.PollerDeps (IssueHandler, ProcessedStore, OnPRCreated) into
// the GitLab-specific Poller, converting *Issue ↔ core.IssueEvent at the boundary.
func (a *Adapter) NewPoller(deps core.PollerDeps) core.Poller {
	var client *Client
	if a.config.BaseURL != "" && a.config.BaseURL != "https://gitlab.com" {
		client = NewClientWithBaseURL(a.config.Token, a.config.Project, a.config.BaseURL)
	} else {
		client = NewClient(a.config.Token, a.config.Project)
	}

	label := a.config.PilotLabel
	if label == "" {
		label = "pilot"
	}

	interval := 30 * time.Second
	if a.config.Polling != nil && a.config.Polling.Interval > 0 {
		interval = a.config.Polling.Interval
	}

	opts := []PollerOption{
		WithOnIssueWithResult(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
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
				MRNumber:   res.PRNumber,
				MRURL:      res.PRURL,
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
		opts = append(opts, WithOnMRCreated(func(mrIID int, mrURL string, issueIID int, headSHA string, branchName string) {
			fn(core.PRCreatedEvent{
				PRNumber:   mrIID,
				PRURL:      mrURL,
				IssueID:    strconv.Itoa(issueIID),
				HeadSHA:    headSHA,
				BranchName: branchName,
			})
		}))
	}

	return NewPoller(client, label, interval, opts...)
}

// toIssueEvent converts a GitLab Issue to a normalized core.IssueEvent.
func toIssueEvent(issue *Issue) core.IssueEvent {
	return core.IssueEvent{
		Action:     "created",
		IssueID:    strconv.Itoa(issue.IID),
		SequenceID: strconv.Itoa(issue.IID),
		Title:      issue.Title,
		Body:       issue.Description,
		Labels:     issue.Labels,
		ProjectID:  strconv.Itoa(issue.ProjectID),
	}
}
