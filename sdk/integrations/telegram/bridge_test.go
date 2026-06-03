package telegram

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// captureHandler records every MessageEvent it receives.
type captureHandler struct {
	mu     sync.Mutex
	events []core.MessageEvent
}

func (h *captureHandler) HandleMessage(_ context.Context, ev core.MessageEvent) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, ev)
	return nil
}

func (h *captureHandler) all() []core.MessageEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]core.MessageEvent, len(h.events))
	copy(out, h.events)
	return out
}

// newBridgeTestServer starts an httptest server and returns a bridge whose
// client points at it.
func newBridgeTestServer(t *testing.T, handler http.HandlerFunc, handler2 *captureHandler, allowedIDs []int64) (*httptest.Server, *bridge) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := NewClientWithBaseURL(testutil.FakeTelegramToken, srv.URL)
	allow := make(map[int64]bool, len(allowedIDs))
	for _, id := range allowedIDs {
		allow[id] = true
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	br := &bridge{
		client:     client,
		deps:       core.ChatDeps{Handler: handler2},
		allowedIDs: allow,
		logger:     logger,
	}
	return srv, br
}

// --- Tests ---

func TestBridgeProcessMessage(t *testing.T) {
	h := &captureHandler{}
	_, br := newBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {}, h, nil)

	update := &Update{
		UpdateID: 1,
		Message: &Message{
			MessageID: 10,
			From:      &User{ID: 100, FirstName: "Alice"},
			Chat:      &Chat{ID: 200, Type: "private"},
			Text:      "hello world",
		},
	}
	br.processUpdate(context.Background(), update)

	events := h.all()
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Action != "message" {
		t.Errorf("Action = %q, want %q", ev.Action, "message")
	}
	if ev.Text != "hello world" {
		t.Errorf("Text = %q, want %q", ev.Text, "hello world")
	}
	if ev.ChannelID != "200" {
		t.Errorf("ChannelID = %q, want %q", ev.ChannelID, "200")
	}
	if ev.Sender.UserID != "100" {
		t.Errorf("Sender.UserID = %q, want %q", ev.Sender.UserID, "100")
	}
}

func TestBridgeProcessCommand(t *testing.T) {
	h := &captureHandler{}
	_, br := newBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {}, h, nil)

	update := &Update{
		UpdateID: 2,
		Message: &Message{
			MessageID: 11,
			From:      &User{ID: 100, FirstName: "Alice"},
			Chat:      &Chat{ID: 200},
			Text:      "/run task-07 extra",
		},
	}
	br.processUpdate(context.Background(), update)

	events := h.all()
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Action != "command" {
		t.Errorf("Action = %q, want %q", ev.Action, "command")
	}
	if ev.Command != "/run" {
		t.Errorf("Command = %q, want %q", ev.Command, "/run")
	}
	if len(ev.Args) != 2 || ev.Args[0] != "task-07" || ev.Args[1] != "extra" {
		t.Errorf("Args = %v, want [task-07 extra]", ev.Args)
	}
	// Commands must NOT execute — they emit Action:"command" and nothing else.
	if ev.Text != "" {
		t.Errorf("Text should be empty for commands, got %q", ev.Text)
	}
}

func TestBridgeProcessCallback(t *testing.T) {
	h := &captureHandler{}
	_, br := newBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {}, h, nil)

	update := &Update{
		UpdateID: 3,
		CallbackQuery: &CallbackQuery{
			ID:   "cbq-42",
			From: &User{ID: 100, FirstName: "Alice"},
			Message: &Message{
				MessageID: 20,
				Chat:      &Chat{ID: 200},
			},
			Data: "approve:TASK-07",
		},
	}
	br.processUpdate(context.Background(), update)

	events := h.all()
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Action != "callback" {
		t.Errorf("Action = %q, want %q", ev.Action, "callback")
	}
	if ev.CallbackID != "cbq-42" {
		t.Errorf("CallbackID = %q, want %q", ev.CallbackID, "cbq-42")
	}
	if ev.Data != "approve:TASK-07" {
		t.Errorf("Data = %q, want %q", ev.Data, "approve:TASK-07")
	}
	if ev.ChannelID != "200" {
		t.Errorf("ChannelID = %q, want %q", ev.ChannelID, "200")
	}
}

