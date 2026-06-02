// Package azuredevops provides a Studio SDK adapter for Azure DevOps.
// It implements sdk/core.Adapter, sdk/core.Pollable, and sdk/core.WebhookCapable.
//
// Usage:
//
//	cfg := azuredevops.DefaultConfig()
//	cfg.PAT = os.Getenv("AZURE_DEVOPS_PAT")
//	cfg.Organization = "myorg"
//	cfg.Project = "myproject"
//
//	a := azuredevops.New(cfg)
//	core.Register(a)
package azuredevops

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

// Adapter implements core.Adapter, core.Pollable, and core.WebhookCapable for Azure DevOps.
type Adapter struct {
	config *Config
}

// New creates a new Azure DevOps adapter from the given configuration.
// Call core.Register(azuredevops.New(cfg)) to make it available via the global registry.
func New(cfg *Config) *Adapter {
	return &Adapter{config: cfg}
}

// Name returns the adapter identifier.
func (a *Adapter) Name() string { return "azuredevops" }

// WebhookSource returns the source key used for webhook routing.
func (a *Adapter) WebhookSource() string { return "azuredevops" }

// NewPoller creates a core.Poller backed by the Azure DevOps polling mechanism.
// It bridges core.PollerDeps (IssueHandler, ProcessedStore, OnPRCreated) into
// the Azure DevOps-specific Poller, converting *WorkItem ↔ core.IssueEvent at the boundary.
func (a *Adapter) NewPoller(deps core.PollerDeps) core.Poller {
	var client *Client
	if a.config.BaseURL != "" && a.config.BaseURL != "https://dev.azure.com" {
		client = NewClientWithBaseURL(a.config.PAT, a.config.Organization, a.config.Project, a.config.BaseURL)
	} else {
		client = NewClient(a.config.PAT, a.config.Organization, a.config.Project)
	}
	if a.config.Repository != "" {
		client.repository = a.config.Repository
	}

	tag := a.config.TriggerLabel
	if tag == "" {
		tag = "pilot"
	}

	interval := 30 * time.Second
	if a.config.Polling != nil && a.config.Polling.Interval > 0 {
		interval = a.config.Polling.Interval
	}

	opts := []PollerOption{
		WithOnWorkItemWithResult(func(ctx context.Context, wi *WorkItem) (*WorkItemResult, error) {
			ev := toIssueEvent(wi)
			res, err := deps.Handler.HandleIssue(ctx, ev)
			if err != nil {
				return nil, err
			}
			if res == nil {
				return &WorkItemResult{Success: true}, nil
			}
			return &WorkItemResult{
				Success:    res.Success,
				PRNumber:   res.PRNumber,
				PRURL:      res.PRURL,
				HeadSHA:    res.HeadSHA,
				BranchName: res.BranchName,
			}, nil
		}),
	}

	if a.config.WorkItemTypes != nil {
		opts = append(opts, WithWorkItemTypes(a.config.WorkItemTypes))
	}
	if deps.ProcessedStore != nil {
		opts = append(opts, WithProcessedStore(deps.ProcessedStore))
	}
	if deps.MaxConcurrent > 0 {
		opts = append(opts, WithMaxConcurrent(deps.MaxConcurrent))
	}
	if deps.OnPRCreated != nil {
		fn := deps.OnPRCreated
		opts = append(opts, WithOnPRCreated(func(prID int, prURL string, workItemID int, headSHA string, branchName string) {
			fn(core.PRCreatedEvent{
				PRNumber:   prID,
				PRURL:      prURL,
				IssueID:    strconv.Itoa(workItemID),
				HeadSHA:    headSHA,
				BranchName: branchName,
			})
		}))
	}

	return NewPoller(client, tag, interval, opts...)
}

// toIssueEvent converts an Azure DevOps WorkItem to a normalized core.IssueEvent.
// The work item is expected to have already been sanitized in place (see
// sanitizeWorkItemFields, called in the poll/webhook path before dispatch).
func toIssueEvent(wi *WorkItem) core.IssueEvent {
	return core.IssueEvent{
		Action:     "created",
		IssueID:    strconv.Itoa(wi.ID),
		SequenceID: "AZDO-" + strconv.Itoa(wi.ID),
		Title:      wi.GetTitle(),
		Body:       wi.GetDescription(),
		Labels:     wi.GetTags(),
		Priority:   core.NormalizePriority(int(wi.GetPriority())),
		ProjectID:  wi.GetWorkItemType(),
	}
}
