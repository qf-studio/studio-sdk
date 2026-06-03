package slack

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
	"time"

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

// fakeSocketListener feeds a pre-loaded set of events then closes the channel.
// When block is true, the channel is never closed (used for ctx-cancel tests).
type fakeSocketListener struct {
	events []SocketModeEvent
	block  bool
}

func (f *fakeSocketListener) Listen(ctx context.Context) (<-chan SocketModeEvent, error) {
	if f.block {
		// Returns a channel that never receives or closes (blocks until ctx done).
		ch := make(chan SocketModeEvent)
		return ch, nil
	}
	ch := make(chan SocketModeEvent, len(f.events))
	for _, e := range f.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

// newTestBridge creates a bridge pointing at a fake HTTP server.
func newTestBridge(
	t *testing.T,
	apiHandler http.HandlerFunc,
	msgHandler core.MessageHandler,
	allowedCh []string,
	allowedUser []string,
) (*bridge, *httptest.Server) {
	t.Helper()
	if apiHandler == nil {
		apiHandler = func(w http.ResponseWriter, r *http.Request) {}
	}
	srv := httptest.NewServer(apiHandler)
	t.Cleanup(srv.Close)

	client := NewClientWithBaseURL(testutil.FakeSlackBotToken, srv.URL)

	ach := make(map[string]bool, len(allowedCh))
	for _, c := range allowedCh {
		ach[c] = true
	}
	auser := make(map[string]bool, len(allowedUser))
	for _, u := range allowedUser {
		auser[u] = true
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	br := &bridge{
		client:          client,
		socket:          nil,
		deps:            core.ChatDeps{Handler: msgHandler},
		allowedChannels: ach,
		allowedUsers:    auser,
		logger:          logger,
	}
	return br, srv
}

// eventsAPIEvt constructs a SocketModeEvent of type events_api with the given inner event JSON.
func eventsAPIEvt(innerJSON string) SocketModeEvent {
	payload := `{"token":"t","team_id":"T1","type":"event_callback","event":` + innerJSON + `}`
	return SocketModeEvent{
		Type:       SocketEventMessage,
		EnvelopeID: "env-001",
		Payload:    json.RawMessage(payload),
	}
}

// --- Bridge event routing tests ---

func TestBridgeHandleMessage(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)

	evt := eventsAPIEvt(`{"type":"message","channel":"C123","user":"U456","text":"hello world","ts":"1.0"}`)
	br.processSocketEvent(context.Background(), evt)

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
	if ev.ChannelID != "C123" {
		t.Errorf("ChannelID = %q, want %q", ev.ChannelID, "C123")
	}
	if ev.Sender.UserID != "U456" {
		t.Errorf("Sender.UserID = %q, want %q", ev.Sender.UserID, "U456")
	}
}

func TestBridgeHandleAppMentionStripsBotPrefix(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)

	evt := eventsAPIEvt(`{"type":"app_mention","channel":"C123","user":"U456","text":"<@UBOT99> deploy prod","ts":"2.0"}`)
	br.processSocketEvent(context.Background(), evt)

	events := h.all()
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if events[0].Text != "deploy prod" {
		t.Errorf("Text = %q, want %q (bot mention should be stripped)", events[0].Text, "deploy prod")
	}
}

func TestBridgeHandleCommand(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)

	evt := eventsAPIEvt(`{"type":"message","channel":"C123","user":"U456","text":"/run task-07 extra","ts":"3.0"}`)
	br.processSocketEvent(context.Background(), evt)

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
	// Commands must NOT execute — Action:"command", no text field.
	if ev.Text != "" {
		t.Errorf("Text should be empty for commands, got %q", ev.Text)
	}
}

func TestBridgeHandleBlockActions(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)

	payload := `{"type":"block_actions","user":{"id":"U123","username":"alice","name":"Alice"},"channel":{"id":"C456"},"message":{"ts":"1.1"},"actions":[{"type":"button","action_id":"approve","block_id":"b1","value":"approve:TASK-07"}]}`
	evt := SocketModeEvent{
		Type:       SocketEventInteraction,
		EnvelopeID: "env-int-001",
		Payload:    json.RawMessage(payload),
	}
	br.processSocketEvent(context.Background(), evt)

	events := h.all()
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Action != "callback" {
		t.Errorf("Action = %q, want %q", ev.Action, "callback")
	}
	if ev.CallbackID != "approve" {
		t.Errorf("CallbackID = %q, want %q", ev.CallbackID, "approve")
	}
	if ev.Data != "approve:TASK-07" {
		t.Errorf("Data = %q, want %q", ev.Data, "approve:TASK-07")
	}
	if ev.ChannelID != "C456" {
		t.Errorf("ChannelID = %q, want %q", ev.ChannelID, "C456")
	}
	if ev.Sender.UserID != "U123" {
		t.Errorf("Sender.UserID = %q, want %q", ev.Sender.UserID, "U123")
	}
}

