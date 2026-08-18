package jira

import (
	"log/slog"
	"regexp"
	"strings"

	"github.com/qf-studio/studio-sdk/sdk/util/text"
)

// sanitizeIssueInPlace runs text.SanitizeUntrusted over an issue's Summary and
// Description fields in place. Any stripped invisible Unicode format characters
// (ASCII-smuggling / prompt-injection vectors) are logged at Warn level.
func sanitizeIssueInPlace(issue *Issue, logger *slog.Logger) {
	var summaryStripped, descStripped int
	issue.Fields.Summary, summaryStripped = text.SanitizeUntrusted(issue.Fields.Summary)

	sanitizedDesc, n := text.SanitizeUntrusted(string(issue.Fields.Description))
	issue.Fields.Description, descStripped = ADFText(sanitizedDesc), n
	if summaryStripped+descStripped > 0 {
		logger.Warn(
			"invisible_unicode_stripped",
			slog.String("source", "jira"),
			slog.String("issue", issue.Key),
			slog.Int("summary_stripped", summaryStripped),
			slog.Int("description_stripped", descStripped),
		)
	}
}

// extractDescription cleans and extracts the issue description by stripping
// common Jira template boilerplate sections (checklist, environment).
func extractDescription(body string) string {
	if body == "" {
		return ""
	}

	lines := strings.Split(body, "\n")
	var filtered []string
	skipSection := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "h2. Checklist") ||
			strings.HasPrefix(trimmed, "h2. Environment") ||
			strings.HasPrefix(trimmed, "*Checklist*") ||
			strings.HasPrefix(trimmed, "*Environment*") {
			skipSection = true
			continue
		}

		if skipSection && (strings.HasPrefix(trimmed, "h2.") || strings.HasPrefix(trimmed, "h1.") ||
			(strings.HasPrefix(trimmed, "*") && strings.HasSuffix(trimmed, "*"))) {
			skipSection = false
		}

		if !skipSection {
			filtered = append(filtered, line)
		}
	}

	return text.SanitizeUntrustedString(strings.TrimSpace(strings.Join(filtered, "\n")))
}

// filterLabels returns labels excluding trigger and priority labels.
func filterLabels(labels []string) []string {
	var filtered []string
	for _, label := range labels {
		lower := strings.ToLower(label)
		if strings.HasPrefix(lower, "pilot") ||
			strings.HasPrefix(lower, "priority") ||
			lower == "p0" || lower == "p1" || lower == "p2" || lower == "p3" {
			continue
		}
		filtered = append(filtered, label)
	}
	return filtered
}

// ExtractAcceptanceCriteria extracts acceptance criteria from an issue body.
// It understands both Jira wiki markup and Markdown headings.
func ExtractAcceptanceCriteria(body string) []string {
	var criteria []string

	jiraPatterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)h[23]\.\s*acceptance criteria\s*\n([\s\S]*?)(?:\nh[123]\.|\z)`),
		regexp.MustCompile(`(?i)\*acceptance criteria\*\s*\n([\s\S]*?)(?:\n\*[^*]+\*|\z)`),
	}
	mdPatterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)###?\s*acceptance criteria\s*\n([\s\S]*?)(?:\n###?|\z)`),
		regexp.MustCompile(`(?i)###?\s*criteria\s*\n([\s\S]*?)(?:\n###?|\z)`),
	}

	for _, pattern := range append(jiraPatterns, mdPatterns...) {
		matches := pattern.FindStringSubmatch(body)
		if len(matches) > 1 {
			checkboxPattern := regexp.MustCompile(`[*-]\s*\[[ x]?\]\s*(.+)`)
			items := checkboxPattern.FindAllStringSubmatch(matches[1], -1)
			for _, item := range items {
				if len(item) > 1 {
					criteria = append(criteria, strings.TrimSpace(item[1]))
				}
			}

			if len(criteria) == 0 {
				listPattern := regexp.MustCompile(`[*-]\s+(.+)`)
				items = listPattern.FindAllStringSubmatch(matches[1], -1)
				for _, item := range items {
					if len(item) > 1 {
						criteria = append(criteria, strings.TrimSpace(item[1]))
					}
				}
			}
			break
		}
	}

	return criteria
}
