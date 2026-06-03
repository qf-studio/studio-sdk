package slack

import (
	"context"
	"testing"

	"github.com/qf-studio/studio-sdk/sdk/core"
	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

func TestAdapterName(t *testing.T) {
	a := New(Config{AppToken: testutil.FakeSlackAppToken, BotToken: testutil.FakeSlackBotToken}, nil)
	if got := a.Name(); got != "slack" {
		t.Errorf("Name() = %q, want %q", got, "slack")
	}
}

func TestAdapterNewChatBridge(t *testing.T) {
	a := New(Config{
		AppToken:        testutil.FakeSlackAppToken,
		BotToken:        testutil.FakeSlackBotToken,
		AllowedChannels: []string{"C123"},
		AllowedUsers:    []string{"U456"},
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
	a := New(Config{AppToken: testutil.FakeSlackAppToken, BotToken: testutil.FakeSlackBotToken}, nil)
	var _ core.Adapter = a
	var _ core.ChatCapable = a
}

type noopHandler struct{}

func (h *noopHandler) HandleMessage(_ context.Context, _ core.MessageEvent) error { return nil }
