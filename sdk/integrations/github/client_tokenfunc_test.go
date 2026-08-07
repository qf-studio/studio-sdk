package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestNewClientWithTokenFunc verifies construction wires the resolver and
// options, and leaves the static token field unset (tokenFunc-based clients
// never populate it).
func TestNewClientWithTokenFunc(t *testing.T) {
	fn := func(ctx context.Context) (string, error) { return "tok", nil }
	client := NewClientWithTokenFunc(fn)
	if client == nil {
		t.Fatal("NewClientWithTokenFunc returned nil")
	}
	if client.tokenFunc == nil {
		t.Error("client.tokenFunc is nil, want set")
	}
	if client.token != "" {
		t.Errorf("client.token = %q, want empty for tokenFunc-based client", client.token)
	}
	if client.baseURL != githubAPIURL {
		t.Errorf("client.baseURL = %s, want %s", client.baseURL, githubAPIURL)
	}
}

// TestNewClientWithTokenFunc_Options verifies WithClientBaseURL and
// WithTokenInvalidate apply.
func TestNewClientWithTokenFunc_Options(t *testing.T) {
	var invalidated bool
	client := NewClientWithTokenFunc(
		func(ctx context.Context) (string, error) { return "tok", nil },
		WithClientBaseURL("https://custom.example.com"),
		WithTokenInvalidate(func() { invalidated = true }),
	)
	if client.baseURL != "https://custom.example.com" {
		t.Errorf("client.baseURL = %s, want https://custom.example.com", client.baseURL)
	}
	if client.invalidateToken == nil {
		t.Fatal("client.invalidateToken is nil, want set")
	}
	client.invalidateToken()
	if !invalidated {
		t.Error("invalidateToken hook was not invoked")
	}
}

// TestDoRequest_TokenFunc_ResolvedPerRequest verifies each call to doRequest
// (via GetIssue) re-invokes TokenFunc, so a token rotated between two
// separate requests over the client's lifetime is picked up without
// reconstructing the client — the core requirement for GitHub App
// installation tokens that expire hourly.
func TestDoRequest_TokenFunc_ResolvedPerRequest(t *testing.T) {
	var seenAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(Issue{Number: 1})
	}))
	defer server.Close()

	var calls int32
	tokenFn := func(ctx context.Context) (string, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return "token-v1", nil
		}
		return "token-v2", nil
	}
	client := NewClientWithTokenFunc(tokenFn, WithClientBaseURL(server.URL))
	client.retryOpts = RetryOptions{MaxRetries: 0}

	if _, err := client.GetIssue(context.Background(), "owner", "repo", 1); err != nil {
		t.Fatalf("first GetIssue: %v", err)
	}
	if _, err := client.GetIssue(context.Background(), "owner", "repo", 1); err != nil {
		t.Fatalf("second GetIssue: %v", err)
	}

	want := []string{"Bearer token-v1", "Bearer token-v2"}
	if len(seenAuth) != 2 || seenAuth[0] != want[0] || seenAuth[1] != want[1] {
		t.Errorf("seenAuth = %v, want %v", seenAuth, want)
	}
}

// TestDoRequest_TokenFunc_ResolvedMidRetry verifies TokenFunc is re-invoked
// on every attempt inside a single request's retry loop, not just once at
// the start — a rotation that happens between attempts (e.g. an installation
// token refreshed by a concurrent goroutine mid-backoff) must be picked up
// on the very next attempt rather than waiting for the next top-level call.
func TestDoRequest_TokenFunc_ResolvedMidRetry(t *testing.T) {
	var seenAuth []string
	var attempt int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		n := atomic.AddInt32(&attempt, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(Issue{Number: 1})
	}))
	defer server.Close()

	var calls int32
	tokenFn := func(ctx context.Context) (string, error) {
		n := atomic.AddInt32(&calls, 1)
		return []string{"token-a", "token-b", "token-c"}[n-1], nil
	}
	client := NewClientWithTokenFunc(tokenFn, WithClientBaseURL(server.URL))
	client.retryOpts = RetryOptions{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 5 * time.Millisecond}

	if _, err := client.GetIssue(context.Background(), "owner", "repo", 1); err != nil {
		t.Fatalf("GetIssue: %v", err)
	}

	want := []string{"Bearer token-a", "Bearer token-b", "Bearer token-c"}
	if len(seenAuth) != 3 {
		t.Fatalf("seenAuth = %v, want 3 attempts", seenAuth)
	}
	for i, w := range want {
		if seenAuth[i] != w {
			t.Errorf("attempt %d: Authorization = %s, want %s", i+1, seenAuth[i], w)
		}
	}
}

