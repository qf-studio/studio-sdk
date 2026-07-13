package jira

import (
	"context"
	"errors"
	"time"

	retryutil "github.com/qf-studio/studio-sdk/sdk/util/retry"
)

// RetryOptions configures retry behavior. The backoff/context-cancellation
// engine lives in sdk/util/retry; this alias keeps the connector's own API
// surface (Client.retryOpts, tests) unchanged.
type RetryOptions = retryutil.RetryOptions

// DefaultRetryOptions returns sensible defaults for retry behavior.
func DefaultRetryOptions() RetryOptions {
	return retryutil.DefaultRetryOptions()
}

// WithRetry executes an operation with exponential backoff retry. Error
// classification defaults to Jira's own isRetryableError/extractRetryAfter
// (429 with Retry-After → retry; 401/403 → AuthError short-circuits) unless
// the caller already set Classify/ExtractRetryAfter.
func WithRetry[T any](ctx context.Context, op func() (T, error), opts RetryOptions) (T, error) {
	if opts.Classify == nil {
		opts.Classify = isRetryableError
	}
	if opts.ExtractRetryAfter == nil {
		opts.ExtractRetryAfter = extractRetryAfter
	}
	return retryutil.WithRetry(ctx, op, opts)
}

// WithRetryVoid is like WithRetry but for operations that don't return a value.
func WithRetryVoid(ctx context.Context, op func() error, opts RetryOptions) error {
	_, err := WithRetry(ctx, func() (struct{}, error) {
		return struct{}{}, op()
	}, opts)
	return err
}

// isRetryableError reports whether err is transient and worth retrying.
// *RateLimitError (429) is retried; *AuthError (401/403) is not, since
// retrying with the same credentials cannot succeed. Anything else is
// treated as non-retryable.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var rlErr *RateLimitError
	if errors.As(err, &rlErr) {
		return true
	}
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return false
	}
	return false
}

// extractRetryAfter returns the provider-requested backoff for err, or 0.
func extractRetryAfter(err error) time.Duration {
	var rlErr *RateLimitError
	if errors.As(err, &rlErr) {
		return rlErr.RetryAfter
	}
	return 0
}
