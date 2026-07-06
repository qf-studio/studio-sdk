package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func newErrClient(t *testing.T, status int, body string, headers map[string]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
}

func TestDoRequest_429_ReturnsTypedRateLimitError(t *testing.T) {
	c := newErrClient(t, http.StatusTooManyRequests, `{"message":"API rate limit exceeded"}`,
		map[string]string{"Retry-After": "42"})

	_, err := c.GetIssue(context.Background(), "o", "r", 1)
	if err == nil {
		t.Fatal("expected error")
	}
	var rlErr *RateLimitError
	if !errors.As(err, &rlErr) {
		t.Fatalf("error is %T, want *RateLimitError (errors.As must match for autopilot handling)", err)
	}
	if rlErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", rlErr.StatusCode)
	}
	if rlErr.RetryAfter != 42*time.Second {
		t.Errorf("RetryAfter = %v, want 42s", rlErr.RetryAfter)
	}
}

func TestDoRequest_403RateLimit_ReturnsTypedRateLimitError(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		headers map[string]string
	}{
		{
			name:    "remaining zero header",
			body:    `{"message":"forbidden"}`,
			headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(time.Now().Add(90*time.Second).Unix(), 10)},
		},
		{
			name: "secondary rate limit message",
			body: `{"message":"You have exceeded a secondary rate limit"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newErrClient(t, http.StatusForbidden, tt.body, tt.headers)
			_, err := c.GetIssue(context.Background(), "o", "r", 1)
			var rlErr *RateLimitError
			if !errors.As(err, &rlErr) {
				t.Fatalf("error is %T, want *RateLimitError", err)
			}
			if rlErr.StatusCode != http.StatusForbidden {
				t.Errorf("StatusCode = %d, want 403", rlErr.StatusCode)
			}
		})
	}
}

func TestDoRequest_403Plain_IsNotRateLimitError(t *testing.T) {
	c := newErrClient(t, http.StatusForbidden, `{"message":"Resource not accessible by integration"}`, nil)
	_, err := c.GetIssue(context.Background(), "o", "r", 1)
	if err == nil {
		t.Fatal("expected error")
	}
	var rlErr *RateLimitError
	if errors.As(err, &rlErr) {
		t.Error("plain 403 must NOT be classified as a rate limit")
	}
}

func TestDoRequest_401_ReturnsTypedAuthError(t *testing.T) {
	c := newErrClient(t, http.StatusUnauthorized, `{"message":"Bad credentials"}`, nil)
	_, err := c.GetIssue(context.Background(), "o", "r", 1)
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("error is %T, want *AuthError", err)
	}
}

func TestIsRetryable_TypedErrors(t *testing.T) {
	if !isRetryableError(&RateLimitError{StatusCode: 403, Message: "secondary rate limit"}) {
		t.Error("403-classified RateLimitError must be retryable (string path would miss it)")
	}
	if isRetryableError(&AuthError{Message: "Bad credentials"}) {
		t.Error("AuthError must not be retryable")
	}
}

func TestExtractRetryAfter_TypedFastPath(t *testing.T) {
	got := extractRetryAfter(&RateLimitError{StatusCode: 429, RetryAfter: 17 * time.Second})
	if got != 17*time.Second {
		t.Errorf("extractRetryAfter = %v, want 17s", got)
	}
}

func TestParseParentIssueNumber_Exported(t *testing.T) {
	if n := ParseParentIssueNumber("Parent: #37\n\nBody"); n != 37 {
		t.Errorf("ParseParentIssueNumber = %d, want 37", n)
	}
	if n := ParseParentIssueNumber("Parent: GH-42"); n != 42 {
		t.Errorf("ParseParentIssueNumber = %d, want 42", n)
	}
	if n := ParseParentIssueNumber("no parent here"); n != 0 {
		t.Errorf("ParseParentIssueNumber = %d, want 0", n)
	}
}

func TestFailedRetryStateLabels(t *testing.T) {
	want := []string{"pilot-failed-retry-1", "pilot-failed-retry-2", "pilot-failed-retry-exhausted"}
	if len(FailedRetryStateLabels) != len(want) {
		t.Fatalf("FailedRetryStateLabels = %v", FailedRetryStateLabels)
	}
	for i, w := range want {
		if FailedRetryStateLabels[i] != w {
			t.Errorf("FailedRetryStateLabels[%d] = %q, want %q", i, FailedRetryStateLabels[i], w)
		}
	}
	if LabelPilot != "pilot" {
		t.Errorf("LabelPilot = %q, want pilot", LabelPilot)
	}
}

func TestGetOpenSubIssueNumbers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/graphql" {
			_, _ = w.Write([]byte(`{"data":{"node":{"subIssues":{"totalCount":3,"nodes":[
				{"number":11,"state":"OPEN"},{"number":12,"state":"CLOSED"},{"number":13,"state":"OPEN"}]}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"node_id":"I_parent","number":10}`))
	}))
	defer srv.Close()

	c := NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	numbers, hasLinks, err := c.GetOpenSubIssueNumbers(context.Background(), "o", "r", 10)
	if err != nil {
		t.Fatalf("GetOpenSubIssueNumbers: %v", err)
	}
	if !hasLinks {
		t.Error("hasNativeLinks = false, want true (totalCount 3)")
	}
	if len(numbers) != 2 || numbers[0] != 11 || numbers[1] != 13 {
		t.Errorf("numbers = %v, want [11 13]", numbers)
	}
}

func TestGetOpenSubIssueNumbers_NoNativeLinks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/graphql" {
			_, _ = w.Write([]byte(`{"data":{"node":{"subIssues":{"totalCount":0,"nodes":[]}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"node_id":"I_parent","number":10}`))
	}))
	defer srv.Close()

	c := NewClientWithBaseURL(testutil.FakeGitHubToken, srv.URL)
	numbers, hasLinks, err := c.GetOpenSubIssueNumbers(context.Background(), "o", "r", 10)
	if err != nil {
		t.Fatalf("GetOpenSubIssueNumbers: %v", err)
	}
	if hasLinks || numbers != nil {
		t.Errorf("got (%v, %v), want (nil, false)", numbers, hasLinks)
	}
}
