package linear

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
)

// Errors returned by VerifyLinearSignature. Callers can use errors.Is to
// distinguish between misconfiguration (no public key) and tampering
// (signature mismatch) so they can choose to log-and-allow vs. reject.
var (
	// ErrLinearNoPublicKey is returned when the public key is empty.
	// Treat as "verification disabled" — the caller decides whether to
	// reject or to log a warning and allow the request through.
	ErrLinearNoPublicKey = errors.New("linear: webhook public key not configured")
	// ErrLinearMissingSignature is returned when the request has no
	// linear-signature header. Always a hard reject.
	ErrLinearMissingSignature = errors.New("linear: missing webhook signature header")
	// ErrLinearInvalidSignatureEncoding is returned when the header value
	// is present but not valid hex. Hard reject.
	ErrLinearInvalidSignatureEncoding = errors.New("linear: invalid signature encoding")
	// ErrLinearSignatureMismatch is returned when the signature does not
	// match the body under the configured public key. Hard reject.
	ErrLinearSignatureMismatch = errors.New("linear: signature verification failed")
)

// VerifyLinearSignature verifies that the given signature is a valid Ed25519
// signature over body, produced by the private key corresponding to publicKey.
//
// Linear's webhook signing convention:
//   - Header name: "linear-signature"
//   - Header value: hex-encoded Ed25519 signature
//   - Signed data: the raw HTTP request body bytes (before JSON parsing)
//
// Returns nil on successful verification. On failure, returns a sentinel error
// (see ErrLinear* above) so callers can match with errors.Is.
func VerifyLinearSignature(publicKey ed25519.PublicKey, signatureHex string, body []byte) error {
	if len(publicKey) == 0 {
		return ErrLinearNoPublicKey
	}
	if signatureHex == "" {
		return ErrLinearMissingSignature
	}
	sig, err := hex.DecodeString(signatureHex)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrLinearInvalidSignatureEncoding, err)
	}
	if !ed25519.Verify(publicKey, body, sig) {
		return ErrLinearSignatureMismatch
	}
	return nil
}

// WebhookEventType represents the type of webhook event.
type WebhookEventType string

const (
	EventIssueCreated WebhookEventType = "Issue.create"
	EventIssueUpdated WebhookEventType = "Issue.update"
	EventIssueDeleted WebhookEventType = "Issue.delete"
	EventCommentAdded WebhookEventType = "Comment.create"
)

// WebhookPayload represents a Linear webhook payload.
type WebhookPayload struct {
	Action    string                 `json:"action"`
	Type      string                 `json:"type"`
	Data      map[string]interface{} `json:"data"`
	URL       string                 `json:"url"`
	CreatedAt string                 `json:"createdAt"`
	WebhookID string                 `json:"webhookId"`
	WebhookTS int64                  `json:"webhookTimestamp"`
}

// WebhookHandler handles Linear webhooks.
type WebhookHandler struct {
	client       *Client
	triggerLabel string
	projectIDs   []string
	onIssue      func(context.Context, *Issue) error
	logger       *slog.Logger
}

// WebhookOption is a functional option for WebhookHandler.
type WebhookOption func(*WebhookHandler)

// WithLogger sets the logger for the webhook handler.
func WithLogger(logger *slog.Logger) WebhookOption {
	return func(h *WebhookHandler) {
		h.logger = logger
	}
}

// NewWebhookHandler creates a new webhook handler.
func NewWebhookHandler(client *Client, triggerLabel string, projectIDs []string, opts ...WebhookOption) *WebhookHandler {
	h := &WebhookHandler{
		client:       client,
		triggerLabel: triggerLabel,
		projectIDs:   projectIDs,
		logger:       slog.Default(),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// OnIssue sets the callback for when a trigger-labeled issue is received.
func (h *WebhookHandler) OnIssue(callback func(context.Context, *Issue) error) {
	h.onIssue = callback
}

// Handle processes a webhook payload.
func (h *WebhookHandler) Handle(ctx context.Context, payload map[string]interface{}) error {
	action, _ := payload["action"].(string)
	eventType, _ := payload["type"].(string)

	h.logger.Debug("Linear webhook", slog.String("action", action), slog.String("type", eventType))

	if action != "create" || eventType != "Issue" {
		return nil
	}

	data, ok := payload["data"].(map[string]interface{})
	if !ok {
		return nil
	}

	if !h.hasTriggerLabel(data) {
		h.logger.Debug("Issue does not have trigger label, skipping")
		return nil
	}

	issueID, _ := data["id"].(string)
	issue, err := h.client.GetIssue(ctx, issueID)
	if err != nil {
		return err
	}

	if !h.isAllowedProject(issue) {
		projectName := "none"
		if issue.Project != nil {
			projectName = issue.Project.Name
		}
		h.logger.Debug("Issue not in allowed project, skipping",
			slog.String("identifier", issue.Identifier),
			slog.String("project", projectName))
		return nil
	}

	sanitizeIssueInPlace(issue, h.logger)

	h.logger.Info("Processing trigger issue", slog.String("identifier", issue.Identifier), slog.String("title", issue.Title))

	if h.onIssue != nil {
		return h.onIssue(ctx, issue)
	}

	return nil
}

func (h *WebhookHandler) isAllowedProject(issue *Issue) bool {
	if len(h.projectIDs) == 0 {
		return true
	}

	if issue.Project == nil {
		return false
	}

	for _, pid := range h.projectIDs {
		if issue.Project.ID == pid {
			return true
		}
	}

	return false
}

func (h *WebhookHandler) hasTriggerLabel(data map[string]interface{}) bool {
	labels, ok := data["labels"].([]interface{})
	if !ok {
		labelIDs, ok := data["labelIds"].([]interface{})
		if !ok {
			return false
		}
		return len(labelIDs) > 0
	}

	for _, label := range labels {
		labelMap, ok := label.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := labelMap["name"].(string)
		if name == h.triggerLabel {
			return true
		}
	}

	return false
}
