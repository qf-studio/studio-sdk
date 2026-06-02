// Package asana provides an adapter for Asana task management.
package asana

import "time"

// Config holds Asana adapter configuration.
type Config struct {
	Enabled       bool   `yaml:"enabled"`
	AccessToken   string `yaml:"access_token"`
	WorkspaceID   string `yaml:"workspace_id"`
	WebhookSecret string `yaml:"webhook_secret"`
	TriggerLabel  string `yaml:"trigger_label"` // Tag name that triggers the agent (default: "pilot")

	Polling *PollingConfig `yaml:"polling,omitempty"`
}

// PollingConfig holds polling configuration for the Asana adapter.
type PollingConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval,omitempty"`
}

// DefaultConfig returns default Asana configuration.
func DefaultConfig() *Config {
	return &Config{
		Enabled:      false,
		TriggerLabel: "pilot",
	}
}

// Priority represents task priority levels.
type Priority int

const (
	PriorityNone   Priority = 0
	PriorityUrgent Priority = 1
	PriorityHigh   Priority = 2
	PriorityMedium Priority = 3
	PriorityLow    Priority = 4
)

// PriorityFromTags extracts priority from Asana tags.
func PriorityFromTags(tags []Tag) Priority {
	for _, tag := range tags {
		switch tag.Name {
		case "urgent", "Urgent", "URGENT", "critical", "Critical", "CRITICAL":
			return PriorityUrgent
		case "high", "High", "HIGH":
			return PriorityHigh
		case "medium", "Medium", "MEDIUM":
			return PriorityMedium
		case "low", "Low", "LOW":
			return PriorityLow
		}
	}
	return PriorityNone
}

// PriorityName returns the human-readable priority name.
func PriorityName(priority Priority) string {
	switch priority {
	case PriorityUrgent:
		return "Urgent"
	case PriorityHigh:
		return "High"
	case PriorityMedium:
		return "Medium"
	case PriorityLow:
		return "Low"
	default:
		return "No Priority"
	}
}

// Task represents an Asana task.
type Task struct {
	GID             string     `json:"gid"`
	Name            string     `json:"name"`
	Notes           string     `json:"notes"`
	HTMLNotes       string     `json:"html_notes,omitempty"`
	Completed       bool       `json:"completed"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	Assignee        *User      `json:"assignee,omitempty"`
	AssigneeSection *Section   `json:"assignee_section,omitempty"`
	Projects        []Project  `json:"projects,omitempty"`
	Tags            []Tag      `json:"tags,omitempty"`
	Workspace       *Workspace `json:"workspace,omitempty"`
	Parent          *Task      `json:"parent,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	ModifiedAt      time.Time  `json:"modified_at"`
	DueOn           string     `json:"due_on,omitempty"`
	DueAt           *time.Time `json:"due_at,omitempty"`
	StartOn         string     `json:"start_on,omitempty"`
	Permalink       string     `json:"permalink_url,omitempty"`
	ResourceType    string     `json:"resource_type"`
}

// User represents an Asana user.
type User struct {
	GID          string `json:"gid"`
	Name         string `json:"name"`
	Email        string `json:"email,omitempty"`
	ResourceType string `json:"resource_type"`
}

// Project represents an Asana project.
type Project struct {
	GID          string `json:"gid"`
	Name         string `json:"name"`
	ResourceType string `json:"resource_type"`
}

// Tag represents an Asana tag.
type Tag struct {
	GID          string `json:"gid"`
	Name         string `json:"name"`
	Color        string `json:"color,omitempty"`
	ResourceType string `json:"resource_type"`
}

// Section represents an Asana project section.
type Section struct {
	GID          string `json:"gid"`
	Name         string `json:"name"`
	ResourceType string `json:"resource_type"`
}

// Workspace represents an Asana workspace.
type Workspace struct {
	GID          string `json:"gid"`
	Name         string `json:"name"`
	ResourceType string `json:"resource_type"`
}

// Story represents an Asana story (comment or activity).
type Story struct {
	GID          string    `json:"gid"`
	Text         string    `json:"text"`
	HTMLText     string    `json:"html_text,omitempty"`
	Type         string    `json:"type"`
	CreatedAt    time.Time `json:"created_at"`
	CreatedBy    *User     `json:"created_by,omitempty"`
	ResourceType string    `json:"resource_type"`
}

// Attachment represents an Asana attachment.
type Attachment struct {
	GID          string    `json:"gid"`
	Name         string    `json:"name"`
	Host         string    `json:"host,omitempty"`
	ViewURL      string    `json:"view_url,omitempty"`
	DownloadURL  string    `json:"download_url,omitempty"`
	Permanent    bool      `json:"permanent"`
	CreatedAt    time.Time `json:"created_at"`
	ResourceType string    `json:"resource_type"`
}

// APIResponse wraps all Asana API responses.
type APIResponse[T any] struct {
	Data   T          `json:"data"`
	Errors []APIError `json:"errors,omitempty"`
}

// APIError represents an Asana API error.
type APIError struct {
	Message string `json:"message"`
	Help    string `json:"help,omitempty"`
}

// PagedResponse wraps paginated Asana API responses.
type PagedResponse[T any] struct {
	Data     []T       `json:"data"`
	NextPage *NextPage `json:"next_page,omitempty"`
}

// NextPage represents pagination info.
type NextPage struct {
	Offset string `json:"offset"`
	Path   string `json:"path"`
	URI    string `json:"uri"`
}

// WebhookEventType represents the type of webhook event.
type WebhookEventType string

const (
	EventTaskAdded     WebhookEventType = "added"
	EventTaskChanged   WebhookEventType = "changed"
	EventTaskRemoved   WebhookEventType = "removed"
	EventTaskDeleted   WebhookEventType = "deleted"
	EventTaskUndeleted WebhookEventType = "undeleted"
)

// WebhookPayload represents the webhook request body from Asana.
type WebhookPayload struct {
	Events []WebhookEvent `json:"events"`
}

// WebhookEvent represents a single event in the webhook payload.
type WebhookEvent struct {
	Action    string           `json:"action"`
	User      *User            `json:"user"`
	Resource  WebhookResource  `json:"resource"`
	Parent    *WebhookResource `json:"parent,omitempty"`
	Change    *WebhookChange   `json:"change,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
}

// WebhookResource represents the resource in a webhook event.
type WebhookResource struct {
	GID          string `json:"gid"`
	ResourceType string `json:"resource_type"`
	Name         string `json:"name,omitempty"`
}

// WebhookChange represents what changed in an event.
type WebhookChange struct {
	Field        string      `json:"field"`
	Action       string      `json:"action"`
	AddedValue   interface{} `json:"added_value,omitempty"`
	RemovedValue interface{} `json:"removed_value,omitempty"`
	NewValue     interface{} `json:"new_value,omitempty"`
}

// WebhookRegistration represents a webhook subscription.
type WebhookRegistration struct {
	GID           string     `json:"gid"`
	Resource      Resource   `json:"resource"`
	Target        string     `json:"target"`
	Active        bool       `json:"active"`
	CreatedAt     time.Time  `json:"created_at"`
	LastFailureAt *time.Time `json:"last_failure_at,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
}

// Resource represents a generic Asana resource reference.
type Resource struct {
	GID          string `json:"gid"`
	Name         string `json:"name,omitempty"`
	ResourceType string `json:"resource_type"`
}
