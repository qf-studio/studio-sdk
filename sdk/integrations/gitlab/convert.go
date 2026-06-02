package gitlab

import (
	"log/slog"
	"strings"

	"github.com/qf-studio/studio-sdk/sdk/util/text"
)

// sanitizeIssueInPlace strips invisible Unicode format characters from the
// issue's untrusted text fields (Title, Description) and cleans the description
// of GitLab template boilerplate, writing the results back onto the issue
// before any downstream consumer (LLM, host handler) sees them. Emits a
// slog.Warn when any runes are stripped — an attack-in-progress signal.
//
// This runs in the live poll/webhook path (see poller.go, webhook.go), which is
// the only place a GitLab issue crosses into core.IssueEvent. Untrusted third-
// party text must never reach the host unsanitized.
func sanitizeIssueInPlace(logger *slog.Logger, issue *Issue) {
	if issue == nil {
		return
	}
	var titleStripped, bodyStripped int
	issue.Title, titleStripped = text.SanitizeUntrusted(issue.Title)
	issue.Description, bodyStripped = text.SanitizeUntrusted(extractDescription(issue.Description))

	if titleStripped+bodyStripped > 0 {
		logger.Warn(
			"invisible_unicode_stripped",
			slog.String("source", "gitlab"),
			slog.Int("issue", issue.IID),
			slog.Int("title_stripped", titleStripped),
			slog.Int("body_stripped", bodyStripped),
		)
	}
}

// extractDescription cleans an issue body for downstream use, removing common
// GitLab issue-template sections and quick-actions that aren't useful as task
// context, then sanitizing the result.
func extractDescription(body string) string {
	if body == "" {
		return ""
	}

	lines := strings.Split(body, "\n")
	var filtered []string
	skipSection := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip template sections (GitLab uses similar patterns)
		if strings.HasPrefix(trimmed, "### Checklist") ||
			strings.HasPrefix(trimmed, "### Environment") ||
			strings.HasPrefix(trimmed, "### Bug Report") ||
			strings.HasPrefix(trimmed, "/label ") ||
			strings.HasPrefix(trimmed, "/assign ") ||
			strings.HasPrefix(trimmed, "/milestone ") {
			skipSection = true
			continue
		}

		// Resume at next heading
		if skipSection && strings.HasPrefix(trimmed, "### ") {
			skipSection = false
		}

		if !skipSection {
			filtered = append(filtered, line)
		}
	}

	return text.SanitizeUntrustedString(strings.TrimSpace(strings.Join(filtered, "\n")))
}

// extractPriority determines the issue priority from its labels, returning the
// connector-local Priority rank. Callers normalize it to the shared vocabulary
// via core.NormalizePriority at the core.IssueEvent boundary.
func extractPriority(labels []string) Priority {
	for _, label := range labels {
		name := strings.ToLower(label)

		// GitLab scoped labels use :: as separator, e.g. priority::urgent.
		if strings.Contains(name, "urgent") || strings.Contains(name, "critical") || name == "p0" || name == "priority::urgent" {
			return PriorityUrgent
		}
		if strings.Contains(name, "high") || name == "p1" || name == "priority::high" {
			return PriorityHigh
		}
		if strings.Contains(name, "medium") || name == "p2" || name == "priority::medium" {
			return PriorityMedium
		}
		if strings.Contains(name, "low") || name == "p3" || name == "priority::low" {
			return PriorityLow
		}
	}

	return PriorityNone
}
