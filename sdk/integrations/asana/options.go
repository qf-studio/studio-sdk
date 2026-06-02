package asana

import "log/slog"

// loggerConfig holds common configuration for Asana components.
type loggerConfig struct {
	logger *slog.Logger
}

// Option configures an Asana component (Notifier or WebhookHandler).
type Option func(*loggerConfig)

// WithLogger sets the logger used by an Asana component.
// Defaults to slog.Default() when not provided.
func WithLogger(logger *slog.Logger) Option {
	return func(cfg *loggerConfig) {
		cfg.logger = logger
	}
}
