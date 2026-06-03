// Package slack provides a Studio SDK Slack adapter via Socket Mode.
// It implements sdk/core.Adapter and sdk/core.ChatCapable.
//
// Usage:
//
//	cfg := slack.Config{AppToken: os.Getenv("SLACK_APP_TOKEN"), BotToken: os.Getenv("SLACK_BOT_TOKEN")}
//	a := slack.New(cfg, nil)
//	core.Register(a)
package slack

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

// Adapter implements core.Adapter and core.ChatCapable for Slack.
type Adapter struct {
	cfg    Config
	logger *slog.Logger
}

// New creates a new Slack adapter. logger may be nil to use slog.Default().
func New(cfg Config, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Adapter{cfg: cfg, logger: logger}
}

// Name returns the adapter identifier.
func (a *Adapter) Name() string { return "slack" }

// NewChatBridge creates a ChatBridge using the given dependencies.
func (a *Adapter) NewChatBridge(deps core.ChatDeps) core.ChatBridge {
	allowedCh := make(map[string]bool, len(a.cfg.AllowedChannels))
	for _, c := range a.cfg.AllowedChannels {
		allowedCh[c] = true
	}
	allowedUser := make(map[string]bool, len(a.cfg.AllowedUsers))
	for _, u := range a.cfg.AllowedUsers {
		allowedUser[u] = true
	}

	client := NewClient(a.cfg.BotToken, WithLogger(a.logger))
	socket := NewSocketModeClient(a.cfg.AppToken, a.logger)

	return &bridge{
		client:          client,
		socket:          socket,
		deps:            deps,
		allowedChannels: allowedCh,
		allowedUsers:    allowedUser,
		logger:          a.logger,
	}
}
