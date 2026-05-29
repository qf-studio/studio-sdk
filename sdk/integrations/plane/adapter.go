// Package plane provides a Studio SDK adapter for Plane.so (https://plane.so).
// It implements sdk/core.Adapter, sdk/core.Pollable, and sdk/core.WebhookCapable.
//
// Usage:
//
//	cfg := plane.DefaultConfig()
//	cfg.BaseURL = "https://api.plane.so"
//	cfg.APIKey = os.Getenv("PLANE_API_KEY")
//	cfg.WorkspaceSlug = "my-workspace"
//	cfg.ProjectIDs = []string{"proj-uuid"}
//
//	a := plane.New(cfg)
//	core.Register(a)
package plane

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

// Adapter implements core.Adapter, core.Pollable, and core.WebhookCapable for Plane.so.
type Adapter struct {
	config *Config
}

// New creates a new Plane adapter from the given configuration.
// Call core.Register(plane.New(cfg)) to make it available via the global registry.
func New(cfg *Config) *Adapter {
	return &Adapter{config: cfg}
}

// Name returns the adapter identifier.
func (a *Adapter) Name() string { return "plane" }

// WebhookSource returns the source key used for webhook routing.
func (a *Adapter) WebhookSource() string { return "plane" }

// NewPoller creates a core.Poller backed by the Plane polling mechanism.
// It bridges core.PollerDeps (IssueHandler, ProcessedStore, OnPRCreated) into
// the Plane-specific Poller, converting *WorkItem ↔ core.IssueEvent at the boundary.
func (a *Adapter) NewPoller(deps core.PollerDeps) core.Poller {
	client := NewClient(
		a.config.BaseURL,
		a.config.APIKey,
		WithWorkspaceSlug(a.config.WorkspaceSlug),
	)

	interval := 5 * time.Minute
	if a.config.Polling != nil && a.config.Polling.Interval > 0 {
		interval = a.config.Polling.Interval
	}

	opts := []PollerOption{
		WithOnIssue(func(ctx context.Context, item *WorkItem) (*core.IssueResult, error) {
			ev := toIssueEvent(item)
			return deps.Handler.HandleIssue(ctx, ev)
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

// toIssueEvent converts a Plane WorkItem to a normalized core.IssueEvent.
// Labels are passed as UUID strings — Plane identifies labels by UUID, not name.
func toIssueEvent(item *WorkItem) core.IssueEvent {
	seqID := ""
	if item.SequenceID != 0 {
		seqID = strconv.Itoa(item.SequenceID)
	}
	return core.IssueEvent{
		Action:     "created",
		IssueID:    item.ID,
		SequenceID: seqID,
		Title:      item.Name,
		Body:       item.Description,
		Labels:     item.LabelIDs,
		Priority:   priorityString(item.Priority),
		ProjectID:  item.ProjectID,
	}
}
