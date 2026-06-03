package discord

import (
	"context"
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func TestAdapterName(t *testing.T) {
	a := New(Config{BotToken: testutil.FakeDiscordToken}, nil)
	if got := a.Name(); got != "discord" {
		t.Errorf("Name() = %q, want %q", got, "discord")
	}
}

func TestAdapterNewChatBridge(t *testing.T) {
	a := New(Config{
		BotToken:        testutil.FakeDiscordToken,
		BotID:           "BOT123",
		AllowedGuilds:   []string{"G123"},
		AllowedChannels: []string{"C456"},
	}, nil)

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
	a := New(Config{BotToken: testutil.FakeDiscordToken}, nil)
	var _ core.Adapter = a
	var _ core.ChatCapable = a
}

type noopHandler struct{}

func (h *noopHandler) HandleMessage(_ context.Context, _ core.MessageEvent) error { return nil }