// TestBridgeSanitizesInboundText is the ASCII-smuggling guard: invisible Unicode
// in the inbound Text must be stripped before the handler sees it.
func TestBridgeSanitizesInboundText(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)

	// U+200B ZERO WIDTH SPACE embedded mid-word — classic invisible prompt injection.
	poisoned := "hello​world"
	evt := eventsAPIEvt(`{"type":"message","channel":"C123","user":"U456","text":"` + poisoned + `","ts":"4.0"}`)
	br.processSocketEvent(context.Background(), evt)

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

func TestBridgeBotMessagesIgnored(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)

	// bot_id present → bot message, should be dropped.
	evt := eventsAPIEvt(`{"type":"message","channel":"C123","user":"U456","text":"bot speaks","ts":"5.0","bot_id":"B999"}`)
	br.processSocketEvent(context.Background(), evt)

	if len(h.all()) != 0 {
		t.Error("bot messages should be ignored")
	}
}

func TestBridgeAllowedChannelsEnforced(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, []string{"C999"}, nil)

	// Unauthorized channel → dropped.
	evt := eventsAPIEvt(`{"type":"message","channel":"C123","user":"U456","text":"secret","ts":"6.0"}`)
	br.processSocketEvent(context.Background(), evt)
	if len(h.all()) != 0 {
		t.Error("message from unauthorized channel should be dropped")
	}

	// Authorized channel → accepted.
	evt2 := eventsAPIEvt(`{"type":"message","channel":"C999","user":"U456","text":"ok","ts":"6.1"}`)
	br.processSocketEvent(context.Background(), evt2)
	if len(h.all()) != 1 {
		t.Errorf("want 1 event for authorized channel, got %d", len(h.all()))
	}
}

func TestBridgeAllowedUsersEnforced(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, []string{"U999"})

	// Unauthorized user → dropped.
	evt := eventsAPIEvt(`{"type":"message","channel":"C123","user":"U456","text":"secret","ts":"7.0"}`)
	br.processSocketEvent(context.Background(), evt)
	if len(h.all()) != 0 {
		t.Error("message from unauthorized user should be dropped")
	}

	// Authorized user → accepted.
	evt2 := eventsAPIEvt(`{"type":"message","channel":"C123","user":"U999","text":"ok","ts":"7.1"}`)
	br.processSocketEvent(context.Background(), evt2)
	if len(h.all()) != 1 {
		t.Errorf("want 1 event for authorized user, got %d", len(h.all()))
	}
}

func TestBridgeAllowedChannelsEnforcedForCallbacks(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, []string{"C999"}, nil)

	// Unauthorized channel in block_actions → dropped.
	payload := `{"type":"block_actions","user":{"id":"U123"},"channel":{"id":"C456"},"message":{"ts":"1.1"},"actions":[{"action_id":"btn","value":"x"}]}`
	evt := SocketModeEvent{Type: SocketEventInteraction, Payload: json.RawMessage(payload)}
	br.processSocketEvent(context.Background(), evt)
	if len(h.all()) != 0 {
		t.Error("callback from unauthorized channel should be dropped")
	}
}

// --- Send / Edit / Ack tests ---

func TestBridgeSend(t *testing.T) {
	h := &captureHandler{}
	br, srv := newTestBridge(t, nil, h, nil, nil)

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/chat.postMessage") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true,"ts":"1234567890.123","channel":"C123"}`))
	})

	ref, err := br.Send(context.Background(), core.OutboundMessage{
		ChannelID: "C123",
		Text:      "hello",
	})
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if ref.ChannelID != "C123" {
		t.Errorf("ref.ChannelID = %q, want %q", ref.ChannelID, "C123")
	}
	if ref.MessageID != "1234567890.123" {
		t.Errorf("ref.MessageID = %q, want %q", ref.MessageID, "1234567890.123")
	}
}

