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
	"log/slog"
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
	SequenceID string // Human-readable, provider-prefixed key used in branch names and PR descriptions (e.g. "GL-42" GitLab, "AZDO-42" Azure DevOps, "PLANE-42" Plane, "PROJ-42" Jira). The prefix keeps IDs distinct across providers in a shared host. Empty when the adapter has no such concept.
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

// TaskChecker reports whether a task is currently queued or in-progress in
// the consuming application's execution pipeline. Pollers consult it during
// retry-grace evaluation so an issue whose task is still running is not
// re-dispatched.
type TaskChecker interface {
	IsTaskQueued(taskID string) bool
}

// ExecutionChecker verifies whether a completed execution record exists for a
// task, preventing re-dispatch when a tracker-side "done" marker failed to
// apply. InvalidateCompletion deletes a stale completed record so an explicit
// retry is not silently no-op'd.
type ExecutionChecker interface {
	HasCompletedExecution(taskID, projectPath string) (bool, error)
	InvalidateCompletion(taskID, projectPath string) error
}

// Verdict is the result of a pre-flight judgment on an issue.
type Verdict struct {
	Accepted   bool
	Decision   string
	Reason     string
	Confidence float64
}

// PreFlightJudger evaluates issues before dispatch so pollers do not burn
// worker slots on vague, ambiguous, or otherwise unactionable issues.
type PreFlightJudger interface {
	JudgeIssue(ctx context.Context, title, body, repoContext string) (Verdict, error)
}

// ExecutionSaver persists pre-flight rejection records for observability.
type ExecutionSaver interface {
	SaveDeclinedExecution(taskID, projectPath, status, reason string) error
}

// DeclinedExecutionRecord is the repo-aware evolution of the arguments to
// ExecutionSaver.SaveDeclinedExecution. RepoOwner/RepoName carry the issue's
// actual repo identity, distinct from ProjectPath: when a single local
// checkout (ProjectPath) is polled against multiple repos, ProjectPath alone
// cannot disambiguate which repo a declined issue came from, so records keyed
// only on it collide across projects (see GH-4833 — the same shared-path
// collision that corrupted canary attribution in the consumer's metrics).
type DeclinedExecutionRecord struct {
	TaskID      string
	ProjectPath string
	Status      string
	Reason      string
	RepoOwner   string
	RepoName    string
}

// ExecutionSaverV2 is the repo-aware evolution of ExecutionSaver. Consumers
// that implement it receive SaveDeclinedExecutionRecord calls carrying the
// issue's repo identity; consumers that only implement ExecutionSaver keep
// receiving SaveDeclinedExecution calls unchanged. Pollers detect which
// interface a wired ExecutionSaver satisfies at the call site and prefer
// ExecutionSaverV2 when available, so existing consumers compile and behave
// exactly as before without modification.
type ExecutionSaverV2 interface {
	ExecutionSaver
	SaveDeclinedExecutionRecord(rec DeclinedExecutionRecord) error
}

// IssueMetricsRecorder records issue processing outcomes (e.g. "rate_limited").
type IssueMetricsRecorder interface {
	RecordIssueProcessed(result string)
}

// PollerMetricsRecorder records poller dispatch/skip counters and the
// unsourced-labeled-issues gauge (GH-4488). Defined locally (rather than
// importing sdk/util/skipreason) to keep this leaf package free of
// dependencies on other SDK packages; any value satisfying
// skipreason.PollerMetricsRecorder's identical method set already satisfies
// this interface.
type PollerMetricsRecorder interface {
	RecordPollerSkipped(repo, reason string)
	RecordPollerDispatched(repo string)
	RecordPollerDeferredScopeOverlap(repo string)
	RecordUnsourcedLabeledIssues(repo string, count int)
}

// RateLimitScheduler lets the consuming application classify a handler error
// as a rate limit and queue the issue for a timed retry on its own scheduler.
// QueueRetryIfRateLimited returns true when the error was recognized as a
// rate limit and a retry was queued — the poller then leaves the issue
// unmarked so the scheduler owns the retry. Returning false hands the error
// back to the poller's standard failure path.
//
// This is a deliberate seam rather than a concrete scheduler dependency:
// error classification and task construction stay host-side, so the SDK never
// imports the application's task or rate-limit types.
type RateLimitScheduler interface {
	QueueRetryIfRateLimited(taskID, title, body, errText string) bool
}

// PollerDeps provides shared infrastructure to adapter pollers. Consuming
// applications supply these — the SDK never constructs them itself. All
// fields except Handler are optional; nil disables the corresponding hook.
type PollerDeps struct {
	// ProcessedStore deduplicates issues across restarts.
	ProcessedStore ProcessedStore
	// MaxConcurrent caps concurrent issue handling. Zero means the
	// adapter's own default.
	MaxConcurrent int
	// ExecutionMode selects how the adapter dispatches discovered issues:
	// "sequential" (one at a time, waiting for PR/MR merge), "parallel", or
	// "auto" where supported. Empty means the adapter's own default. Adapters
	// whose poller does not implement sequential execution ignore this field.
	ExecutionMode string
	// Handler processes each discovered issue.
	Handler IssueHandler
	// OnPRCreated fires after an adapter opens a PR/MR for an issue.
	OnPRCreated func(ev PRCreatedEvent)
	// TaskChecker skips retry re-dispatch while a task is still queued/running.
	TaskChecker TaskChecker
	// ExecutionChecker prevents re-dispatch when a completed execution record
	// exists. Its lookups (and ExecutionSaver records) are scoped by ProjectPath.
	ExecutionChecker ExecutionChecker
	// ProjectPath identifies the local project checkout used to scope
	// ExecutionChecker/ExecutionSaver records.
	ProjectPath string
	// PreFlightJudge evaluates issue quality before dispatch; rejections are
	// surfaced on the tracker and the issue is not dispatched.
	PreFlightJudge PreFlightJudger
	// ExecutionSaver persists pre-flight rejection records.
	ExecutionSaver ExecutionSaver
	// IssueMetricsRecorder records issue processing outcomes.
	IssueMetricsRecorder IssueMetricsRecorder
	// RateLimitScheduler queues timed retries for rate-limited handler errors.
	RateLimitScheduler RateLimitScheduler
	// Logger receives all poller-originated log lines. Nil falls back to
	// slog.Default(), so existing consumers see no behavior change.
	Logger *slog.Logger
	// PollerMetrics records poller dispatch/skip counters and the
	// unsourced-labeled-issues gauge (GH-4488).
	PollerMetrics PollerMetricsRecorder
	// BoardSyncAuthAlert, if set, is called at most once per process lifetime
	// when board status-sync fails with an auth/scope-class error (GH-4488) —
	// e.g. INSUFFICIENT_SCOPES. Adapters that have no board-sync layer ignore
	// this field.
	BoardSyncAuthAlert func(error)
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
