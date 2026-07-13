package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWithRetry_SuccessOnFirstAttempt(t *testing.T) {
	calls := 0
	result, err := WithRetry(context.Background(), func() (string, error) {
		calls++
		return "success", nil
	}, DefaultRetryOptions())

	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if result != "success" {
		t.Errorf("expected 'success', got: %s", result)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got: %d", calls)
	}
}

func TestWithRetry_SuccessAfterRetries(t *testing.T) {
	calls := 0
	result, err := WithRetry(context.Background(), func() (string, error) {
		calls++
		if calls < 3 {
			return "", &RateLimitError{Message: "rate limited"}
		}
		return "success", nil
	}, RetryOptions{
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   10 * time.Millisecond,
	})

	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if result != "success" {
		t.Errorf("expected 'success', got: %s", result)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got: %d", calls)
	}
}

func TestWithRetry_ExhaustsRetries(t *testing.T) {
	calls := 0
	_, err := WithRetry(context.Background(), func() (string, error) {
		calls++
		return "", &RateLimitError{Message: "still limited"}
	}, RetryOptions{
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   10 * time.Millisecond,
	})

	if err == nil {
		t.Error("expected error after exhausting retries")
	}
	if calls != 4 {
		t.Errorf("expected 4 calls (1 + 3 retries), got: %d", calls)
	}
}

func TestWithRetry_NonRetryableError(t *testing.T) {
	calls := 0
	_, err := WithRetry(context.Background(), func() (string, error) {
		calls++
		return "", errors.New("boom: not a typed error")
	}, RetryOptions{
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   10 * time.Millisecond,
	})

	if err == nil {
		t.Error("expected error for non-retryable failure")
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retries for untyped error), got: %d", calls)
	}
}

func TestWithRetry_AuthErrorShortCircuits(t *testing.T) {
	calls := 0
	_, err := WithRetry(context.Background(), func() (string, error) {
		calls++
		return "", &AuthError{Message: "bad credentials"}
	}, RetryOptions{
		MaxRetries: 5,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   10 * time.Millisecond,
	})

	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected *AuthError, got %T", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call (AuthError must not retry), got: %d", calls)
	}
}

func TestWithRetry_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0

	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	_, err := WithRetry(ctx, func() (string, error) {
		calls++
		return "", &RateLimitError{Message: "rate limited"}
	}, RetryOptions{
		MaxRetries: 10,
		BaseDelay:  50 * time.Millisecond,
		MaxDelay:   100 * time.Millisecond,
	})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
	if calls > 2 {
		t.Errorf("expected at most 2 calls before cancellation, got: %d", calls)
	}
}

func TestWithRetry_RetryAfterHonored(t *testing.T) {
	start := time.Now()
	calls := 0
	_, _ = WithRetry(context.Background(), func() (string, error) {
		calls++
		if calls == 1 {
			return "", &RateLimitError{RetryAfter: 20 * time.Millisecond, Message: "rate limited"}
		}
		return "ok", nil
	}, RetryOptions{
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   1 * time.Second,
	})
	elapsed := time.Since(start)

	if elapsed < 20*time.Millisecond {
		t.Errorf("Retry-After was not honored: elapsed %v (expected >= 20ms)", elapsed)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
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
	}, RetryOptions{
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   10 * time.Millisecond,
	})
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("Retry-After was not capped: elapsed %v (expected <500ms)", elapsed)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
}

func TestWithRetry_ExponentialBackoff(t *testing.T) {
	delays := []time.Duration{}
	lastCall := time.Now()

	calls := 0
	_, _ = WithRetry(context.Background(), func() (string, error) {
		now := time.Now()
		if calls > 0 {
			delays = append(delays, now.Sub(lastCall))
		}
		lastCall = now
		calls++
		if calls <= 3 {
			return "", &RateLimitError{Message: "rate limited"}
		}
		return "done", nil
	}, RetryOptions{
		MaxRetries: 3,
		BaseDelay:  10 * time.Millisecond,
		MaxDelay:   100 * time.Millisecond,
	})

	if len(delays) != 3 {
		t.Fatalf("expected 3 delays, got %d", len(delays))
	}

	expectedDelays := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	tolerance := 5 * time.Millisecond

	for i, expected := range expectedDelays {
		if delays[i] < expected-tolerance || delays[i] > expected+2*tolerance {
			t.Errorf("delay[%d] = %v, expected ~%v (tolerance %v)", i, delays[i], expected, tolerance)
		}
	}
}

func TestWithRetryVoid_Success(t *testing.T) {
	calls := 0
	err := WithRetryVoid(context.Background(), func() error {
		calls++
		return nil
	}, DefaultRetryOptions())

	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got: %d", calls)
	}
}

func TestWithRetryVoid_RetriesOnError(t *testing.T) {
	calls := 0
	err := WithRetryVoid(context.Background(), func() error {
		calls++
		if calls < 2 {
			return &RateLimitError{Message: "rate limited"}
		}
		return nil
	}, RetryOptions{
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   10 * time.Millisecond,
	})

	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls, got: %d", calls)
	}
}

func TestWithRetry_CustomClassifyOverridesDefault(t *testing.T) {
	calls := 0
	_, err := WithRetry(context.Background(), func() (string, error) {
		calls++
		return "", errors.New("status 503: service unavailable")
	}, RetryOptions{
		MaxRetries: 2,
		BaseDelay:  1 * time.Millisecond,
		MaxDelay:   10 * time.Millisecond,
		Classify: func(err error) bool {
			return err != nil // treat every error as retryable for this test
		},
	})

	if err == nil {
		t.Error("expected error after exhausting retries")
	}
	if calls != 3 {
		t.Errorf("expected 3 calls (1 + 2 retries) with custom Classify, got: %d", calls)
	}
}

func TestDefaultClassify(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"nil error", nil, false},
		{"rate limit error", &RateLimitError{Message: "limited"}, true},
		{"auth error", &AuthError{Message: "bad token"}, false},
		{"untyped error", errors.New("something went wrong"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DefaultClassify(tt.err); got != tt.retryable {
				t.Errorf("DefaultClassify(%v) = %v, want %v", tt.err, got, tt.retryable)
			}
		})
	}
}

func TestDefaultExtractRetryAfter(t *testing.T) {
	if got := DefaultExtractRetryAfter(nil); got != 0 {
		t.Errorf("expected 0 for nil error, got %v", got)
	}
	if got := DefaultExtractRetryAfter(errors.New("plain")); got != 0 {
		t.Errorf("expected 0 for untyped error, got %v", got)
	}
	got := DefaultExtractRetryAfter(&RateLimitError{RetryAfter: 17 * time.Second})
	if got != 17*time.Second {
		t.Errorf("expected 17s, got %v", got)
	}
}

func TestDefaultRetryOptions(t *testing.T) {
	opts := DefaultRetryOptions()

	if opts.MaxRetries != 3 {
		t.Errorf("expected MaxRetries=3, got %d", opts.MaxRetries)
	}
	if opts.BaseDelay != 1*time.Second {
		t.Errorf("expected BaseDelay=1s, got %v", opts.BaseDelay)
	}
	if opts.MaxDelay != 30*time.Second {
		t.Errorf("expected MaxDelay=30s, got %v", opts.MaxDelay)
	}
}
