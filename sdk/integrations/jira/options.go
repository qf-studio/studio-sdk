package jira

import "log/slog"

// loggerConfig holds common configuration for Jira components.
type loggerConfig struct {
	logger *slog.Logger
}

// Option configures a Jira component (Notifier or WebhookHandler).
type Option func(*loggerConfig)

// WithLogger sets the logger used by a Jira component.
// Defaults to slog.Default() when not provided.
func WithLogger(logger *slog.Logger) Option {
	return func(cfg *loggerConfig) {
		cfg.logger = logger
	}
}
