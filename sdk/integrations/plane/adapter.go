package plane

import (
	"context"
	"log/slog"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// Adapter implements core.Adapter, core.Pollable, and core.WebhookCapable for Plane.so.
// Register it at startup via core.Register(plane.NewAdapter(...)).
type Adapter struct {
	client   *Client
	config   *Config
	interval time.Duration
	logger   *slog.Logger
}

// AdapterOption configures an Adapter.
type AdapterOption func(*Adapter)

// WithAdapterLogger sets the logger injected into pollers and webhook handlers
// created by this adapter.
func WithAdapterLogger(logger *slog.Logger) AdapterOption {
	return func(a *Adapter) {
		a.logger = logger
	}
}

// NewAdapter creates a new Plane adapter.
// client and config must be non-nil. interval controls the polling frequency.
func NewAdapter(client *Client, config *Config, interval time.Duration, opts ...AdapterOption) *Adapter {
	a := &Adapter{
		client:   client,
		config:   config,
		interval: interval,
		logger:   slog.Default(),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Name returns the adapter identifier. Implements core.Adapter.
func (a *Adapter) Name() string { return "plane" }

// WebhookSource returns the routing key used to dispatch webhooks to this adapter.
// Implements core.WebhookCapable.
func (a *Adapter) WebhookSource() string { return "plane" }

// NewPoller constructs a Plane Poller wired to the provided core.PollerDeps.
// *WorkItem → core.IssueEvent translation happens here, at the adapter boundary.
// Implements core.Pollable.
func (a *Adapter) NewPoller(deps core.PollerDeps) core.Poller {
	opts := []PollerOption{
		WithPollerLogger(a.logger.With(slog.String("component", "plane-poller"))),
	}

	if deps.ProcessedStore != nil {
		opts = append(opts, WithProcessedStore(deps.ProcessedStore))
	}

	if deps.MaxConcurrent > 0 {
		opts = append(opts, WithMaxConcurrent(deps.MaxConcurrent))
	}

	if deps.Handler != nil {
		handler := deps.Handler
		opts = append(opts, WithOnIssue(func(ctx context.Context, item *WorkItem) (*core.IssueResult, error) {
			ev := workItemToIssueEvent(item)
			return handler.HandleIssue(ctx, ev)
		}))
	}

	if deps.OnPRCreated != nil {
		onPR := deps.OnPRCreated
		opts = append(opts, WithOnPRCreated(func(ev core.PRCreatedEvent) {
			onPR(ev)
		}))
	}

	return NewPoller(a.client, a.config, a.interval, opts...)
}

// workItemToIssueEvent maps a Plane WorkItem to a core.IssueEvent.
// LabelIDs are carried as-is (UUID strings); consuming handlers may resolve
// them to human names if needed.
func workItemToIssueEvent(item *WorkItem) core.IssueEvent {
	labels := make([]string, len(item.LabelIDs))
	copy(labels, item.LabelIDs)
	return core.IssueEvent{
		Action:    "created",
		IssueID:   item.ID,
		Title:     item.Name,
		Body:      item.Description,
		Labels:    labels,
		ProjectID: item.ProjectID,
	}
}

// Ensure Adapter satisfies the SDK interfaces at compile time.
var (
	_ core.Adapter        = (*Adapter)(nil)
	_ core.Pollable       = (*Adapter)(nil)
	_ core.WebhookCapable = (*Adapter)(nil)
)
