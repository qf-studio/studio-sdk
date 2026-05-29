package plane

import (
	"log/slog"

	"github.com/qf-studio/studio-sdk/sdk/util/text"
)

// sanitizeWorkItemInPlace strips invisible Unicode format characters
// from the work item's untrusted text fields (Name, Description)
// before it is handed to any downstream consumer. Emits a slog.Warn
// when any runes are stripped — attack-in-progress telemetry signal.
func sanitizeWorkItemInPlace(item *WorkItem, logger *slog.Logger) {
	if item == nil {
		return
	}
	var nameStripped, descStripped int
	item.Name, nameStripped = text.SanitizeUntrusted(item.Name)
	item.Description, descStripped = text.SanitizeUntrusted(item.Description)

	if nameStripped+descStripped > 0 {
		logger.Warn(
			"invisible_unicode_stripped",
			slog.String("source", "plane"),
			slog.String("workitem", item.ID),
			slog.Int("name_stripped", nameStripped),
			slog.Int("description_stripped", descStripped),
		)
	}
}
