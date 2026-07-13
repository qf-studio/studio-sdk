// Package retry provides a provider-agnostic retry engine (exponential
// backoff, context cancellation, Retry-After honoring) plus the two typed
// errors every connector needs to drive it: RateLimitError and AuthError.
//
// Error classification is deliberately NOT centralized here. Each connector
// knows how its provider signals "rate limited" or "auth rejected" (status
// codes, headers, GraphQL error types, ...) and maps those onto typed errors
// or its own RetryOptions.Classify/ExtractRetryAfter functions. This package
// only understands the typed RateLimitError/AuthError by default; it does not
// pattern-match provider error strings.
//
// This is a leaf package with no dependencies beyond the Go stdlib, so any
// connector can import it without creating a cycle.
package retry

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// RateLimitError signals that an operation was rejected because of API rate
// limiting. RetryAfter carries the provider's requested backoff when known;
// zero means the caller should fall back to exponential backoff.
type RateLimitError struct {
	RetryAfter time.Duration
	Message    string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited: %s", e.Message)
}

// AuthError signals that the credentials themselves were rejected (e.g. HTTP
// 401). It is never retryable — retrying with the same token cannot help, so
// callers should park the operation immediately instead of burning attempts.
type AuthError struct {
	Message string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("auth error: %s", e.Message)
}

// RetryOptions configures WithRetry / WithRetryVoid.
//
// Classify and ExtractRetryAfter are per-connector hooks: each integration
// maps its provider's error responses onto typed errors (or its own richer
// error types) and supplies matching functions here. Both default to typed-
// error-only behavior when left nil — no status-code string matching.
type RetryOptions struct {
	MaxRetries int           // Maximum number of retries (default: 3)
	BaseDelay  time.Duration // Initial delay between retries (default: 1s)
	MaxDelay   time.Duration // Maximum delay between retries (default: 30s)

	// Classify reports whether err is transient and worth retrying. Defaults
	// to DefaultClassify when nil.
	Classify func(err error) bool

	// ExtractRetryAfter returns the provider-requested backoff for err, or 0
	// if none applies. Defaults to DefaultExtractRetryAfter when nil.
	ExtractRetryAfter func(err error) time.Duration
}

// DefaultRetryOptions returns sensible defaults for retry behavior.
func DefaultRetryOptions() RetryOptions {
	return RetryOptions{
		MaxRetries: 3,
		BaseDelay:  1 * time.Second,
		MaxDelay:   30 * time.Second,
	}
}

// DefaultClassify retries *RateLimitError and refuses *AuthError; any other
// error is treated as non-retryable. Connectors with richer transient-error
// detection (5xx, network failures, provider-specific GraphQL errors, ...)
// should supply their own Classify via RetryOptions.
func DefaultClassify(err error) bool {
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

// DefaultExtractRetryAfter returns RateLimitError.RetryAfter when err (or
// something it wraps) is a *RateLimitError, else 0.
func DefaultExtractRetryAfter(err error) time.Duration {
	var rlErr *RateLimitError
	if errors.As(err, &rlErr) {
		return rlErr.RetryAfter
	}
	return 0
}

// WithRetry executes op with exponential backoff, honoring context
// cancellation and any provider-requested Retry-After delay reported via
// opts.ExtractRetryAfter.
func WithRetry[T any](ctx context.Context, op func() (T, error), opts RetryOptions) (T, error) {
	classify := opts.Classify
	if classify == nil {
		classify = DefaultClassify
	}
	extractRetryAfter := opts.ExtractRetryAfter
	if extractRetryAfter == nil {
		extractRetryAfter = DefaultExtractRetryAfter
	}

	var result T
	var lastErr error

	for attempt := 0; attempt <= opts.MaxRetries; attempt++ {
		result, lastErr = op()
		if lastErr == nil {
			return result, nil
		}

		if !classify(lastErr) {
			return result, lastErr
		}
		if attempt >= opts.MaxRetries {
			return result, lastErr
		}

		// Exponential backoff: BaseDelay, 2x, 4x, 8x... capped at MaxDelay.
		delay := opts.BaseDelay * time.Duration(1<<uint(attempt))
		if delay > opts.MaxDelay {
			delay = opts.MaxDelay
		}

		// A provider-requested Retry-After overrides backoff, but is still
		// capped at MaxDelay so a runaway "Retry-After: 3600" can't stall
		// the caller for an hour.
		if retryAfter := extractRetryAfter(lastErr); retryAfter > 0 {
			if retryAfter > opts.MaxDelay {
				retryAfter = opts.MaxDelay
			}
			delay = retryAfter
		}

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(delay):
		}
	}

	return result, lastErr
}

// WithRetryVoid is like WithRetry but for operations that don't return a value.
func WithRetryVoid(ctx context.Context, op func() error, opts RetryOptions) error {
	_, err := WithRetry(ctx, func() (struct{}, error) {
		return struct{}{}, op()
	}, opts)
	return err
}
