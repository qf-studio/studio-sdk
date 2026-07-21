// Package skipreason defines shared constants and interfaces for poller skip metrics.
// All issue-tracker pollers (GitHub, GitLab, Azure DevOps) use these constants as the
// `reason` label value in the poller-skipped counter.
package skipreason

// Reason constants — used as the `reason` label in the poller-skipped counter.
const (
	ReasonInProgress         = "in_progress"
	ReasonDone               = "done"
	ReasonBlocked            = "blocked"
	ReasonNeedsClarification = "needs_clarification"
	ReasonSuperseded         = "superseded"
	ReasonFailedSkip         = "failed_skip"
	ReasonRetryReadySkip     = "retry_ready_skip"
	ReasonProcessedGrace     = "processed_grace"
	ReasonTaskQueued         = "task_queued"
	ReasonHasMergedWork      = "has_merged_work"
	ReasonPendingDependency  = "pending_dependency"
	ReasonCompletedExecution = "completed_execution"
	ReasonFreshLabelCheck    = "fresh_label_check"
	ReasonPreFlightReject    = "pre_flight_reject"
	ReasonStatusLabel        = "status_label" // GitLab combined in_progress/done/failed
	ReasonStatusTag          = "status_tag"   // Azure DevOps combined in_progress/done/failed
)

// PollerMetricsRecorder records poller dispatch/skip counters. Consuming
// applications implement this; the SDK keeps it as an interface so adapter
// packages can reference it without importing a concrete metrics backend.
type PollerMetricsRecorder interface {
	RecordPollerSkipped(repo, reason string)
	RecordPollerDispatched(repo string)
	RecordPollerDeferredScopeOverlap(repo string)

	// RecordUnsourcedLabeledIssues sets a gauge to the current count of open,
	// dispatch-labeled issues that a board-sourcing poller cannot see (absent
	// from the board, or on it in a status other than source_status). Unlike
	// the counters above this is a gauge: callers should pass the current
	// count each time, not a delta. GH-4488.
	RecordUnsourcedLabeledIssues(repo string, count int)
}
