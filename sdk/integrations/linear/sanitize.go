package linear

import (
	"log/slog"

	"github.com/qf-studio/studio-sdk/sdk/util/text"
)

// sanitizeIssueInPlace strips invisible Unicode format characters from
// the issue's untrusted text fields (Title, Description) before the issue
// is handed to any downstream consumer. Emits a slog.Warn when any runes
// are stripped — this is the attack-in-progress signal.
// log may be nil; logging is skipped in that case.
func sanitizeIssueInPlace(issue *Issue, log *slog.Logger) {
	if issue == nil {
		return
	}
	var titleStripped, descStripped int
	issue.Title, titleStripped = text.SanitizeUntrusted(issue.Title)
	issue.Description, descStripped = text.SanitizeUntrusted(issue.Description)

	if log != nil && titleStripped+descStripped > 0 {
		log.Warn(
			"invisible_unicode_stripped",
			slog.String("source", "linear"),
			slog.String("issue", issue.Identifier),
			slog.Int("title_stripped", titleStripped),
			slog.Int("description_stripped", descStripped),
		)
	}
}
