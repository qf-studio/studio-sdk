package plane

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/qf-studio/studio-sdk/sdk/util/text"
)

// WebhookEventType represents the type of Plane webhook event.
type WebhookEventType string

const (
	EventIssue WebhookEventType = "issue"
)

// WebhookPayload represents a Plane.so webhook payload.
type WebhookPayload struct {
	Event       string          `json:"event"`
	Action      string          `json:"action"`
	WebhookID   string          `json:"webhook_id"`
	WorkspaceID string          `json:"workspace_id"`
	Data        json.RawMessage `json:"data"`
}

// WebhookWorkItemData represents the data field of a work item webhook event.
type WebhookWorkItemData struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	StateID    string   `json:"state"`
	LabelIDs   []string `json:"labels"`
	ProjectID  string   `json:"project"`
	SequenceID int      `json:"sequence_id"`
}

// WebhookHandler handles Plane.so webhooks.
type WebhookHandler struct {
	secret     string
	pilotLabel string
	projectIDs []string
	onWorkItem func(context.Context, *WebhookWorkItemData) error
	logger     *slog.Logger
}

// WebhookHandlerOption configures a WebhookHandler.
type WebhookHandlerOption func(*WebhookHandler)

// WithWebhookLogger sets the logger for the webhook handler.
func WithWebhookLogger(logger *slog.Logger) WebhookHandlerOption {
	return func(h *WebhookHandler) {
		h.logger = logger
	}
}

// NewWebhookHandler creates a new Plane webhook handler.
func NewWebhookHandler(secret, pilotLabel string, projectIDs []string, opts ...WebhookHandlerOption) *WebhookHandler {
	h := &WebhookHandler{
		secret:     secret,
		pilotLabel: pilotLabel,
		projectIDs: projectIDs,
		logger:     slog.Default(),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// OnWorkItem sets the callback for when a pilot-labeled work item event is received.
func (h *WebhookHandler) OnWorkItem(callback func(context.Context, *WebhookWorkItemData) error) {
	h.onWorkItem = callback
}

// VerifySignature verifies the Plane webhook HMAC-SHA256 signature.
// Returns true if signature is valid, or if no secret is configured (development mode).
func VerifySignature(secret string, payload []byte, signature string) bool {
	if secret == "" {
		return true
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// Handle processes a Plane webhook payload.
func (h *WebhookHandler) Handle(ctx context.Context, payload []byte, signature string) error {
	// Verify signature
	if !VerifySignature(h.secret, payload, signature) {
		return fmt.Errorf("invalid webhook signature")
	}

	var wp WebhookPayload
	if err := json.Unmarshal(payload, &wp); err != nil {
		return fmt.Errorf("failed to parse webhook payload: %w", err)
	}

	h.logger.Debug("Plane webhook", slog.String("event", wp.Event), slog.String("action", wp.Action))

	// Only process issue events with created/updated actions
	if wp.Event != string(EventIssue) {
		return nil
	}
	if wp.Action != "created" && wp.Action != "updated" {
		return nil
	}

	var data WebhookWorkItemData
	if err := json.Unmarshal(wp.Data, &data); err != nil {
		return fmt.Errorf("failed to parse work item data: %w", err)
	}

	// Check project filter
	if !h.isAllowedProject(data.ProjectID) {
		h.logger.Debug("Work item not in allowed project, skipping",
			slog.String("work_item_id", data.ID),
			slog.String("project_id", data.ProjectID))
		return nil
	}

	// Check pilot label
	if !h.hasPilotLabel(data.LabelIDs) {
		h.logger.Debug("Work item does not have pilot label, skipping",
			slog.String("work_item_id", data.ID))
		return nil
	}

	// Strip invisible Unicode from the untrusted Name field before the
	// log line and before handing data to the pilot callback.
	cleanName, stripped := text.SanitizeUntrusted(data.Name)
	if stripped > 0 {
		h.logger.Warn("invisible_unicode_stripped",
			slog.String("source", "plane-webhook"),
			slog.String("workitem", data.ID),
			slog.Int("name_stripped", stripped),
		)
	}
	data.Name = cleanName

	h.logger.Info("Processing pilot work item",
		slog.String("work_item_id", data.ID),
		slog.String("name", data.Name),
		slog.String("action", wp.Action))

	if h.onWorkItem != nil {
		return h.onWorkItem(ctx, &data)
	}

	return nil
}

// isAllowedProject checks if the work item belongs to an allowed project.
func (h *WebhookHandler) isAllowedProject(projectID string) bool {
	if len(h.projectIDs) == 0 {
		return true
	}
	for _, pid := range h.projectIDs {
		if pid == projectID {
			return true
		}
	}
	return false
}

// hasPilotLabel checks if any of the label UUIDs match the configured pilot label UUID.
func (h *WebhookHandler) hasPilotLabel(labelIDs []string) bool {
	if h.pilotLabel == "" {
		return false
	}
	for _, id := range labelIDs {
		if id == h.pilotLabel {
			return true
		}
	}
	return false
}