func TestBridgeSendWithButtons(t *testing.T) {
	h := &captureHandler{}
	br, srv := newTestBridge(t, nil, h, nil, nil)

	var requestBody []byte
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/chat.postMessage") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var err error
		requestBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"ts":"9876543210.456","channel":"C123"}`))
	})

	_, err := br.Send(context.Background(), core.OutboundMessage{
		ChannelID: "C123",
		Text:      "choose:",
		Buttons: []core.Button{
			{Label: "Yes", ActionID: "yes", Data: "yes"},
			{Label: "No", ActionID: "no", Data: "no"},
		},
	})
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}

	// Verify the request body contains blocks with an actions block.
	var msg map[string]interface{}
	if err := json.Unmarshal(requestBody, &msg); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	blocks, ok := msg["blocks"].([]interface{})
	if !ok || len(blocks) == 0 {
		t.Fatal("expected blocks in interactive message")
	}

	// Find the actions block.
	var foundActions bool
	for _, b := range blocks {
		blk, ok := b.(map[string]interface{})
		if !ok {
			continue
		}
		if blk["type"] == "actions" {
			foundActions = true
			elements, ok := blk["elements"].([]interface{})
			if !ok || len(elements) != 2 {
				t.Errorf("actions block has %d elements, want 2", len(elements))
			}
		}
	}
	if !foundActions {
		t.Error("no actions block found in Block Kit message")
	}
}

func TestBridgeEdit(t *testing.T) {
	h := &captureHandler{}
	br, srv := newTestBridge(t, nil, h, nil, nil)

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/chat.update") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	err := br.Edit(context.Background(), core.MessageRef{ChannelID: "C123", MessageID: "1.1"}, "updated")
	if err != nil {
		t.Fatalf("Edit error: %v", err)
	}
}

func TestBridgeAckIsNoop(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)

	if err := br.Ack(context.Background(), "any-callback-id"); err != nil {
		t.Errorf("Ack should return nil, got: %v", err)
	}
}

func TestBridgeStartReturnsOnCtxCancel(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)
	br.socket = &fakeSocketListener{block: true}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- br.Start(ctx) }()

	cancel()

	select {
	case <-done:
		// returned, test passes
	case <-time.After(2 * time.Second):
		t.Error("Start did not return after context cancel")
	}
}

func TestBridgeStartProcessesPreloadedEvents(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)

	payload := `{"token":"t","team_id":"T1","type":"event_callback","event":{"type":"message","channel":"C1","user":"U1","text":"preloaded","ts":"1.0"}}`
	br.socket = &fakeSocketListener{events: []SocketModeEvent{{
		Type:       SocketEventMessage,
		EnvelopeID: "pre-001",
		Payload:    json.RawMessage(payload),
	}}}

	err := br.Start(context.Background())
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	events := h.all()
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if events[0].Text != "preloaded" {
		t.Errorf("Text = %q, want %q", events[0].Text, "preloaded")
	}
}

// --- Helper unit tests ---

func TestButtonsToBlockKit(t *testing.T) {
	blocks := buttonsToBlockKit("pick one", []core.Button{
		{Label: "A", ActionID: "a", Data: "va"},
		{Label: "B", ActionID: "b", Data: "vb"},
	})
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks (section + actions), got %d", len(blocks))
	}
	section, ok := blocks[0].(map[string]interface{})
	if !ok || section["type"] != "section" {
		t.Errorf("blocks[0] = %+v, want section block", blocks[0])
	}
	actions, ok := blocks[1].(map[string]interface{})
	if !ok || actions["type"] != "actions" {
		t.Errorf("blocks[1] = %+v, want actions block", blocks[1])
	}
	elements := actions["elements"].([]interface{})
	if len(elements) != 2 {
		t.Errorf("elements count = %d, want 2", len(elements))
	}
	btn0 := elements[0].(map[string]interface{})
	if btn0["action_id"] != "a" {
		t.Errorf("btn0.action_id = %v, want %q", btn0["action_id"], "a")
	}
	if btn0["value"] != "va" {
		t.Errorf("btn0.value = %v, want %q", btn0["value"], "va")
	}
}

func TestIdentityFromSlackUser(t *testing.T) {
	u := &InteractionUser{ID: "U1", Username: "uname", Name: "Alice"}
	id := identityFromSlackUser(u)
	if id.UserID != "U1" {
		t.Errorf("UserID = %q, want %q", id.UserID, "U1")
	}
	if id.DisplayName != "Alice" {
		t.Errorf("DisplayName = %q, want %q (prefer Name over Username)", id.DisplayName, "Alice")
	}

	// When Name is empty, fall back to Username.
	u2 := &InteractionUser{ID: "U2", Username: "uname2"}
	id2 := identityFromSlackUser(u2)
	if id2.DisplayName != "uname2" {
		t.Errorf("DisplayName = %q, want %q (fallback to Username)", id2.DisplayName, "uname2")
	}

	// Nil user.
	empty := identityFromSlackUser(nil)
	if empty.UserID != "" {
		t.Errorf("nil user: UserID = %q, want empty", empty.UserID)
	}
}
