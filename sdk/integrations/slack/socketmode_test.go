package slack

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func TestSocketModeHandler_Disconnect(t *testing.T) {
	tests := []struct {
		name    string
		frame   string
		wantAck string // expected ack frame; empty means none
	}{
		{
			name:  "no envelope_id, no ack",
			frame: `{"type":"disconnect","reason":"refresh_requested"}`,
		},
		{
			name:    "envelope_id present, acked",
			frame:   `{"type":"disconnect","envelope_id":"env-9","reason":"link_disabled"}`,
			wantAck: `{"envelope_id":"env-9"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var texts []string
			serverDone := make(chan struct{})

			up := websocket.Upgrader{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(serverDone)
				c, err := up.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = c.Close() }()
				if err := c.WriteMessage(websocket.TextMessage, []byte(tc.frame)); err != nil {
					return
				}
				// Read until the client closes the connection.
				for {
					mt, data, err := c.ReadMessage()
					if err != nil {
						return
					}
					if mt == websocket.TextMessage {
						mu.Lock()
						texts = append(texts, string(data))
						mu.Unlock()
					}
				}
			}))
			defer srv.Close()

			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			h, events := NewSocketModeHandler(conn, slog.New(slog.NewTextHandler(testWriter{t}, nil)))

			runDone := make(chan struct{})
			go func() { h.Run(); close(runDone) }()

			select {
			case ev, ok := <-events:
				if !ok || ev.Type != SocketEventDisconnect {
					t.Fatalf("got %+v ok=%v, want SocketEventDisconnect", ev, ok)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no disconnect event")
			}
			select {
			case <-runDone:
			case <-time.After(3 * time.Second):
				t.Fatal("Run did not return after disconnect")
			}
			select {
			case <-serverDone:
			case <-time.After(3 * time.Second):
				t.Fatal("connection not closed")
			}
			if _, ok := <-events; ok {
				t.Error("want exactly one event")
			}

			mu.Lock()
			defer mu.Unlock()
			if tc.wantAck == "" {
				if len(texts) != 0 {
					t.Errorf("unexpected frames written: %v", texts)
				}
			} else if len(texts) != 1 || texts[0] != tc.wantAck {
				t.Errorf("frames = %v, want [%s]", texts, tc.wantAck)
			}
		})
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

// TestCanary_SocketModeDoesNotLeakCredential builds the Socket Mode client
// with the canary as the app token and drives OpenConnection through a
// failing transport and through an echo server that answers 401 and 500. No
// error text and no captured log line may hold the canary, and each error
// still matches ErrConnectionOpen.
func TestCanary_SocketModeDoesNotLeakCredential(t *testing.T) {
	logBuf := testutil.CaptureSlog(t)
	ctx := context.Background()
	var texts []string

	collect := func(err error) {
		t.Helper()
		texts = append(texts, testutil.ErrText(t, err))
		if !errors.Is(err, ErrConnectionOpen) {
			t.Errorf("errors.Is(err, ErrConnectionOpen) = false; err = %v", err)
		}
	}

	failing := NewSocketModeClient(testutil.CanaryToken, nil,
		WithSocketModeHTTPClient(&http.Client{Transport: testutil.FailingTransport()}))
	_, err := failing.OpenConnection(ctx)
	collect(err)
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("errors.As(err, *url.Error) = false; err = %v", err)
	}
	texts = append(texts, urlErr.Error())

	for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
		srv := testutil.EchoServer(t, status)
		echo := NewSocketModeClient(testutil.CanaryToken, nil, WithSocketModeAPIURL(srv.URL))
		_, err := echo.OpenConnection(ctx)
		collect(err)
	}

	texts = append(texts, logBuf.String())
	testutil.AssertNoCanaryLeak(t, testutil.CanaryToken, texts...)
}
