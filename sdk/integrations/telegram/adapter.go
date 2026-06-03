// Package telegram provides a Studio SDK Telegram adapter.
// It implements sdk/core.Adapter and sdk/core.ChatCapable.
//
// Usage:
//
//	cfg := telegram.Config{BotToken: os.Getenv("TELEGRAM_BOT_TOKEN")}
//	a := telegram.New(cfg, nil)
//	core.Register(a)
package telegram

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

// Adapter implements core.Adapter and core.ChatCapable for Telegram.
type Adapter struct {
	cfg    Config
	logger *slog.Logger
}

// New creates a new Telegram adapter. logger may be nil to use slog.Default().
func New(cfg Config, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{cfg: cfg, logger: logger}
}

// Name returns the adapter identifier.
func (a *Adapter) Name() string { return "telegram" }

// NewChatBridge creates a ChatBridge using the given dependencies.
func (a *Adapter) NewChatBridge(deps core.ChatDeps) core.ChatBridge {
	allowedIDs := make(map[int64]bool, len(a.cfg.AllowedIDs))
	for _, id := range a.cfg.AllowedIDs {
		allowedIDs[id] = true
	}
	client := NewClient(a.cfg.BotToken, WithLogger(a.logger))
	return &bridge{
		client:     client,
		deps:       deps,
		allowedIDs: allowedIDs,
		logger:     a.logger,
	}
}
