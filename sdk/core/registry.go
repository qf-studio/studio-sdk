// Package core defines the shared contract surface for Studio SDK
// integrations: the Adapter interfaces, normalized event types, and the
// global adapter registry.
//
// It is a leaf package with no dependencies on any other SDK package so
// that every integration can import it without creating a cycle. It has
// ZERO dependencies on Pilot internals — consuming applications wire their
// own services in via the interfaces and callbacks declared here.
package core

import (
	"context"
	"sync"
	"time"
)

// Adapter is the common interface all integration adapters implement.
// New adapters register via Register() in their init() function.
type Adapter interface {
	// Name returns the adapter identifier (e.g. "jira", "linear", "github").
	Name() string
}

// Pollable is implemented by adapters that support polling for new issues.
type Pollable interface {
	Adapter

	// NewPoller creates a poller for this adapter using the given options.
	NewPoller(opts PollerDeps) Poller
}

// WebhookCapable is implemented by adapters that can receive webhooks.
type WebhookCapable interface {
	Adapter

	// WebhookSource returns the source key for webhook routing (e.g. "jira").
	WebhookSource() string
}

// Poller abstracts a polling loop that discovers new issues.
type Poller interface {
	Start(ctx context.Context) error
}

// IssueEvent is the normalized issue event emitted by all adapters.
type IssueEvent struct {
	Action     string // "created", "updated"
	IssueID    string // Adapter-specific primary ID (UUID, GID, node ID, etc.)
	SequenceID string // Human-readable number/key within the project (e.g. "42" for Plane, "PROJ-42" for Jira). Used in branch names and PR descriptions. Empty when the adapter has no such concept.
	Title      string
	Body       string
	Labels     []string
	Priority   string // Normalized priority: "urgent", "high", "medium", "low", "none", or "" if the adapter has no priority concept.
	ProjectID  string
}

// IssueResult is the normalized result from processing an issue.
type IssueResult struct {
	Success    bool
	Skipped    bool   // true when the handler decided not to process the issue
	SkipReason string // reason constant from sdk/util/skipreason; populated when Skipped is true
	PRNumber   int
	PRURL      string
	HeadSHA    string
	BranchName string
	Error      error
}

// IssueHandler is the unified contract for processing a discovered issue.
// It replaces the per-adapter WithOnIssue / WithOnIssueWithResult callback
// variants that diverged across the original Pilot adapters. A nil
// *IssueResult with a nil error means "handled, no PR produced".
type IssueHandler interface {
	HandleIssue(ctx context.Context, ev IssueEvent) (*IssueResult, error)
}

// IssueHandlerFunc adapts a plain function to the IssueHandler interface.
type IssueHandlerFunc func(ctx context.Context, ev IssueEvent) (*IssueResult, error)

// HandleIssue implements IssueHandler.
func (f IssueHandlerFunc) HandleIssue(ctx context.Context, ev IssueEvent) (*IssueResult, error) {
	return f(ctx, ev)
}

// PRCreatedEvent is the normalized payload emitted when an adapter creates
// a pull/merge request for an issue. It unifies the divergent callback
// signatures across the original Pilot adapters (GitHub carried an extra
// node ID; GitLab/Azure/Plane did not). Adapters without a node ID leave
// IssueNodeID empty. IssueID is a string to cover int- and GID-keyed
// trackers uniformly.
type PRCreatedEvent struct {
	PRNumber    int
	PRURL       string
	IssueID     string
	HeadSHA     string
	BranchName  string
	IssueNodeID string // optional; empty for adapters that have no node ID
}

// ProcessedStore is the generic interface for tracking which issues have
// been processed across restarts. Source identifies the adapter (e.g.
// "github"), repo identifies the repository namespace (e.g. "owner/repo"),
// and issueID is the adapter-native identifier converted to a string.
// Tracker-style adapters pass repo="".
type ProcessedStore interface {
	Mark(source, repo, issueID string) error
	Unmark(source, repo, issueID string) error
	IsProcessed(source, repo, issueID string) (bool, error)
	Load(source, repo string) (map[string]time.Time, error)
}

// ActiveExecutionLister reports the task IDs of executions that are currently
// active, so stale-branch / stale-label cleanup never removes a branch or label
// an execution is still using. Consuming applications implement this against
// their own execution store; the SDK never constructs it.
type ActiveExecutionLister interface {
	// ListActiveTaskIDs returns the task IDs (e.g. "GL-123", "AZ-456") of
	// executions currently in flight.
	ListActiveTaskIDs(ctx context.Context) ([]string, error)
}

// PollerDeps provides shared infrastructure to adapter pollers. Consuming
// applications supply these — the SDK never constructs them itself.
type PollerDeps struct {
	// ProcessedStore deduplicates issues across restarts.
	ProcessedStore ProcessedStore
	// MaxConcurrent caps concurrent issue handling. Zero means the
	// adapter's own default.
	MaxConcurrent int
	// Handler processes each discovered issue.
	Handler IssueHandler
	// OnPRCreated fires after an adapter opens a PR/MR for an issue.
	OnPRCreated func(ev PRCreatedEvent)
}

// --- Registry ---

var (
	registryMu sync.RWMutex
	registry   = map[string]Adapter{}
)

// Register adds an adapter to the global registry.
// Typically called from an adapter package's init() function.
func Register(a Adapter) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[a.Name()] = a
}

// Get returns a registered adapter by name, or nil if not found.
func Get(name string) Adapter {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[name]
}

// All returns a copy of all registered adapters.
func All() map[string]Adapter {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make(map[string]Adapter, len(registry))
	for k, v := range registry {
		out[k] = v
	}
	return out
}

// Reset clears the registry. Used for testing only.
func Reset() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = map[string]Adapter{}
}
