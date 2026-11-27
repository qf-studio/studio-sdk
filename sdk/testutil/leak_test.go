package testutil

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// recordingTB records Errorf and Fatal calls instead of failing the test, so
// a helper's failure path can be asserted.
type recordingTB struct {
	testing.TB
	msgs []string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Errorf(format string, args ...any) {
	r.msgs = append(r.msgs, fmt.Sprintf(format, args...))
}

func (r *recordingTB) Fatal(args ...any) {
	r.msgs = append(r.msgs, fmt.Sprint(args...))
}

func TestCanaryToken_IsPlainMarker(t *testing.T) {
	for _, prefix := range []string{"ghp_", "glpat-", "xoxb-", "xapp-", "sk-", "AKIA"} {
		if strings.HasPrefix(CanaryToken, prefix) {
			t.Errorf("CanaryToken %q starts with secret prefix %q", CanaryToken, prefix)
		}
	}
	if CanaryToken == "" {
		t.Error("CanaryToken is empty")
	}
}

func TestAssertNoCanaryLeak(t *testing.T) {
	rec := &recordingTB{TB: t}
	AssertNoCanaryLeak(rec, CanaryToken, "clean", "has "+CanaryToken, "also clean", CanaryToken)
	if len(rec.msgs) != 2 {
		t.Fatalf("got %d failures, want 2: %q", len(rec.msgs), rec.msgs)
	}
	if !strings.Contains(rec.msgs[0], "text 1") || !strings.Contains(rec.msgs[1], "text 3") {
		t.Errorf("failures do not name the index: %q", rec.msgs)
	}

	rec = &recordingTB{TB: t}
	AssertNoCanaryLeak(rec, CanaryToken, "clean", "")
	if len(rec.msgs) != 0 {
		t.Errorf("clean texts failed: %q", rec.msgs)
	}

	rec = &recordingTB{TB: t}
	AssertNoCanaryLeak(rec, "", "anything")
	if len(rec.msgs) != 1 {
		t.Errorf("empty canary: got %d failures, want 1", len(rec.msgs))
	}
}

func TestFailingTransport(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://example.invalid/path", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := FailingTransport().RoundTrip(req)
	if resp != nil {
		t.Errorf("resp = %v, want nil", resp)
	}
	if err == nil {
		t.Fatal("RoundTrip returned nil error")
	}
	for _, want := range []string{"POST", "http://example.invalid/path", "payload"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not hold %q", err.Error(), want)
		}
	}
}

func TestEchoServer(t *testing.T) {
	srv := EchoServer(t, http.StatusTeapot)
	resp, err := http.Post(srv.URL+"/echo", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusTeapot)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"POST /echo", "Content-Type: text/plain", "payload"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("body %q does not hold %q", body, want)
		}
	}
}

// TestLeakHelpers_CarryCredentials proves the worst case: the error of
// FailingTransport and the body of EchoServer hold the Authorization header
// value and the URL path value the request carried. If a change drops either,
// the leak tests built on these helpers go blind and this test fails.
func TestLeakHelpers_CarryCredentials(t *testing.T) {
	const pathSecret = "path-" + CanaryToken
	const headerSecret = "header-" + CanaryToken

	newReq := func(base string) *http.Request {
		req, err := http.NewRequest(http.MethodPost, base+"/bot"+pathSecret+"/sendMessage", strings.NewReader(`{"text":"hi"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+headerSecret)
		return req
	}

	_, err := FailingTransport().RoundTrip(newReq("http://example.invalid"))
	if err == nil {
		t.Fatal("FailingTransport returned nil error")
	}
	for _, want := range []string{pathSecret, headerSecret} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("FailingTransport error does not hold %q: %s", want, err)
		}
	}

	srv := EchoServer(t, http.StatusUnauthorized)
	resp, err := http.DefaultClient.Do(newReq(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{pathSecret, headerSecret} {
		if !strings.Contains(string(body), want) {
			t.Errorf("EchoServer body does not hold %q: %s", want, body)
		}
	}
}

func TestErrText(t *testing.T) {
	err := fmt.Errorf("outer: %w", fmt.Errorf("middle: %w", errors.New("inner")))
	if got, want := ErrText(t, err), "outer: middle: inner"; got != want {
		t.Errorf("ErrText = %q, want %q", got, want)
	}

	rec := &recordingTB{TB: t}
	if got := ErrText(rec, nil); got != "" {
		t.Errorf("ErrText(nil) = %q, want empty", got)
	}
	if len(rec.msgs) != 1 {
		t.Errorf("ErrText(nil): got %d failures, want 1", len(rec.msgs))
	}
}

func TestCaptureSlog(t *testing.T) {
	prev := slog.Default()
	t.Run("capture", func(t *testing.T) {
		buf := CaptureSlog(t)
		slog.Warn("captured line", slog.String("k", "v"))
		if !strings.Contains(buf.String(), "captured line") || !strings.Contains(buf.String(), "k=v") {
			t.Errorf("buffer = %q, want the logged line", buf.String())
		}
	})
	if slog.Default() != prev {
		t.Error("CaptureSlog did not restore the default logger")
	}
}
