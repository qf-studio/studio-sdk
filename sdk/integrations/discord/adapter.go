// Package discord provides a Studio SDK Discord adapter via the Gateway WebSocket.
// It implements sdk/core.Adapter and sdk/core.ChatCapable.
//
// Usage:
//
//	cfg := discord.Config{BotToken: os.Getenv("DISCORD_BOT_TOKEN")}
//	a := discord.New(cfg, nil)
//	core.Register(a)
package discord

import (
	"log/slog"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// Compile-time interface assertions.
var (
	_ core.Adapter     = (*Adapter)(nil)
	_ core.ChatCapable = (*Adapter)(nil)
	_ core.ChatBridge  = (*bridge)(nil)
)

// Adapter implements core.Adapter and core.ChatCapable for Discord.
type Adapter struct {
	cfg    Config
	logger *slog.Logger
}

// New creates a new Discord adapter. logger may be nil to use slog.Default().
func New(cfg Config, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{cfg: cfg, logger: logger}
}

// Name returns the adapter identifier.
func (a *Adapter) Name() string { return "discord" }

// NewChatBridge creates a ChatBridge using the given dependencies.
func (a *Adapter) NewChatBridge(deps core.ChatDeps) core.ChatBridge {
	allowedGuilds := make(map[string]bool, len(a.cfg.AllowedGuilds))
	for _, g := range a.cfg.AllowedGuilds {
		allowedGuilds[g] = true
	}
	allowedChannels := make(map[string]bool, len(a.cfg.AllowedChannels))
	for _, c := range a.cfg.AllowedChannels {
		allowedChannels[c] = true
	}

	client := NewClient(a.cfg.BotToken, WithLogger(a.logger))
	gateway := NewGatewayClient(a.cfg.BotToken, DefaultIntents, a.logger)

	return &bridge{
		client:          client,
		gateway:         gateway,
		deps:            deps,
		botID:           a.cfg.BotID,
		allowedGuilds:   allowedGuilds,
		allowedChannels: allowedChannels,
		log:             a.logger,
	}
}
