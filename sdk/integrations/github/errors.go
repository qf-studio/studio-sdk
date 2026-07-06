package github

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// RateLimitError is returned by doRequest when GitHub signals a rate limit via
// a 403 or 429 response. It carries the parsed Retry-After duration so the
// retry loop can honor it without regexing the error string.
type RateLimitError struct {
	StatusCode int
	RetryAfter time.Duration
	Message    string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("API error (status %d): %s", e.StatusCode, e.Message)
}

// AuthError is returned by doRequest when GitHub rejects the token itself
// (401 Unauthorized) — as opposed to a 403 which can mean rate-limited or
// forbidden-but-authenticated. Callers use errors.As to distinguish a dead/
// revoked token from other failures.
type AuthError struct {
	Message string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("API error (status %d): %s", http.StatusUnauthorized, e.Message)
}

// parseRetryAfterHeader reads Retry-After and X-RateLimit-Reset headers and
// returns the delay duration. Returns 0 when neither header is present.
func parseRetryAfterHeader(h http.Header) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	if v := h.Get("X-RateLimit-Reset"); v != "" {
		if unix, err := strconv.ParseInt(v, 10, 64); err == nil {
			if d := time.Until(time.Unix(unix, 0)); d > 0 {
				return d
			}
		}
	}
	return 0
}
