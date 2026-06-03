package telegram

import (
	"context"
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func TestAdapterName(t *testing.T) {
	a := New(Config{BotToken: testutil.FakeTelegramToken}, nil)
	if got := a.Name(); got != "telegram" {
		t.Errorf("Name() = %q, want %q", got, "telegram")
	}
}

func TestAdapterNewChatBridge(t *testing.T) {
	a := New(Config{BotToken: testutil.FakeTelegramToken, AllowedIDs: []int64{42}}, nil)
	deps := core.ChatDeps{Handler: &noopHandler{}}
	br := a.NewChatBridge(deps)
	if br == nil {
		t.Fatal("NewChatBridge returned nil")
	}
	if _, ok := br.(*bridge); !ok {
		t.Errorf("NewChatBridge returned %T, want *bridge", br)
	}
}

func TestAdapterImplementsInterfaces(t *testing.T) {
	a := New(Config{BotToken: testutil.FakeTelegramToken}, nil)
	var _ core.Adapter = a
	var _ core.ChatCapable = a
}

// noopHandler discards every event.
type noopHandler struct{}

func (h *noopHandler) HandleMessage(_ context.Context, _ core.MessageEvent) error { return nil }
