// Package asana provides a Studio SDK adapter for Asana (https://asana.com).
// It implements sdk/core.Adapter, sdk/core.Pollable, and sdk/core.WebhookCapable.
//
// Usage:
//
//	cfg := asana.DefaultConfig()
//	cfg.AccessToken = os.Getenv("ASANA_ACCESS_TOKEN")
//	cfg.WorkspaceID = os.Getenv("ASANA_WORKSPACE_ID")
//
//	a := asana.New(cfg)
//	core.Register(a)
package asana

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

// Adapter implements core.Adapter, core.Pollable, and core.WebhookCapable for Asana.
type Adapter struct {
	config *Config
}

// New creates a new Asana adapter from the given configuration.
// Call core.Register(asana.New(cfg)) to make it available via the global registry.
func New(cfg *Config) *Adapter {
	return &Adapter{config: cfg}
}

// Name returns the adapter identifier.
func (a *Adapter) Name() string { return "asana" }

// WebhookSource returns the source key used for webhook routing.
func (a *Adapter) WebhookSource() string { return "asana" }

// NewPoller creates a core.Poller backed by the Asana polling mechanism.
// It bridges core.PollerDeps (IssueHandler, ProcessedStore) into the Asana-specific
// Poller, converting *Task ↔ core.IssueEvent at the boundary.
func (a *Adapter) NewPoller(deps core.PollerDeps) core.Poller {
	if !a.config.Enabled {
		return &nopPoller{}
	}

	client := NewClient(a.config.AccessToken, a.config.WorkspaceID)

	interval := 30 * time.Second
	if a.config.Polling != nil && a.config.Polling.Interval > 0 {
		interval = a.config.Polling.Interval
	}

	opts := []PollerOption{
		WithOnAsanaTask(func(ctx context.Context, task *Task) (*TaskResult, error) {
			ev := toIssueEvent(task)
			res, err := deps.Handler.HandleIssue(ctx, ev)
			if err != nil {
				return nil, err
			}
			if res == nil {
				return &TaskResult{Success: true}, nil
			}
			return &TaskResult{
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

	return NewPoller(client, a.config, interval, opts...)
}

// toIssueEvent converts an Asana Task to a normalized core.IssueEvent.
// The task is expected to have already been sanitized in place (see
// sanitizeTaskInPlace, called in the poll path before dispatch).
func toIssueEvent(task *Task) core.IssueEvent {
	priority := int(PriorityFromTags(task.Tags))

	labels := make([]string, 0, len(task.Tags))
	for _, tag := range task.Tags {
		labels = append(labels, tag.Name)
	}

	projectID := ""
	if len(task.Projects) > 0 {
		projectID = task.Projects[0].GID
	}

	return core.IssueEvent{
		Action:     "created",
		IssueID:    task.GID,
		SequenceID: "ASANA-" + task.GID,
		Title:      task.Name,
		Body:       task.Notes,
		Labels:     labels,
		Priority:   core.NormalizePriority(priority),
		ProjectID:  projectID,
	}
}

// nopPoller is returned when adapter configuration is disabled.
type nopPoller struct{}

func (n *nopPoller) Start(_ context.Context) error { return nil }
