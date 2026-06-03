package telegram

import (
	"log/slog"

	"github.com/qf-studio/studio-sdk/sdk/util/text"
)

// sanitizeMessageText strips invisible Unicode format characters from inbound
// message text. Emits a slog.Warn when runes are stripped — potential
// prompt-injection signal.
func sanitizeMessageText(s string, logger *slog.Logger) string {
	cleaned, stripped := text.SanitizeUntrusted(s)
	if stripped > 0 {
		logger.Warn(
			"invisible_unicode_stripped",
			slog.String("source", "telegram"),
			slog.Int("count", stripped),
		)
	}
	return cleaned
}
