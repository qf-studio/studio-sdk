package asana

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
)

// WebhookHandler handles Asana webhooks.
type WebhookHandler struct {
	client        *Client
	logger        *slog.Logger
	webhookSecret string
	pilotTag      string
	onTask        func(context.Context, *Task) error
}

// NewWebhookHandler creates a new webhook handler.
func NewWebhookHandler(client *Client, webhookSecret, pilotTag string, opts ...Option) *WebhookHandler {
	cfg := &loggerConfig{logger: slog.Default()}
	for _, o := range opts {
		o(cfg)
	}
	return &WebhookHandler{
		client:        client,
		logger:        cfg.logger,
		webhookSecret: webhookSecret,
		pilotTag:      pilotTag,
	}
}

// OnTask sets the callback for when a pilot-tagged task is received.
func (h *WebhookHandler) OnTask(callback func(context.Context, *Task) error) {
	h.onTask = callback
}

// VerifySignature verifies the Asana webhook signature using HMAC-SHA256.
func (h *WebhookHandler) VerifySignature(payload []byte, signature string) bool {
	if h.webhookSecret == "" {
		return true
	}

	mac := hmac.New(sha256.New, []byte(h.webhookSecret))
	mac.Write(payload)
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expectedSig))
}

// HandleHandshake handles the initial webhook handshake from Asana.
// Returns the X-Hook-Secret value that should be echoed back.
func (h *WebhookHandler) HandleHandshake(hookSecret string) string {
	return hookSecret
}

// Handle processes a webhook payload.
func (h *WebhookHandler) Handle(ctx context.Context, payload *WebhookPayload) error {
	for _, event := range payload.Events {
		if err := h.handleEvent(ctx, event); err != nil {
			h.logger.Error("Failed to handle event",
				slog.String("action", event.Action),
				slog.String("resource_type", event.Resource.ResourceType),
				slog.Any("error", err))
		}
	}
	return nil
}

func (h *WebhookHandler) handleEvent(ctx context.Context, event WebhookEvent) error {
	h.logger.Debug("Processing Asana webhook event",
		slog.String("action", event.Action),
		slog.String("resource_type", event.Resource.ResourceType),
		slog.String("resource_gid", event.Resource.GID))

	if event.Resource.ResourceType != "task" {
		h.logger.Debug("Ignoring non-task event",
			slog.String("resource_type", event.Resource.ResourceType))
		return nil
	}

	switch WebhookEventType(event.Action) {
	case EventTaskAdded, EventTaskChanged:
		return h.handleTaskEvent(ctx, event)
	default:
		h.logger.Debug("Ignoring event action",
			slog.String("action", event.Action))
		return nil
	}
}

func (h *WebhookHandler) handleTaskEvent(ctx context.Context, event WebhookEvent) error {
	taskGID := event.Resource.GID

	if event.Change != nil && event.Change.Field == "tags" {
		if !h.wasTagAdded(event.Change) {
			h.logger.Debug("Tag change but pilot tag not added, skipping",
				slog.String("task_gid", taskGID))
			return nil
		}
	}

	task, err := h.client.GetTaskWithFields(ctx, taskGID, []string{
		"gid", "name", "notes", "html_notes", "completed", "completed_at",
		"assignee", "projects", "tags", "workspace", "parent",
		"created_at", "modified_at", "due_on", "due_at", "start_on", "permalink_url",
	})
	if err != nil {
		return fmt.Errorf("failed to fetch task details: %w", err)
	}

	if !h.hasPilotTag(task) {
		h.logger.Debug("Task does not have pilot tag, skipping",
			slog.String("gid", taskGID),
			slog.String("name", task.Name))
		return nil
	}

	if task.Completed {
		h.logger.Debug("Task is already completed, skipping",
			slog.String("gid", taskGID))
		return nil
	}

	return h.processTask(ctx, task)
}

func (h *WebhookHandler) processTask(ctx context.Context, task *Task) error {
	sanitizeTaskInPlace(task, h.logger)

	h.logger.Info("Processing pilot task",
		slog.String("gid", task.GID),
		slog.String("name", task.Name))

	if h.onTask != nil {
		return h.onTask(ctx, task)
	}

	return nil
}

func (h *WebhookHandler) hasPilotTag(task *Task) bool {
	for _, tag := range task.Tags {
		if strings.EqualFold(tag.Name, h.pilotTag) {
			return true
		}
	}
	return false
}

func (h *WebhookHandler) wasTagAdded(change *WebhookChange) bool {
	if change.Action != "added" {
		return false
	}

	if addedTag, ok := change.AddedValue.(map[string]interface{}); ok {
		if name, ok := addedTag["name"].(string); ok {
			return strings.EqualFold(name, h.pilotTag)
		}
		if gid, ok := addedTag["gid"].(string); ok {
			h.logger.Debug("Tag added by GID",
				slog.String("gid", gid))
			return true
		}
	}

	return false
}

// HandleRaw processes a raw webhook payload (for use with net/http handlers).
func (h *WebhookHandler) HandleRaw(ctx context.Context, events []map[string]interface{}) error {
	for _, eventData := range events {
		event := h.parseEvent(eventData)
		if err := h.handleEvent(ctx, event); err != nil {
			h.logger.Error("Failed to handle raw event",
				slog.Any("error", err))
		}
	}
	return nil
}

func (h *WebhookHandler) parseEvent(data map[string]interface{}) WebhookEvent {
	event := WebhookEvent{}

	if action, ok := data["action"].(string); ok {
		event.Action = action
	}

	if resource, ok := data["resource"].(map[string]interface{}); ok {
		if gid, ok := resource["gid"].(string); ok {
			event.Resource.GID = gid
		}
		if resourceType, ok := resource["resource_type"].(string); ok {
			event.Resource.ResourceType = resourceType
		}
		if name, ok := resource["name"].(string); ok {
			event.Resource.Name = name
		}
	}

	if parent, ok := data["parent"].(map[string]interface{}); ok {
		event.Parent = &WebhookResource{}
		if gid, ok := parent["gid"].(string); ok {
			event.Parent.GID = gid
		}
		if resourceType, ok := parent["resource_type"].(string); ok {
			event.Parent.ResourceType = resourceType
		}
	}

	if change, ok := data["change"].(map[string]interface{}); ok {
		event.Change = &WebhookChange{}
		if field, ok := change["field"].(string); ok {
			event.Change.Field = field
		}
		if action, ok := change["action"].(string); ok {
			event.Change.Action = action
		}
		if addedValue, ok := change["added_value"]; ok {
			event.Change.AddedValue = addedValue
		}
		if removedValue, ok := change["removed_value"]; ok {
			event.Change.RemovedValue = removedValue
		}
		if newValue, ok := change["new_value"]; ok {
			event.Change.NewValue = newValue
		}
	}

	return event
}
