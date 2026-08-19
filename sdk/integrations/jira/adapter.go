// Package jira provides a Studio SDK adapter for Jira (https://www.atlassian.com/software/jira).
// It implements sdk/core.Adapter, sdk/core.Pollable, and sdk/core.WebhookCapable.
//
// Usage:
//
//	cfg := jira.DefaultConfig()
//	cfg.BaseURL = os.Getenv("JIRA_BASE_URL")
//	cfg.Username = os.Getenv("JIRA_USERNAME")
//	cfg.APIToken = os.Getenv("JIRA_API_TOKEN")
//
//	a := jira.New(cfg)
//	core.Register(a)
package jira

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

// Adapter implements core.Adapter, core.Pollable, and core.WebhookCapable for Jira.
type Adapter struct {
	config *Config
}

// New creates a new Jira adapter from the given configuration.
// Call core.Register(jira.New(cfg)) to make it available via the global registry.
func New(cfg *Config) *Adapter {
	return &Adapter{config: cfg}
}

// Name returns the adapter identifier.
func (a *Adapter) Name() string { return "jira" }

// WebhookSource returns the source key used for webhook routing.
func (a *Adapter) WebhookSource() string { return "jira" }

// NewPoller creates a core.Poller backed by the Jira polling mechanism.
// It bridges core.PollerDeps (IssueHandler, ProcessedStore, OnPRCreated) into
// the Jira-specific Poller, converting *Issue ↔ core.IssueEvent at the boundary.
func (a *Adapter) NewPoller(deps core.PollerDeps) core.Poller {
	if !a.config.Enabled {
		return &nopPoller{}
	}

	client := NewClient(a.config.BaseURL, a.config.Username, a.config.APIToken, a.config.Platform)

	interval := 30 * time.Second
	if a.config.Polling != nil && a.config.Polling.Interval > 0 {
		interval = a.config.Polling.Interval
	}

	opts := []PollerOption{
		WithOnJiraIssue(func(ctx context.Context, issue *Issue) (*IssueResult, error) {
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
		opts = append(opts, WithOnPRCreated(func(ev core.PRCreatedEvent) {
			fn(ev)
		}))
	}

	return NewPoller(client, a.config, interval, opts...)
}

// toIssueEvent converts a Jira Issue to a normalized core.IssueEvent.
// The issue is expected to have already been sanitized in place (see
// sanitizeIssueInPlace, called in the poll path before dispatch).
func toIssueEvent(issue *Issue) core.IssueEvent {
	var priority int
	if issue.Fields.Priority != nil {
		priority = int(PriorityFromJira(issue.Fields.Priority.Name))
	}

	labels := issue.Fields.Labels
	if labels == nil {
		labels = []string{}
	}

	return core.IssueEvent{
		Action:     "created",
		IssueID:    issue.ID,
		SequenceID: "JIRA-" + issue.Key,
		Title:      issue.Fields.Summary,
		Body:       string(issue.Fields.Description),
		Labels:     labels,
		Priority:   core.NormalizePriority(priority),
		ProjectID:  issue.Fields.Project.Key,
	}
}

// nopPoller is returned when adapter configuration is invalid or disabled.
type nopPoller struct{}

func (n *nopPoller) Start(_ context.Context) error { return nil }
