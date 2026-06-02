package azuredevops

import (
	"log/slog"
	"regexp"
	"strings"

	"github.com/qf-studio/studio-sdk/sdk/util/text"
)

// sanitizeWorkItemFields strips invisible Unicode format characters from the
// work item's untrusted text fields (title, description) and cleans the
// description of HTML and Azure DevOps template boilerplate, writing the
// results back into wi.Fields before any downstream consumer (LLM, host
// handler) sees them. Emits a slog.Warn when any runes are stripped — an
// attack-in-progress signal.
//
// Azure DevOps stores title/description in the Fields map (and serves
// descriptions as HTML), so sanitization rewrites System.Title and
// System.Description in place. This runs in the live poll/webhook path (see
// poller.go, webhook.go), the only place a work item crosses into
// core.IssueEvent. Untrusted third-party text must never reach the host
// unsanitized.
func sanitizeWorkItemFields(logger *slog.Logger, wi *WorkItem) {
	if wi == nil {
		return
	}
	title, titleStripped := text.SanitizeUntrusted(wi.GetTitle())
	description, bodyStripped := text.SanitizeUntrusted(extractDescription(wi.GetDescription()))

	if wi.Fields == nil {
		wi.Fields = map[string]interface{}{}
	}
	wi.Fields["System.Title"] = title
	wi.Fields["System.Description"] = description

	if titleStripped+bodyStripped > 0 {
		logger.Warn(
			"invisible_unicode_stripped",
			slog.String("source", "azuredevops"),
			slog.Int("workitem", wi.ID),
			slog.Int("title_stripped", titleStripped),
			slog.Int("body_stripped", bodyStripped),
		)
	}
}

// extractDescription cleans a work item description for downstream use. Azure
// DevOps stores descriptions as HTML, so this strips HTML, removes common
// template sections, and sanitizes the result.
func extractDescription(body string) string {
	if body == "" {
		return ""
	}

	// Remove HTML tags
	body = stripHTML(body)

	// Remove common Azure DevOps template sections
	lines := strings.Split(body, "\n")
	var filtered []string
	skipSection := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "### Checklist") ||
			strings.HasPrefix(trimmed, "### Environment") ||
			strings.HasPrefix(trimmed, "### Repro Steps") ||
			strings.HasPrefix(trimmed, "### Expected Behavior") ||
			strings.HasPrefix(trimmed, "### Actual Behavior") {
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

// stripHTML removes HTML tags and decodes common entities from a string.
func stripHTML(s string) string {
	// Remove script and style tags with content
	scriptRe := regexp.MustCompile(`(?i)<script[^>]*>[\s\S]*?</script>`)
	s = scriptRe.ReplaceAllString(s, "")
	styleRe := regexp.MustCompile(`(?i)<style[^>]*>[\s\S]*?</style>`)
	s = styleRe.ReplaceAllString(s, "")

	// Replace common HTML elements with appropriate text
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")
	s = strings.ReplaceAll(s, "</p>", "\n\n")
	s = strings.ReplaceAll(s, "</div>", "\n")
	s = strings.ReplaceAll(s, "</li>", "\n")
	s = strings.ReplaceAll(s, "<li>", "- ")

	// Remove all remaining HTML tags
	tagRe := regexp.MustCompile(`<[^>]*>`)
	s = tagRe.ReplaceAllString(s, "")

	// Decode common HTML entities
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")

	// Clean up multiple newlines
	multiNewlineRe := regexp.MustCompile(`\n{3,}`)
	s = multiNewlineRe.ReplaceAllString(s, "\n\n")

	return strings.TrimSpace(s)
}
