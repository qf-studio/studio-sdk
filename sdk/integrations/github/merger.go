package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// MergeWaitResult represents the outcome of waiting for a PR to merge.
type MergeWaitResult struct {
	Merged      bool
	Closed      bool
	Conflicting bool
	TimedOut    bool
	PRNumber    int
	PRURL       string
	Message     string
}

// MergeWaiterConfig holds configuration for the merge waiter.
type MergeWaiterConfig struct {
	PollInterval time.Duration
	Timeout      time.Duration
}

// DefaultMergeWaiterConfig returns sensible defaults.
func DefaultMergeWaiterConfig() *MergeWaiterConfig {
	return &MergeWaiterConfig{
		PollInterval: 30 * time.Second,
		Timeout:      1 * time.Hour,
	}
}

// MergeWaiter waits for a PR to be merged.
type MergeWaiter struct {
	client *Client
	owner  string
	repo   string
	config *MergeWaiterConfig
	logger *slog.Logger
}

// NewMergeWaiter creates a new merge waiter.
func NewMergeWaiter(client *Client, owner, repo string, config *MergeWaiterConfig) *MergeWaiter {
	if config == nil {
		config = DefaultMergeWaiterConfig()
	}
	return &MergeWaiter{
		client: client,
		owner:  owner,
		repo:   repo,
		config: config,
		logger: slog.Default(),
	}
}

// Common errors.
var (
	ErrPRClosed     = errors.New("PR was closed without merging")
	ErrPRConflict   = errors.New("PR has merge conflicts")
	ErrMergeTimeout = errors.New("timed out waiting for PR merge")
)

// WaitForMerge polls the PR status until it's merged, closed, or times out.
func (m *MergeWaiter) WaitForMerge(ctx context.Context, prNumber int) (*MergeWaitResult, error) {
	m.logger.Info("Waiting for PR merge",
		slog.Int("pr_number", prNumber),
		slog.String("repo", m.owner+"/"+m.repo),
		slog.Duration("timeout", m.config.Timeout),
		slog.Duration("poll_interval", m.config.PollInterval),
	)

	deadline := time.Now().Add(m.config.Timeout)
	ticker := time.NewTicker(m.config.PollInterval)
	defer ticker.Stop()

	for {
		result, err := m.checkPRStatus(ctx, prNumber)
		if err != nil {
			return nil, fmt.Errorf("failed to check PR status: %w", err)
		}

		if result.Merged || result.Closed || result.Conflicting {
			return result, nil
		}

		select {
		case <-ctx.Done():
			return &MergeWaitResult{
				PRNumber: prNumber,
				Message:  "Context cancelled while waiting for merge",
			}, ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			m.logger.Warn("PR merge timed out",
				slog.Int("pr_number", prNumber),
				slog.Duration("timeout", m.config.Timeout),
			)
			return &MergeWaitResult{
				PRNumber: prNumber,
				TimedOut: true,
				Message:  fmt.Sprintf("Timed out waiting for PR #%d to merge after %s", prNumber, m.config.Timeout),
			}, ErrMergeTimeout
		}

		select {
		case <-ctx.Done():
			return &MergeWaitResult{
				PRNumber: prNumber,
				Message:  "Context cancelled while waiting for merge",
			}, ctx.Err()
		case <-ticker.C:
			m.logger.Debug("Polling PR status",
				slog.Int("pr_number", prNumber),
				slog.Duration("remaining", time.Until(deadline)),
			)
		}
	}
}

func (m *MergeWaiter) checkPRStatus(ctx context.Context, prNumber int) (*MergeWaitResult, error) {
	pr, err := m.client.GetPullRequest(ctx, m.owner, m.repo, prNumber)
	if err != nil {
		return nil, err
	}

	result := &MergeWaitResult{
		PRNumber: prNumber,
		PRURL:    pr.HTMLURL,
	}

	if pr.Merged {
		m.logger.Info("PR merged successfully", slog.Int("pr_number", prNumber))
		result.Merged = true
		result.Message = fmt.Sprintf("PR #%d was merged", prNumber)
		return result, nil
	}

	if pr.State == "closed" && !pr.Merged {
		m.logger.Warn("PR closed without merging", slog.Int("pr_number", prNumber))
		result.Closed = true
		result.Message = fmt.Sprintf("PR #%d was closed without merging", prNumber)
		return result, nil
	}

	if pr.Mergeable != nil && !*pr.Mergeable {
		m.logger.Warn("PR has merge conflicts", slog.Int("pr_number", prNumber))
		result.Conflicting = true
		result.Message = fmt.Sprintf("PR #%d has merge conflicts", prNumber)
		return result, nil
	}

	result.Message = fmt.Sprintf("PR #%d is open, waiting for merge...", prNumber)
	return result, nil
}

// WaitWithCallback is like WaitForMerge but calls the callback on each poll.
func (m *MergeWaiter) WaitWithCallback(ctx context.Context, prNumber int, onPoll func(result *MergeWaitResult)) (*MergeWaitResult, error) {
	m.logger.Info("Waiting for PR merge with callback", slog.Int("pr_number", prNumber))

	deadline := time.Now().Add(m.config.Timeout)
	ticker := time.NewTicker(m.config.PollInterval)
	defer ticker.Stop()

	for {
		result, err := m.checkPRStatus(ctx, prNumber)
		if err != nil {
			return nil, fmt.Errorf("failed to check PR status: %w", err)
		}

		if onPoll != nil {
			onPoll(result)
		}

		if result.Merged || result.Closed || result.Conflicting {
			return result, nil
		}

		select {
		case <-ctx.Done():
			return &MergeWaitResult{
				PRNumber: prNumber,
				Message:  "Context cancelled",
			}, ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			return &MergeWaitResult{
				PRNumber: prNumber,
				TimedOut: true,
				Message:  fmt.Sprintf("Timed out after %s", m.config.Timeout),
			}, ErrMergeTimeout
		}

		select {
		case <-ctx.Done():
			return &MergeWaitResult{
				PRNumber: prNumber,
				Message:  "Context cancelled",
			}, ctx.Err()
		case <-ticker.C:
		}
	}
}