// TestBridgeSanitizesInboundText is the ASCII-smuggling guard: invisible Unicode
// in the inbound Text must be stripped before the handler sees it.
func TestBridgeSanitizesInboundText(t *testing.T) {
	h := &captureHandler{}
	_, br := newBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {}, h, nil)

	// U+200B ZERO WIDTH SPACE embedded mid-word — classic invisible prompt injection.
	poisoned := "hello​world"
	update := &Update{
		UpdateID: 4,
		Message: &Message{
			MessageID: 30,
			From:      &User{ID: 100, FirstName: "Alice"},
			Chat:      &Chat{ID: 200},
			Text:      poisoned,
		},
	}
	br.processUpdate(context.Background(), update)

	events := h.all()
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	got := events[0].Text
	if strings.Contains(got, "​") {
		t.Errorf("handler received unsanitized text %q; invisible unicode must be stripped before HandleMessage", got)
	}
	if got != "helloworld" {
		t.Errorf("sanitized text = %q, want %q", got, "helloworld")
	}
}

func TestBridgeAllowedIDsEnforcedForMessages(t *testing.T) {
	h := &captureHandler{}
	// Only chat ID 999 is allowed.
	_, br := newBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {}, h, []int64{999})

	// Unauthorized chat
	update := &Update{
		UpdateID: 5,
		Message: &Message{
			MessageID: 40,
			From:      &User{ID: 123},
			Chat:      &Chat{ID: 456},
			Text:      "secret",
		},
	}
	br.processUpdate(context.Background(), update)
	if len(h.all()) != 0 {
		t.Error("handler should not be called for unauthorized sender/chat")
	}

	// Authorized chat
	update2 := &Update{
		UpdateID: 6,
		Message: &Message{
			MessageID: 41,
			From:      &User{ID: 123},
			Chat:      &Chat{ID: 999},
			Text:      "ok",
		},
	}
	br.processUpdate(context.Background(), update2)
	if len(h.all()) != 1 {
		t.Errorf("want 1 event for authorized chat, got %d", len(h.all()))
	}
}

func TestBridgeAllowedIDsEnforcedForCallbacks(t *testing.T) {
	h := &captureHandler{}
	_, br := newBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {}, h, []int64{999})

	// Unauthorized user
	update := &Update{
		UpdateID: 7,
		CallbackQuery: &CallbackQuery{
			ID:      "cb1",
			From:    &User{ID: 123},
			Message: &Message{MessageID: 1, Chat: &Chat{ID: 456}},
			Data:    "x",
		},
	}
	br.processUpdate(context.Background(), update)
	if len(h.all()) != 0 {
		t.Error("callback from unauthorized user should be dropped")
	}
}

func TestBridgeSend(t *testing.T) {
	h := &captureHandler{}
	srv, br := newBridgeTestServer(t, nil, h, nil)

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/sendMessage") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(SendMessageResponse{OK: true, Result: &Result{MessageID: 55}})
	})

	ref, err := br.Send(context.Background(), core.OutboundMessage{
		ChannelID: "200",
		Text:      "hi there",
	})
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if ref.ChannelID != "200" {
		t.Errorf("ref.ChannelID = %q, want %q", ref.ChannelID, "200")
	}
	if ref.MessageID != "55" {
		t.Errorf("ref.MessageID = %q, want %q", ref.MessageID, "55")
	}
}

