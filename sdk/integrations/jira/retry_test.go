package jira

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"nil error", nil, false},
		{"rate limit error", &RateLimitError{Message: "rate limited"}, true},
		{"auth error 401", &AuthError{StatusCode: 401, Message: "unauthorized"}, false},
		{"auth error 403", &AuthError{StatusCode: 403, Message: "forbidden"}, false},
		{"generic error", errors.New("something went wrong"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableError(tt.err); got != tt.retryable {
				t.Errorf("isRetryableError(%v) = %v, want %v", tt.err, got, tt.retryable)
			}
		})
	}
}

func TestExtractRetryAfter(t *testing.T) {
	if got := extractRetryAfter(&RateLimitError{RetryAfter: 45 * time.Second}); got != 45*time.Second {
		t.Errorf("extractRetryAfter() = %v, want 45s", got)
	}
	if got := extractRetryAfter(errors.New("generic")); got != 0 {
		t.Errorf("extractRetryAfter() = %v, want 0", got)
	}
	if got := extractRetryAfter(nil); got != 0 {
		t.Errorf("extractRetryAfter(nil) = %v, want 0", got)
	}
}

func TestDefaultRetryOptions(t *testing.T) {
	opts := DefaultRetryOptions()
	if opts.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", opts.MaxRetries)
	}
	if opts.BaseDelay != 1*time.Second {
		t.Errorf("BaseDelay = %v, want 1s", opts.BaseDelay)
	}
	if opts.MaxDelay != 30*time.Second {
		t.Errorf("MaxDelay = %v, want 30s", opts.MaxDelay)
	}
}

func TestWithRetry_RetriesRateLimitThenSucceeds(t *testing.T) {
	calls := 0
	result, err := WithRetry(context.Background(), func() (string, error) {
		calls++
		if calls < 3 {
			return "", &RateLimitError{Message: "rate limited"}
		}
		return "success", nil
	}, RetryOptions{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result != "success" {
		t.Errorf("result = %q, want success", result)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestWithRetryVoid_AuthErrorShortCircuits(t *testing.T) {
	calls := 0
	err := WithRetryVoid(context.Background(), func() error {
		calls++
		return &AuthError{StatusCode: 401, Message: "unauthorized"}
	}, RetryOptions{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond})

	if err == nil {
		t.Fatal("expected error")
	}
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *AuthError, got %T", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on auth error)", calls)
	}
}

func TestWithRetry_RetryAfterCappedAtMaxDelay(t *testing.T) {
	start := time.Now()
	calls := 0
	_, _ = WithRetry(context.Background(), func() (string, error) {
		calls++
		if calls == 1 {
			return "", &RateLimitError{RetryAfter: 60 * time.Second, Message: "rate limited"}
		}
		return "ok", nil
	}, RetryOptions{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond})
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("Retry-After was not capped: elapsed %v (expected <500ms)", elapsed)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}
