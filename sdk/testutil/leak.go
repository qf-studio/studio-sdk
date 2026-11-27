package testutil

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// CanaryToken is a plain marker used as a credential in leak tests. It matches
// no secret pattern check, so a leak test that finds it in an error or a log
// line proves the credential escaped without tripping a secret scanner.
const CanaryToken = "sdk-canary-7f3e1c9a-do-not-log"

// AssertNoCanaryLeak fails the test for each text that holds canary and names
// the index of that text.
func AssertNoCanaryLeak(t testing.TB, canary string, texts ...string) {
	t.Helper()
	if canary == "" {
		t.Fatal("AssertNoCanaryLeak: empty canary matches every text")
		return
	}
	for i, s := range texts {
		if strings.Contains(s, canary) {
			t.Errorf("text %d leaks the canary %q: %s", i, canary, s)
		}
	}
}

// FailingTransport returns an http.RoundTripper that fails each request. The
// error text holds the request line (with the full URL), each header and the
// body — the worst case of what a transport error can carry.
func FailingTransport() http.RoundTripper {
	return failingTransport{}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	dump, err := dumpRequest(req, req.URL.String())
	if err != nil {
		return nil, err
	}
	return nil, errors.New("testutil: failing transport: " + dump)
}

// EchoServer returns a test server that answers each request with status and
// a body that holds the request line, each header and the body of the request.
// The server is closed when the test ends.
func EchoServer(t testing.TB, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dump, err := dumpRequest(r, r.URL.RequestURI())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, dump)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ErrText returns the text of the full error chain — err.Error(), which for a
// chain built with %w holds the text of each wrapped layer. This is the text
// a host logs or posts. It fails the test for a nil error.
func ErrText(t testing.TB, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("ErrText: nil error")
		return ""
	}
	return err.Error()
}

// CaptureSlog sets the default slog logger to one that writes to a buffer for
// the duration of the test and returns the buffer. The previous default is
// restored when the test ends. Tests that use it must not run in parallel.
func CaptureSlog(t testing.TB) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// dumpRequest renders the request line (with target), each header and the
// body of req. It consumes and closes the request body.
func dumpRequest(req *http.Request, target string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s\n", req.Method, target, req.Proto)

	keys := make([]string, 0, len(req.Header))
	for k := range req.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range req.Header[k] {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}

	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return "", fmt.Errorf("testutil: read request body: %w", err)
		}
		b.WriteString("\n")
		b.Write(body)
	}
	return b.String(), nil
}
