package discord

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

// fakeGatewayListener feeds pre-loaded events then closes the channel.
// When block is true, the channel never closes (used for ctx-cancel tests).
type fakeGatewayListener struct {
	events []GatewayEvent
	block  bool
}

func (f *fakeGatewayListener) StartListening(_ context.Context) (<-chan GatewayEvent, error) {
	if f.block {
		return make(chan GatewayEvent), nil
	}
	ch := make(chan GatewayEvent, len(f.events))
	for _, e := range f.events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

// newTestBridge creates a bridge backed by a fake HTTP server and fake gateway listener.
func newTestBridge(
	t *testing.T,
	apiHandler http.HandlerFunc,
	msgHandler core.MessageHandler,
	allowedGuilds []string,
	allowedChannels []string,
) (*bridge, *httptest.Server) {
	t.Helper()
	if apiHandler == nil {
		apiHandler = func(w http.ResponseWriter, r *http.Request) {}
	}
	srv := httptest.NewServer(apiHandler)
	t.Cleanup(srv.Close)

	client := NewClientWithBaseURL(testutil.FakeDiscordToken, srv.URL)

	ag := make(map[string]bool, len(allowedGuilds))
	for _, g := range allowedGuilds {
		ag[g] = true
	}
	ach := make(map[string]bool, len(allowedChannels))
	for _, c := range allowedChannels {
		ach[c] = true
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	br := &bridge{
		client:          client,
		gateway:         nil,
		deps:            core.ChatDeps{Handler: msgHandler},
		botID:           "BOT123",
		allowedGuilds:   ag,
		allowedChannels: ach,
		log:             logger,
	}
	return br, srv
}

// dispatchEvt builds a GatewayEvent with Op=0 (DISPATCH) for the given event type and data.
func dispatchEvt(t string, data interface{}) GatewayEvent {
	raw, _ := json.Marshal(data)
	return GatewayEvent{Op: OpcodeDispatch, T: &t, D: json.RawMessage(raw)}
}

// --- Bridge event routing tests ---

func TestBridgeHandleMessageCreate(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)

	evt := dispatchEvt("MESSAGE_CREATE", MessageCreate{
		ID:        "msg-1",
		ChannelID: "C123",
		Author:    User{ID: "U456", Username: "alice"},
		Content:   "hello world",
	})
	br.processGatewayEvent(context.Background(), evt)

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

func TestBridgeStripsGlobalBotMention(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)

	evt := dispatchEvt("MESSAGE_CREATE", MessageCreate{
		ID:        "msg-2",
		ChannelID: "C123",
		Author:    User{ID: "U456", Username: "alice"},
		Content:   "<@BOT123> deploy prod",
	})
	br.processGatewayEvent(context.Background(), evt)

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

	evt := dispatchEvt("MESSAGE_CREATE", MessageCreate{
		ID:        "msg-3",
		ChannelID: "C123",
		Author:    User{ID: "U456", Username: "alice"},
		Content:   "/run task-07 extra",
	})
	br.processGatewayEvent(context.Background(), evt)

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
	// Commands must NOT execute — no Text, just Command+Args.
	if ev.Text != "" {
		t.Errorf("Text should be empty for commands, got %q", ev.Text)
	}
}

func TestBridgeHandleInteractionCreate(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)

	evt := dispatchEvt("INTERACTION_CREATE", InteractionCreate{
		ID:        "INT1",
		Token:     "tok-abc",
		Type:      3,
		ChannelID: "C456",
		User:      &User{ID: "U123", Username: "bob"},
		Data:      InteractionData{CustomID: "approve:TASK-07"},
	})
	br.processGatewayEvent(context.Background(), evt)

	events := h.all()
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Action != "callback" {
		t.Errorf("Action = %q, want %q", ev.Action, "callback")
	}
	if ev.CallbackID != "INT1/tok-abc" {
		t.Errorf("CallbackID = %q, want %q", ev.CallbackID, "INT1/tok-abc")
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

// TestBridgeSanitizesInboundText verifies invisible Unicode is stripped before HandleMessage.
func TestBridgeSanitizesInboundText(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)

	// U+200B ZERO WIDTH SPACE embedded mid-word — classic invisible prompt injection.
	poisoned := "hello​world"
	evt := dispatchEvt("MESSAGE_CREATE", MessageCreate{
		ID:        "msg-s",
		ChannelID: "C123",
		Author:    User{ID: "U456", Username: "alice"},
		Content:   poisoned,
	})
	br.processGatewayEvent(context.Background(), evt)

	events := h.all()
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	got := events[0].Text
	if strings.ContainsRune(got, '​') {
		t.Errorf("handler received unsanitized text %q; invisible unicode must be stripped", got)
	}
	if got != "helloworld" {
		t.Errorf("sanitized text = %q, want %q", got, "helloworld")
	}
}

func TestBridgeBotMessagesIgnored(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)

	evt := dispatchEvt("MESSAGE_CREATE", MessageCreate{
		ID:        "bot-msg",
		ChannelID: "C123",
		Author:    User{ID: "BOT999", Bot: true},
		Content:   "I am a bot",
	})
	br.processGatewayEvent(context.Background(), evt)

	if len(h.all()) != 0 {
		t.Error("bot messages should be ignored")
	}
}

