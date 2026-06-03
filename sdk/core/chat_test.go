package core

import (
	"context"
	"errors"
	"testing"
)

// --- fakes ---

type fakeChatBridge struct {
	started  bool
	sent     []OutboundMessage
	lastSent MessageRef
	edited   map[string]string // MessageID → new text
	acked    []string
}

func newFakeChatBridge() *fakeChatBridge {
	return &fakeChatBridge{edited: map[string]string{}}
}

func (f *fakeChatBridge) Start(ctx context.Context) error {
	f.started = true
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeChatBridge) Send(ctx context.Context, m OutboundMessage) (MessageRef, error) {
	f.sent = append(f.sent, m)
	ref := MessageRef{ChannelID: m.ChannelID, MessageID: "msg-1", ThreadID: m.ThreadID}
	f.lastSent = ref
	return ref, nil
}

func (f *fakeChatBridge) Edit(ctx context.Context, ref MessageRef, text string) error {
	f.edited[ref.MessageID] = text
	return nil
}

func (f *fakeChatBridge) Ack(ctx context.Context, callbackID string) error {
	f.acked = append(f.acked, callbackID)
	return nil
}

type fakeChatAdapter struct {
	bridge *fakeChatBridge
}

func (a *fakeChatAdapter) Name() string { return "fake-chat" }

func (a *fakeChatAdapter) NewChatBridge(deps ChatDeps) ChatBridge {
	return a.bridge
}

type fakeMessageHandler struct {
	received []MessageEvent
	err      error
}

func (h *fakeMessageHandler) HandleMessage(_ context.Context, ev MessageEvent) error {
	h.received = append(h.received, ev)
	return h.err
}

// --- interface conformance ---

var _ ChatBridge = (*fakeChatBridge)(nil)
var _ ChatCapable = (*fakeChatAdapter)(nil)
var _ MessageHandler = (*fakeMessageHandler)(nil)

// --- tests ---

func TestChatCapable_NewChatBridge(t *testing.T) {
	bridge := newFakeChatBridge()
	adapter := &fakeChatAdapter{bridge: bridge}
	handler := &fakeMessageHandler{}

	got := adapter.NewChatBridge(ChatDeps{Handler: handler})
	if got != bridge {
		t.Fatal("NewChatBridge returned unexpected bridge")
	}
}

func TestChatBridge_Send_ReturnsRef(t *testing.T) {
	bridge := newFakeChatBridge()

	msg := OutboundMessage{
		ChannelID: "C1",
		ThreadID:  "T0",
		Text:      "hello",
		Buttons:   []Button{{Label: "OK", ActionID: "ok", Data: "approve:TASK-1"}},
	}
	ref, err := bridge.Send(context.Background(), msg)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if ref.ChannelID != "C1" {
		t.Errorf("ref.ChannelID = %q, want %q", ref.ChannelID, "C1")
	}
	if len(bridge.sent) != 1 {
		t.Errorf("sent count = %d, want 1", len(bridge.sent))
	}
}

func TestChatBridge_Edit(t *testing.T) {
	bridge := newFakeChatBridge()
	ref := MessageRef{ChannelID: "C1", MessageID: "msg-42", ThreadID: ""}

	if err := bridge.Edit(context.Background(), ref, "updated text"); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if bridge.edited["msg-42"] != "updated text" {
		t.Errorf("edited text = %q, want %q", bridge.edited["msg-42"], "updated text")
	}
}

func TestChatBridge_Ack(t *testing.T) {
	bridge := newFakeChatBridge()

	if err := bridge.Ack(context.Background(), "cb-99"); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if len(bridge.acked) != 1 || bridge.acked[0] != "cb-99" {
		t.Errorf("acked = %v, want [cb-99]", bridge.acked)
	}
}

func TestMessageHandler_MessageVariant(t *testing.T) {
	handler := &fakeMessageHandler{}
	ev := MessageEvent{
		Action:    "message",
		MessageID: "m1",
		ChannelID: "C1",
		Text:      "hello world",
		Sender:    Identity{UserID: "U1", DisplayName: "Alice", IsBot: false},
	}
	if err := handler.HandleMessage(context.Background(), ev); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if len(handler.received) != 1 {
		t.Fatalf("received count = %d, want 1", len(handler.received))
	}
	got := handler.received[0]
	if got.Action != "message" || got.Text != "hello world" || got.Sender.UserID != "U1" {
		t.Errorf("unexpected event: %+v", got)
	}
}

func TestMessageHandler_CommandVariant(t *testing.T) {
	handler := &fakeMessageHandler{}
	ev := MessageEvent{
		Action:    "command",
		MessageID: "m2",
		ChannelID: "C1",
		Command:   "/run",
		Args:      []string{"TASK-42", "--force"},
		Sender:    Identity{UserID: "U2", DisplayName: "Bob"},
	}
	if err := handler.HandleMessage(context.Background(), ev); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	got := handler.received[0]
	if got.Command != "/run" || len(got.Args) != 2 || got.Args[0] != "TASK-42" {
		t.Errorf("unexpected command event: %+v", got)
	}
}

func TestMessageHandler_CallbackVariant(t *testing.T) {
	handler := &fakeMessageHandler{}
	ev := MessageEvent{
		Action:     "callback",
		MessageID:  "m3",
		ChannelID:  "C1",
		CallbackID: "cb-77",
		Data:       "approve:TASK-99",
		Sender:     Identity{UserID: "U3", DisplayName: "Carol", IsBot: false},
	}
	if err := handler.HandleMessage(context.Background(), ev); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	got := handler.received[0]
	if got.CallbackID != "cb-77" || got.Data != "approve:TASK-99" {
		t.Errorf("unexpected callback event: %+v", got)
	}
}

func TestMessageHandler_PropagatesError(t *testing.T) {
	want := errors.New("handler error")
	handler := &fakeMessageHandler{err: want}
	ev := MessageEvent{Action: "message", ChannelID: "C1", Text: "hi"}

	err := handler.HandleMessage(context.Background(), ev)
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}
