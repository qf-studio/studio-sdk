package azuredevops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// MergeWaitResult represents the outcome of waiting for a PR to merge.
type MergeWaitResult struct {
	Merged       bool
	Abandoned    bool
	HasConflicts bool
	TimedOut     bool
	PRNumber     int
	PRURL        string
	Message      string
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
	config *MergeWaiterConfig
	logger *slog.Logger
}

// NewMergeWaiter creates a new merge waiter.
func NewMergeWaiter(client *Client, config *MergeWaiterConfig) *MergeWaiter {
	if config == nil {
		config = DefaultMergeWaiterConfig()
	}
	return &MergeWaiter{
		client: client,
		config: config,
		logger: slog.Default(),
	}
}

// Common errors.
var (
	ErrPRAbandoned  = errors.New("PR was abandoned without merging")
	ErrPRConflict   = errors.New("PR has merge conflicts")
	ErrMergeTimeout = errors.New("timed out waiting for PR merge")
)

// WaitForMerge polls the PR status until it's merged, abandoned, or times out.
func (m *MergeWaiter) WaitForMerge(ctx context.Context, prID int) (*MergeWaitResult, error) {
	m.logger.Info("Waiting for PR merge",
		slog.Int("pr_id", prID),
		slog.Duration("timeout", m.config.Timeout),
		slog.Duration("poll_interval", m.config.PollInterval),
	)

	deadline := time.Now().Add(m.config.Timeout)
	ticker := time.NewTicker(m.config.PollInterval)
	defer ticker.Stop()

	for {
		result, err := m.checkPRStatus(ctx, prID)
		if err != nil {
			return nil, fmt.Errorf("failed to check PR status: %w", err)
		}

		if result.Merged || result.Abandoned || result.HasConflicts {
			return result, nil
		}

		select {
		case <-ctx.Done():
			return &MergeWaitResult{
				PRNumber: prID,
				Message:  "Context cancelled while waiting for merge",
			}, ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			m.logger.Warn("PR merge timed out",
				slog.Int("pr_id", prID),
				slog.Duration("timeout", m.config.Timeout),
			)
			return &MergeWaitResult{
				PRNumber: prID,
				TimedOut: true,
				Message:  fmt.Sprintf("Timed out waiting for PR #%d to merge after %s", prID, m.config.Timeout),
			}, ErrMergeTimeout
		}

		select {
		case <-ctx.Done():
			return &MergeWaitResult{
				PRNumber: prID,
				Message:  "Context cancelled while waiting for merge",
			}, ctx.Err()
		case <-ticker.C:
			m.logger.Debug("Polling PR status",
				slog.Int("pr_id", prID),
				slog.Duration("remaining", time.Until(deadline)),
			)
		}
	}
}

// checkPRStatus fetches and interprets the current PR status.
func (m *MergeWaiter) checkPRStatus(ctx context.Context, prID int) (*MergeWaitResult, error) {
	pr, err := m.client.GetPullRequest(ctx, prID)
	if err != nil {
		return nil, err
	}

	result := &MergeWaitResult{
		PRNumber: prID,
		PRURL:    m.client.GetPullRequestWebURL(prID),
	}

	if pr.Status == PRStateCompleted {
		m.logger.Info("PR merged successfully", slog.Int("pr_id", prID))
		result.Merged = true
		result.Message = fmt.Sprintf("PR #%d was merged", prID)
		return result, nil
	}

	if pr.Status == PRStateAbandoned {
		m.logger.Warn("PR abandoned without merging", slog.Int("pr_id", prID))
		result.Abandoned = true
		result.Message = fmt.Sprintf("PR #%d was abandoned without merging", prID)
		return result, nil
	}

	if pr.MergeStatus == MergeStatusConflicts {
		m.logger.Warn("PR has merge conflicts", slog.Int("pr_id", prID))
		result.HasConflicts = true
		result.Message = fmt.Sprintf("PR #%d has merge conflicts", prID)
		return result, nil
	}

	switch pr.MergeStatus {
	case MergeStatusQueued:
		result.Message = fmt.Sprintf("PR #%d merge is queued", prID)
	case MergeStatusFailure:
		result.Message = fmt.Sprintf("PR #%d merge failed (checks not passing)", prID)
	default:
		result.Message = fmt.Sprintf("PR #%d is active, waiting for merge...", prID)
	}

	return result, nil
}

// WaitWithCallback is like WaitForMerge but calls the callback on each poll.
func (m *MergeWaiter) WaitWithCallback(ctx context.Context, prID int, onPoll func(result *MergeWaitResult)) (*MergeWaitResult, error) {
	m.logger.Info("Waiting for PR merge with callback", slog.Int("pr_id", prID))

	deadline := time.Now().Add(m.config.Timeout)
	ticker := time.NewTicker(m.config.PollInterval)
	defer ticker.Stop()

	for {
		result, err := m.checkPRStatus(ctx, prID)
		if err != nil {
			return nil, fmt.Errorf("failed to check PR status: %w", err)
		}

		if onPoll != nil {
			onPoll(result)
		}

		if result.Merged || result.Abandoned || result.HasConflicts {
			return result, nil
		}

		select {
		case <-ctx.Done():
			return &MergeWaitResult{
				PRNumber: prID,
				Message:  "Context cancelled",
			}, ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			return &MergeWaitResult{
				PRNumber: prID,
				TimedOut: true,
				Message:  fmt.Sprintf("Timed out after %s", m.config.Timeout),
			}, ErrMergeTimeout
		}

		select {
		case <-ctx.Done():
			return &MergeWaitResult{
				PRNumber: prID,
				Message:  "Context cancelled",
			}, ctx.Err()
		case <-ticker.C:
		}
	}
}