func TestBridgeAllowedGuildsEnforced(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, []string{"G999"}, nil)

	// Unauthorized guild → dropped.
	br.processGatewayEvent(context.Background(), dispatchEvt("MESSAGE_CREATE", MessageCreate{
		ID:        "m1",
		ChannelID: "C1",
		GuildID:   "G001",
		Author:    User{ID: "U1"},
		Content:   "secret",
	}))
	if len(h.all()) != 0 {
		t.Error("message from unauthorized guild should be dropped")
	}

	// Authorized guild → accepted.
	br.processGatewayEvent(context.Background(), dispatchEvt("MESSAGE_CREATE", MessageCreate{
		ID:        "m2",
		ChannelID: "C1",
		GuildID:   "G999",
		Author:    User{ID: "U1"},
		Content:   "ok",
	}))
	if len(h.all()) != 1 {
		t.Errorf("want 1 event for authorized guild, got %d", len(h.all()))
	}
}

func TestBridgeAllowedChannelsEnforced(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, []string{"C999"})

	br.processGatewayEvent(context.Background(), dispatchEvt("MESSAGE_CREATE", MessageCreate{
		ID:        "m1",
		ChannelID: "C123",
		Author:    User{ID: "U1"},
		Content:   "secret",
	}))
	if len(h.all()) != 0 {
		t.Error("message from unauthorized channel should be dropped")
	}

	br.processGatewayEvent(context.Background(), dispatchEvt("MESSAGE_CREATE", MessageCreate{
		ID:        "m2",
		ChannelID: "C999",
		Author:    User{ID: "U1"},
		Content:   "ok",
	}))
	if len(h.all()) != 1 {
		t.Errorf("want 1 event for authorized channel, got %d", len(h.all()))
	}
}

func TestBridgeAllowedChannelsEnforcedForCallbacks(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, []string{"C999"})

	evt := dispatchEvt("INTERACTION_CREATE", InteractionCreate{
		ID:        "INT1",
		Token:     "tok",
		Type:      3,
		ChannelID: "C123", // unauthorized
		User:      &User{ID: "U1"},
		Data:      InteractionData{CustomID: "btn"},
	})
	br.processGatewayEvent(context.Background(), evt)
	if len(h.all()) != 0 {
		t.Error("callback from unauthorized channel should be dropped")
	}
}

func TestBridgeNonComponentInteractionIgnored(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)

	// Type 2 = APPLICATION_COMMAND, not a button click.
	evt := dispatchEvt("INTERACTION_CREATE", InteractionCreate{
		ID:        "INT2",
		Token:     "tok",
		Type:      2,
		ChannelID: "C1",
		User:      &User{ID: "U1"},
		Data:      InteractionData{CustomID: "slash-cmd"},
	})
	br.processGatewayEvent(context.Background(), evt)
	if len(h.all()) != 0 {
		t.Error("non-component interactions should be ignored")
	}
}

// --- Send / Edit / Ack tests ---

