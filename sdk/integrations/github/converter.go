package github

import (
	"log/slog"
	"strings"

	"github.com/qf-studio/studio-sdk/sdk/util/text"
)

// sanitizeIssueInPlace strips invisible Unicode format characters from the
// issue's untrusted text fields (Title, Body) and cleans the body of template
// boilerplate, writing the results back onto the issue before any downstream
// consumer (LLM, host handler) sees them. Emits a slog.Warn when any runes
// are stripped — an attack-in-progress signal.
//
// This runs in the live poll/webhook path (see poller.go, webhook.go), which is
// the only place a GitHub issue crosses into core.IssueEvent. Untrusted third-
// party text must never reach the host unsanitized.
func sanitizeIssueInPlace(logger *slog.Logger, issue *Issue) {
	if issue == nil {
		return
	}
	var titleStripped, bodyStripped int
	issue.Title, titleStripped = text.SanitizeUntrusted(issue.Title)
	issue.Body, bodyStripped = text.SanitizeUntrusted(extractDescription(issue.Body))

	if titleStripped+bodyStripped > 0 {
		logger.Warn(
			"invisible_unicode_stripped",
			slog.String("source", "github"),
			slog.Int("issue", issue.Number),
			slog.Int("title_stripped", titleStripped),
			slog.Int("body_stripped", bodyStripped),
		)
	}
}

// extractDescription extracts and cleans the issue body, removing common GitHub
// issue template sections that aren't useful as task context, then sanitizing
// the result.
func extractDescription(body string) string {
	if body == "" {
		return ""
	}

	// Remove common GitHub issue template sections that aren't useful for tasks
	lines := strings.Split(body, "\n")
	var filtered []string
	skipSection := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip template sections
		if strings.HasPrefix(trimmed, "### Checklist") ||
			strings.HasPrefix(trimmed, "### Environment") ||
			strings.HasPrefix(trimmed, "### Bug Report") {
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

// extractPriority determines priority from labels.
// Callers normalize to the shared vocabulary via core.NormalizePriority.
func extractPriority(labels []Label) Priority {
	for _, label := range labels {
		name := strings.ToLower(label.Name)

		// Common priority label patterns
		if strings.Contains(name, "urgent") || strings.Contains(name, "critical") || name == "p0" {
			return PriorityUrgent
		}
		if strings.Contains(name, "high") || name == "p1" {
			return PriorityHigh
		}
		if strings.Contains(name, "medium") || name == "p2" {
			return PriorityMedium
		}
		if strings.Contains(name, "low") || name == "p3" {
			return PriorityLow
		}
	}

	return PriorityNone
}

// extractLabelNames returns a list of label names excluding trigger/priority labels
func extractLabelNames(labels []Label) []string {
	var names []string
	for _, label := range labels {
		name := strings.ToLower(label.Name)
		// Skip pilot and priority labels
		if strings.HasPrefix(name, "pilot") ||
			strings.HasPrefix(name, "priority") ||
			strings.HasPrefix(name, "p0") || strings.HasPrefix(name, "p1") ||
			strings.HasPrefix(name, "p2") || strings.HasPrefix(name, "p3") {
			continue
		}
		names = append(names, label.Name)
	}
	return names
}