func TestBridgeSendWithKeyboard(t *testing.T) {
	h := &captureHandler{}
	srv, br := newBridgeTestServer(t, nil, h, nil)

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req SendMessageRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("unmarshal request: %v", err)
		}
		if req.ReplyMarkup == nil {
			t.Error("ReplyMarkup should not be nil when buttons are present")
		} else if len(req.ReplyMarkup.InlineKeyboard) != 1 {
			t.Errorf("keyboard rows = %d, want 1", len(req.ReplyMarkup.InlineKeyboard))
		} else if len(req.ReplyMarkup.InlineKeyboard[0]) != 2 {
			t.Errorf("keyboard cols = %d, want 2", len(req.ReplyMarkup.InlineKeyboard[0]))
		}
		_ = json.NewEncoder(w).Encode(SendMessageResponse{OK: true, Result: &Result{MessageID: 66}})
	})

	_, err := br.Send(context.Background(), core.OutboundMessage{
		ChannelID: "200",
		Text:      "choose:",
		Buttons: []core.Button{
			{Label: "Yes", ActionID: "yes", Data: "yes"},
			{Label: "No", ActionID: "no", Data: "no"},
		},
	})
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
}

func TestBridgeEdit(t *testing.T) {
	h := &captureHandler{}
	srv, br := newBridgeTestServer(t, nil, h, nil)

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/editMessageText") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(SendMessageResponse{OK: true, Result: &Result{MessageID: 10}})
	})

	err := br.Edit(context.Background(), core.MessageRef{ChannelID: "200", MessageID: "10"}, "updated text")
	if err != nil {
		t.Fatalf("Edit error: %v", err)
	}
}

func TestBridgeEditInvalidMessageID(t *testing.T) {
	h := &captureHandler{}
	_, br := newBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {}, h, nil)

	err := br.Edit(context.Background(), core.MessageRef{ChannelID: "200", MessageID: "notanint"}, "x")
	if err == nil {
		t.Error("expected error for non-integer MessageID, got nil")
	}
}

func TestBridgeAck(t *testing.T) {
	h := &captureHandler{}
	srv, br := newBridgeTestServer(t, nil, h, nil)

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/answerCallbackQuery") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	})

	err := br.Ack(context.Background(), "cbq-99")
	if err != nil {
		t.Fatalf("Ack error: %v", err)
	}
}

func TestBridgeStartReturnsOnCtxCancel(t *testing.T) {
	h := &captureHandler{}
	srv, br := newBridgeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(GetUpdatesResponse{OK: true, Result: []*Update{}})
	}, h, nil)
	_ = srv

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := br.Start(ctx)
	if err == nil {
		t.Error("Start should return non-nil error when ctx is already cancelled")
	}
}

func TestButtionsToKeyboard(t *testing.T) {
	buttons := []core.Button{
		{Label: "A", Data: "a"},
		{Label: "B", Data: "b"},
	}
	kb := buttonsToKeyboard(buttons)
	if len(kb) != 1 {
		t.Fatalf("rows = %d, want 1", len(kb))
	}
	if len(kb[0]) != 2 {
		t.Fatalf("cols = %d, want 2", len(kb[0]))
	}
	if kb[0][0].Text != "A" || kb[0][0].CallbackData != "a" {
		t.Errorf("button[0] = %+v", kb[0][0])
	}
}

func TestIdentityFromUser(t *testing.T) {
	u := &User{ID: 7, FirstName: "Alice", LastName: "Smith", IsBot: false}
	id := identityFromUser(u)
	if id.UserID != "7" {
		t.Errorf("UserID = %q, want %q", id.UserID, "7")
	}
	if id.DisplayName != "Alice Smith" {
		t.Errorf("DisplayName = %q, want %q", id.DisplayName, "Alice Smith")
	}
	if id.IsBot {
		t.Error("IsBot should be false")
	}

	// nil user
	empty := identityFromUser(nil)
	if empty.UserID != "" {
		t.Errorf("nil user: UserID = %q, want empty", empty.UserID)
	}
}
