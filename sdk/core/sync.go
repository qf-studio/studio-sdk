package core

import (
	"context"
	"time"
)

// IssueSnapshot is the normalized, point-in-time view of a single tracker
// issue used by the board-sync engine. A connector's SyncSource produces
// snapshots; the host diffs each snapshot against a stored "shadow" (the
// last-synced copy) to compute a 3-way merge between the provider, the
// shadow, and the host's own board state.
type IssueSnapshot struct {
	// NativeID is the provider's opaque internal identifier for the issue.
	NativeID string
	// SequenceID is the provider-prefixed human identifier (e.g. "GH-83").
	// The prefix scheme is load-bearing: Pilot's branch naming
	// (pilot/<SequenceID>) depends on it staying stable across syncs.
	SequenceID string
	Title      string
	Body       string
	// State is the provider-native state id or name (e.g. a workflow state).
	State string
	// StateGroup is the provider's own state category (e.g. "todo", "in
	// progress", "done") for providers that expose one; empty otherwise.
	StateGroup string
	Labels     []string
	// Priority is normalized via NormalizePriority so the shadow diff
	// compares one vocabulary across every provider.
	Priority string
	// Assignee is a display-only identifier; it is not resolved to a host
	// user record here.
	Assignee string
	URL      string

	CreatedAt time.Time
	UpdatedAt time.Time

	// Deleted marks that the issue was removed or archived on the provider
	// side since the last sync; the shadow diff treats this as a tombstone.
	Deleted bool
}

// Cursor is an opaque, provider-specific pagination token returned by
// SyncSource list calls and passed back on the next page request.
type Cursor string

// FieldPatch is a partial set of issue fields to apply via
// SyncWriter.UpdateFields. Keys are field names understood by the target
// connector; values are the new field content.
type FieldPatch map[string]any

// IssueDraft is the minimal payload needed to create a new issue on a
// provider via SyncWriter.CreateIssue.
type IssueDraft struct {
	Title    string
	Body     string
	Labels   []string
	Priority string
}

// SyncSource is implemented by connectors that can read issue state for
// board synchronization. Unlike Pollable/Poller, it is not trigger-label
// filtered and is not restricted to created|updated actions — it exposes
// the full issue set a shadow-based sync needs to reconcile.
type SyncSource interface {
	// ListUpdatedSince returns issues changed on or after since, paginated
	// via page (empty Cursor requests the first page). It returns the page
	// of snapshots and the cursor for the next page (empty when exhausted).
	ListUpdatedSince(ctx context.Context, projectID string, since time.Time, page Cursor) ([]IssueSnapshot, Cursor, error)

	// ListAll returns every issue in projectID, paginated via page. Used for
	// full-resync passes where an incremental cursor is unavailable or
	// distrusted.
	ListAll(ctx context.Context, projectID string, page Cursor) ([]IssueSnapshot, Cursor, error)

	// GetIssue fetches a single issue snapshot by its native ID.
	GetIssue(ctx context.Context, nativeID string) (IssueSnapshot, error)
}

// SyncWriter is implemented by connectors that can write issue state back
// to the provider as part of board synchronization.
type SyncWriter interface {
	// UpdateFields applies a partial field patch to nativeID and returns the
	// resulting snapshot.
	UpdateFields(ctx context.Context, nativeID string, fields FieldPatch) (IssueSnapshot, error)

	// TransitionState moves nativeID to providerState, a provider-native
	// state id or name (see IssueSnapshot.State).
	TransitionState(ctx context.Context, nativeID, providerState string) error

	// AddComment posts body as a comment on nativeID. idemKey is a caller-
	// supplied idempotency key so retried syncs do not double-post.
	AddComment(ctx context.Context, nativeID, body, idemKey string) error

	// CreateIssue creates a new issue in projectID from draft and returns
	// the resulting snapshot.
	CreateIssue(ctx context.Context, projectID string, draft IssueDraft) (IssueSnapshot, error)
}

// SyncCapable is implemented by connectors that support full board
// synchronization: reading provider issue state (SyncSource) and writing
// changes back (SyncWriter). No connector implements this yet — the types
// here are the contract; connector implementations are follow-up work.
type SyncCapable interface {
	SyncSource
	SyncWriter
}