// TestDoRequest_TokenFunc_InvalidateOnAuthError verifies that a 401 triggers
// the invalidation hook exactly once and a single fresh-resolve retry, which
// succeeds once TokenFunc returns a valid token.
func TestDoRequest_TokenFunc_InvalidateOnAuthError(t *testing.T) {
	var seenAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") == "Bearer expired-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Bad credentials"})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(Issue{Number: 1})
	}))
	defer server.Close()

	current := "expired-token"
	tokenFn := func(ctx context.Context) (string, error) { return current, nil }

	var invalidateCalls int
	client := NewClientWithTokenFunc(
		tokenFn,
		WithClientBaseURL(server.URL),
		WithTokenInvalidate(func() {
			invalidateCalls++
			current = "fresh-token"
		}),
	)
	client.retryOpts = RetryOptions{MaxRetries: 0}

	issue, err := client.GetIssue(context.Background(), "owner", "repo", 1)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Number != 1 {
		t.Errorf("issue.Number = %d, want 1", issue.Number)
	}
	if invalidateCalls != 1 {
		t.Errorf("invalidateCalls = %d, want 1", invalidateCalls)
	}
	want := []string{"Bearer expired-token", "Bearer fresh-token"}
	if len(seenAuth) != 2 || seenAuth[0] != want[0] || seenAuth[1] != want[1] {
		t.Errorf("seenAuth = %v, want %v", seenAuth, want)
	}
}

// TestDoRequest_TokenFunc_NoInvalidateHook_NoRetryOn401 verifies that
// without WithTokenInvalidate, a 401 is not retried at all — matching
// isRetryableError's existing "dead token, retrying cannot help" behavior,
// so static-style TokenFunc usage without an invalidation hook doesn't
// silently double every failed request.
func TestDoRequest_TokenFunc_NoInvalidateHook_NoRetryOn401(t *testing.T) {
	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Bad credentials"})
	}))
	defer server.Close()

	client := NewClientWithTokenFunc(
		func(ctx context.Context) (string, error) { return "dead-token", nil },
		WithClientBaseURL(server.URL),
	)
	client.retryOpts = RetryOptions{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 5 * time.Millisecond}

	_, err := client.GetIssue(context.Background(), "owner", "repo", 1)
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Errorf("expected *AuthError, got %T: %v", err, err)
	}
	if callCount != 1 {
		t.Errorf("callCount = %d, want 1 (no retry without invalidation hook)", callCount)
	}
}

// TestDoRequest_TokenFunc_ResolveError verifies a TokenFunc error short-circuits
// the request without making an HTTP call.
func TestDoRequest_TokenFunc_ResolveError(t *testing.T) {
	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	wantErr := errors.New("installation token unavailable")
	client := NewClientWithTokenFunc(
		func(ctx context.Context) (string, error) { return "", wantErr },
		WithClientBaseURL(server.URL),
	)
	client.retryOpts = RetryOptions{MaxRetries: 0}

	_, err := client.GetIssue(context.Background(), "owner", "repo", 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want wrapping %v", err, wantErr)
	}
	if called {
		t.Error("HTTP request was made despite TokenFunc failing")
	}
}

// TestExecuteGraphQL_TokenFunc_InvalidateOnAuthError mirrors
// TestDoRequest_TokenFunc_InvalidateOnAuthError for the GraphQL path, which
// has its own request/response handling separate from doRequest.
func TestExecuteGraphQL_TokenFunc_InvalidateOnAuthError(t *testing.T) {
	var seenAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") == "Bearer expired-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "Bad credentials"})
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"octocat"}}}`))
	}))
	defer server.Close()

	current := "expired-token"
	var invalidateCalls int
	client := NewClientWithTokenFunc(
		func(ctx context.Context) (string, error) { return current, nil },
		WithClientBaseURL(server.URL),
		WithTokenInvalidate(func() {
			invalidateCalls++
			current = "fresh-token"
		}),
	)
	client.retryOpts = RetryOptions{MaxRetries: 0}

	var result struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
	}
	err := client.ExecuteGraphQL(context.Background(), `query { viewer { login } }`, nil, &result)
	if err != nil {
		t.Fatalf("ExecuteGraphQL: %v", err)
	}
	if result.Viewer.Login != "octocat" {
		t.Errorf("Viewer.Login = %s, want octocat", result.Viewer.Login)
	}
	if invalidateCalls != 1 {
		t.Errorf("invalidateCalls = %d, want 1", invalidateCalls)
	}
	want := []string{"Bearer expired-token", "Bearer fresh-token"}
	if len(seenAuth) != 2 || seenAuth[0] != want[0] || seenAuth[1] != want[1] {
		t.Errorf("seenAuth = %v, want %v", seenAuth, want)
	}
}

// TestNewClient_StaticToken_Unaffected verifies the one-shot NewClient
// constructor still resolves to its fixed token via resolveToken (no
// TokenFunc set), preserving existing behavior for one-shot callers.
func TestNewClient_StaticToken_Unaffected(t *testing.T) {
	client := NewClient("static-token")
	token, err := client.resolveToken(context.Background())
	if err != nil {
		t.Fatalf("resolveToken: %v", err)
	}
	if token != "static-token" {
		t.Errorf("resolveToken() = %s, want static-token", token)
	}
}
