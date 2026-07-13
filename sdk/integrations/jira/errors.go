package jira

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// RateLimitError is returned by doRequest when Jira signals a rate limit via
// a 429 response. It carries the parsed Retry-After duration so the retry
// loop can honor it without regexing the error string.
type RateLimitError struct {
	RetryAfter time.Duration
	Message    string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("API error (status %d): %s", http.StatusTooManyRequests, e.Message)
}

// AuthError is returned by doRequest when Jira rejects the credentials
// themselves (401 Unauthorized or 403 Forbidden). Both are treated as
// non-retryable: retrying with the same API token cannot help.
type AuthError struct {
	StatusCode int
	Message    string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("API error (status %d): %s", e.StatusCode, e.Message)
}

// parseRetryAfterHeader reads the Retry-After header (seconds, per RFC 7231)
// and returns the delay duration. Returns 0 when the header is absent.
func parseRetryAfterHeader(h http.Header) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}
