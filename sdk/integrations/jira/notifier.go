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
		if err := n.client.TransitionIssueTo(ctx, issueKey, "In Progress"); err != nil {
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

	if _, err := n.client.AddComment(ctx, issueKey, comment.String()); err != nil {
		return fmt.Errorf("failed to add completion comment: %w", err)
	}

	if n.transitions.done != "" {
		if err := n.client.TransitionIssue(ctx, issueKey, n.transitions.done); err != nil {
			n.logger.Warn("failed to transition issue to Done", slog.String("issue", issueKey), slog.Any("err", err))
		}
	} else {
		if err := n.client.TransitionIssueTo(ctx, issueKey, "Done"); err != nil {
			n.logger.Warn("failed to transition issue to Done", slog.String("issue", issueKey), slog.Any("err", err))
		}
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