func TestBridgeSend(t *testing.T) {
	h := &captureHandler{}
	var requestPath string
	br, srv := newTestBridge(t, nil, h, nil, nil)

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"msg-99","channel_id":"C123"}`))
	})

	ref, err := br.Send(context.Background(), core.OutboundMessage{
		ChannelID: "C123",
		Text:      "hello",
	})
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if !strings.Contains(requestPath, "/channels/C123/messages") {
		t.Errorf("unexpected path: %s", requestPath)
	}
	if ref.ChannelID != "C123" {
		t.Errorf("ref.ChannelID = %q, want %q", ref.ChannelID, "C123")
	}
	if ref.MessageID != "msg-99" {
		t.Errorf("ref.MessageID = %q, want %q", ref.MessageID, "msg-99")
	}
}

func TestBridgeSendWithButtons(t *testing.T) {
	h := &captureHandler{}
	var requestBody []byte
	br, srv := newTestBridge(t, nil, h, nil, nil)

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		requestBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		_, _ = w.Write([]byte(`{"id":"msg-42","channel_id":"C123"}`))
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

	var payload struct {
		Components []struct {
			Type       int `json:"type"`
			Components []struct {
				Type     int    `json:"type"`
				Label    string `json:"label"`
				CustomID string `json:"custom_id"`
			} `json:"components"`
		} `json:"components"`
	}
	if err := json.Unmarshal(requestBody, &payload); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if len(payload.Components) != 1 || payload.Components[0].Type != 1 {
		t.Fatalf("expected one action row (type=1), got %+v", payload.Components)
	}
	row := payload.Components[0]
	if len(row.Components) != 2 {
		t.Fatalf("action row has %d buttons, want 2", len(row.Components))
	}
	if row.Components[0].CustomID != "yes" {
		t.Errorf("button[0].custom_id = %q, want %q", row.Components[0].CustomID, "yes")
	}
	if row.Components[1].CustomID != "no" {
		t.Errorf("button[1].custom_id = %q, want %q", row.Components[1].CustomID, "no")
	}
}

func TestBridgeEdit(t *testing.T) {
	h := &captureHandler{}
	var requestPath string
	br, srv := newTestBridge(t, nil, h, nil, nil)

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"msg-1","content":"updated"}`))
	})

	err := br.Edit(context.Background(), core.MessageRef{ChannelID: "C123", MessageID: "msg-1"}, "updated")
	if err != nil {
		t.Fatalf("Edit error: %v", err)
	}
	if !strings.Contains(requestPath, "/channels/C123/messages/msg-1") {
		t.Errorf("unexpected path: %s", requestPath)
	}
}

func TestBridgeAck(t *testing.T) {
	h := &captureHandler{}
	var requestPath string
	br, srv := newTestBridge(t, nil, h, nil, nil)

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})

	err := br.Ack(context.Background(), "INT1/tok-abc")
	if err != nil {
		t.Fatalf("Ack error: %v", err)
	}
	if !strings.Contains(requestPath, "/interactions/INT1/tok-abc/callback") {
		t.Errorf("unexpected path: %s", requestPath)
	}
}

func TestBridgeAckInvalidCallbackID(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)

	err := br.Ack(context.Background(), "no-slash-here")
	if err == nil {
		t.Error("Ack with invalid callbackID should return error")
	}
}

func TestBridgeStartReturnsOnCtxCancel(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)
	br.gateway = &fakeGatewayListener{block: true}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- br.Start(ctx) }()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Start did not return after context cancel")
	}
}

func TestBridgeStartProcessesPreloadedEvents(t *testing.T) {
	h := &captureHandler{}
	br, _ := newTestBridge(t, nil, h, nil, nil)

	br.gateway = &fakeGatewayListener{events: []GatewayEvent{
		dispatchEvt("MESSAGE_CREATE", MessageCreate{
			ID:        "pre-1",
			ChannelID: "C1",
			Author:    User{ID: "U1"},
			Content:   "preloaded",
		}),
	}}

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

func TestButtonsToActionRow(t *testing.T) {
	rows := buttonsToActionRow([]core.Button{
		{Label: "A", ActionID: "action-a", Data: "va"},
		{Label: "B", ActionID: "action-b", Data: "vb"},
	})
	if len(rows) != 1 {
		t.Fatalf("want 1 action row, got %d", len(rows))
	}
	row := rows[0]
	if row.Type != 1 {
		t.Errorf("row.Type = %d, want 1 (ACTION_ROW)", row.Type)
	}
	if len(row.Components) != 2 {
		t.Fatalf("want 2 buttons, got %d", len(row.Components))
	}
	if row.Components[0].CustomID != "action-a" {
		t.Errorf("Components[0].CustomID = %q, want %q", row.Components[0].CustomID, "action-a")
	}
	if row.Components[1].CustomID != "action-b" {
		t.Errorf("Components[1].CustomID = %q, want %q", row.Components[1].CustomID, "action-b")
	}
}
