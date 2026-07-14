package discord

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestListenNilConnNoPanic verifies that listen() does not dereference a nil
// g.conn: it must return (closing its output channel) instead of panicking.
func TestListenNilConnNoPanic(t *testing.T) {
	g := &GatewayClient{
		stopCh: make(chan struct{}),
		log:    discardLogger(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := g.listen(ctx)

	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("expected output channel to close without emitting an event")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listen did not return promptly on a nil connection")
	}
}

// TestListenConnRace exercises listen() reading from a snapshot of g.conn
// while a concurrent goroutine nils g.conn out from under it (the
// reconnect/close path), the way reconnectLoop and Close do in production.
// Run with -race to confirm there is no data race on g.conn.
func TestListenConnRace(t *testing.T) {
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	g := &GatewayClient{
		stopCh: make(chan struct{}),
		log:    discardLogger(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := 0; i < 20; i++ {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		g.mu.Lock()
		g.conn = conn
		g.mu.Unlock()

		out := g.listen(ctx)

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.mu.Lock()
			c := g.conn
			g.conn = nil
			g.mu.Unlock()
			if c != nil {
				_ = c.Close()
			}
		}()

		for range out {
		}
		wg.Wait()
	}
}
