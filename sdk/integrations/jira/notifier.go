package jira

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// Notifier handles status updates to Jira issues.
type Notifier struct {
	client      *Client
	logger      *slog.Logger
	transitions struct {
		inProgress string
		done       string
	}
}

// Jira's built-in status category keys. Unlike status names (e.g. "In
// Progress" vs. the Russian "К выполнению"), these keys are stable across
// every workflow and locale, so they are safe to match against when no
// explicit transition ID has been configured.
const (
	statusCategoryIndeterminate = "indeterminate" // "in progress"-style categories
	statusCategoryDone          = "done"
)

// NewNotifier creates a new Jira notifier.
func NewNotifier(client *Client, inProgressTransition, doneTransition string, opts ...Option) *Notifier {
	cfg := &loggerConfig{logger: slog.Default()}
	for _, o := range opts {
		o(cfg)
	}
	n := &Notifier{
		client: client,
		logger: cfg.logger,
	}
	n.transitions.inProgress = inProgressTransition
	n.transitions.done = doneTransition
	return n
}

// NotifyTaskStarted posts a comment and transitions to In Progress.
func (n *Notifier) NotifyTaskStarted(ctx context.Context, issueKey, taskID string) error {
	if n.transitions.inProgress != "" {
		if err := n.client.TransitionIssue(ctx, issueKey, n.transitions.inProgress); err != nil {
			n.logger.Warn("failed to transition issue to In Progress", slog.String("issue", issueKey), slog.Any("err", err))
		}
	} else {
		if err := n.transitionToCategory(ctx, issueKey, statusCategoryIndeterminate); err != nil {
			n.logger.Warn("failed to transition issue to In Progress", slog.String("issue", issueKey), slog.Any("err", err))
		}
	}

	comment := fmt.Sprintf("🤖 *Pilot started working on this issue*\n\nTask ID: %s\n\nI'll post updates as I make progress.", taskID)
	if _, err := n.client.AddComment(ctx, issueKey, comment); err != nil {
		return fmt.Errorf("failed to add start comment: %w", err)
	}

	return nil
}

// NotifyProgress posts a progress update comment.
func (n *Notifier) NotifyProgress(ctx context.Context, issueKey, phase, details string) error {
	var emoji string
	switch strings.ToLower(phase) {
	case "exploring", "research":
		emoji = "🔍"
	case "implementing", "impl":
		emoji = "🔨"
	case "testing", "verify":
		emoji = "🧪"
	case "committing":
		emoji = "📝"
	default:
		emoji = "⏳"
	}

	comment := fmt.Sprintf("%s *Phase: %s*\n\n%s", emoji, phase, details)
	if _, err := n.client.AddComment(ctx, issueKey, comment); err != nil {
		return fmt.Errorf("failed to add progress comment: %w", err)
	}

	return nil
}

// NotifyTaskCompleted posts completion comment and transitions to Done.
//
// The comment and the transition are attempted independently: a comment
// failure (e.g. a malformed ADF response) must not skip the Done transition.
// Both failures are logged individually; the comment error, if any, is still
// returned to preserve the existing caller contract.
func (n *Notifier) NotifyTaskCompleted(ctx context.Context, issueKey, prURL, summary string) error {
	var comment strings.Builder
	comment.WriteString("✅ *Pilot completed this task!*\n\n")

	if prURL != "" {
		comment.WriteString(fmt.Sprintf("*Pull Request*: %s\n\n", prURL))
	}

	if summary != "" {
		comment.WriteString("*Summary*:\n")
		comment.WriteString(summary)
		comment.WriteString("\n\n")
	}

	comment.WriteString("_This issue will be closed when the PR is merged._")

	_, commentErr := n.client.AddComment(ctx, issueKey, comment.String())
	if commentErr != nil {
		n.logger.Warn("failed to add completion comment", slog.String("issue", issueKey), slog.Any("err", commentErr))
	}

	var transitionErr error
	if n.transitions.done != "" {
		transitionErr = n.client.TransitionIssue(ctx, issueKey, n.transitions.done)
	} else {
		transitionErr = n.transitionToCategory(ctx, issueKey, statusCategoryDone)
	}
	if transitionErr != nil {
		n.logger.Warn("failed to transition issue to Done", slog.String("issue", issueKey), slog.Any("err", transitionErr))
	}

	if commentErr != nil {
		return fmt.Errorf("failed to add completion comment: %w", commentErr)
	}

	return nil
}

// NotifyTaskFailed posts a failure comment.
func (n *Notifier) NotifyTaskFailed(ctx context.Context, issueKey, reason string) error {
	comment := fmt.Sprintf("❌ *Pilot could not complete this task*\n\n*Reason*: %s\n\n_Please review the issue and consider manual intervention or reopening with more details._", reason)
	if _, err := n.client.AddComment(ctx, issueKey, comment); err != nil {
		return fmt.Errorf("failed to add failure comment: %w", err)
	}

	return nil
}

// transitionToCategory resolves and performs a transition to the first
// available workflow transition whose target status belongs to
// categoryKey (e.g. "indeterminate" for in-progress-style statuses, "done"
// for done-style statuses). Status category keys are assigned by Jira and
// stay stable across every workflow and locale, unlike status names — see
// the package-level constants above. If multiple transitions target the
// category, the first one returned by the API is used and the ambiguity is
// logged. Returns an error if no transition targets the category; callers
// treat that as non-fatal (WARN, no abort), matching the existing
// configured-ID fallback behavior.
func (n *Notifier) transitionToCategory(ctx context.Context, issueKey, categoryKey string) error {
	transitions, err := n.client.GetTransitions(ctx, issueKey)
	if err != nil {
		return fmt.Errorf("failed to get transitions: %w", err)
	}

	var candidates []Transition
	for _, t := range transitions {
		if strings.EqualFold(t.To.StatusCategory.Key, categoryKey) {
			candidates = append(candidates, t)
		}
	}

	if len(candidates) == 0 {
		return fmt.Errorf("no transition found targeting status category: %s", categoryKey)
	}

	chosen := candidates[0]
	if len(candidates) > 1 {
		names := make([]string, len(candidates))
		for i, c := range candidates {
			names[i] = c.To.Name
		}
		n.logger.Info("multiple transitions match status category, picking first",
			slog.String("issue", issueKey),
			slog.String("category", categoryKey),
			slog.String("chosen", chosen.To.Name),
			slog.Any("candidates", names),
		)
	}

	return n.client.TransitionIssue(ctx, issueKey, chosen.ID)
}

// LinkPR adds a PR link to the issue (as a web link) and posts a comment.
func (n *Notifier) LinkPR(ctx context.Context, issueKey string, prNumber int, prURL string) error {
	prTitle := fmt.Sprintf("PR #%d", prNumber)
	if err := n.client.AddPRLink(ctx, issueKey, prURL, prTitle); err != nil {
		return fmt.Errorf("failed to add PR link: %w", err)
	}

	comment := fmt.Sprintf("🔗 *Pull Request Created*: [PR #%d|%s]\n\n_This PR implements the changes for this issue._", prNumber, prURL)
	if _, err := n.client.AddComment(ctx, issueKey, comment); err != nil {
		return fmt.Errorf("failed to add PR link comment: %w", err)
	}

	return nil
}
