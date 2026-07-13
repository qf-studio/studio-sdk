package github

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	retryutil "github.com/qf-studio/studio-sdk/sdk/util/retry"
)

// RetryOptions configures retry behavior. The backoff/context-cancellation
// engine lives in sdk/util/retry; this alias keeps the existing github API
// surface (Client.retryOpts, tests) unchanged.
type RetryOptions = retryutil.RetryOptions

// DefaultRetryOptions returns sensible defaults for retry behavior.
func DefaultRetryOptions() RetryOptions {
	return retryutil.DefaultRetryOptions()
}

// WithRetry executes an operation with exponential backoff retry.
// It respects context cancellation and GitHub's Retry-After header. Error
// classification defaults to GitHub's own isRetryableError/extractRetryAfter
// (provider-specific: 429/403-rate-limit, 5xx, network errors, GraphQL
// RATE_LIMITED) unless the caller already set Classify/ExtractRetryAfter.
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

// isRetryableError determines if an error is transient and should be retried.
// Returns true for:
// - 429 Too Many Requests (rate limiting)
// - 500, 502, 503, 504 (server errors)
// - Network/connection errors
// Returns false for:
// - 400 Bad Request
// - 401 Unauthorized
// - 403 Forbidden (non-rate-limit)
// - 404 Not Found
// - 422 Unprocessable Entity
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Typed fast path: doRequest classifies 429s and 403-rate-limits as
	// *RateLimitError (including 403s whose Error() string would not match
	// the status-code list below).
	var rlErr *RateLimitError
	if errors.As(err, &rlErr) {
		return true
	}
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return false // dead token — retrying cannot help
	}

	errStr := err.Error()

	// Check for retryable HTTP status codes
	retryableStatuses := []string{
		"status 429", // Rate limited
		"status 500", // Internal Server Error
		"status 502", // Bad Gateway
		"status 503", // Service Unavailable
		"status 504", // Gateway Timeout
	}

	for _, status := range retryableStatuses {
		if strings.Contains(errStr, status) {
			return true
		}
	}

	// GraphQL rate limit errors: HTTP 200 but error body signals rate-limiting.
	// GitHub Projects V2 returns these as GraphQL errors rather than HTTP 429.
	if strings.Contains(errStr, "RATE_LIMITED") || strings.Contains(errStr, "was submitted too quickly") {
		return true
	}

	// Check for network errors (these don't have HTTP status)
	networkErrors := []string{
		"connection refused",
		"connection reset",
		"no such host",
		"network is unreachable",
		"i/o timeout",
		"context deadline exceeded",
		"dial tcp",
	}

	errLower := strings.ToLower(errStr)
	for _, netErr := range networkErrors {
		if strings.Contains(errLower, netErr) {
			return true
		}
	}

	return false
}

// extractRetryAfter extracts the Retry-After duration from a rate limit error.
// GitHub includes this header in 429 responses indicating when the client can retry.
// Returns 0 if no Retry-After information is found.
func extractRetryAfter(err error) time.Duration {
	if err == nil {
		return 0
	}

	// Typed fast path: doRequest parses Retry-After/X-RateLimit-Reset headers
	// into RateLimitError.RetryAfter — no string parsing needed.
	var rlErr *RateLimitError
	if errors.As(err, &rlErr) && rlErr.RetryAfter > 0 {
		return rlErr.RetryAfter
	}

	errStr := err.Error()

	// GitHub API sometimes includes retry-after info in error response
	patterns := []string{
		`retry.after[:\s]+(\d+)`,
		`Retry-After[:\s]+(\d+)`,
		`rate.limit.*?(\d+)\s*seconds?`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile("(?i)" + pattern)
		matches := re.FindStringSubmatch(errStr)
		if len(matches) > 1 {
			if seconds, parseErr := strconv.Atoi(matches[1]); parseErr == nil && seconds > 0 {
				return time.Duration(seconds) * time.Second
			}
		}
	}

	// Default for 429 without explicit retry-after: wait 60 seconds
	if strings.Contains(errStr, "status 429") {
		return 60 * time.Second
	}

	return 0
}
