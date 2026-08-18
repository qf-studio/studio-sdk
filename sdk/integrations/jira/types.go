package jira

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Config holds Jira adapter configuration.
type Config struct {
	Enabled       bool   `yaml:"enabled"`
	Platform      string `yaml:"platform,omitempty"`       // "cloud" or "server"
	BaseURL       string `yaml:"base_url,omitempty"`       // e.g., "https://company.atlassian.net"
	Username      string `yaml:"username,omitempty"`       // Email for Cloud, username for Server
	APIToken      string `yaml:"api_token,omitempty"`      // API token (both Cloud and Server)
	WebhookSecret string `yaml:"webhook_secret,omitempty"` // For HMAC signature verification
	TriggerLabel  string `yaml:"trigger_label,omitempty"`  // Label that triggers processing, default "pilot"
	ProjectKey    string `yaml:"project_key,omitempty"`    // Optional project filter (e.g., "PROJ")
	Transitions   struct {
		InProgress string `yaml:"in_progress,omitempty"` // Jira transition ID
		Done       string `yaml:"done,omitempty"`        // Jira transition ID
	} `yaml:"transitions,omitempty"`

	// Polling configuration
	Polling *PollingConfig `yaml:"polling,omitempty"`
}

// PollingConfig holds polling configuration for the Jira adapter.
type PollingConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"`
}

// DefaultConfig returns default Jira configuration.
func DefaultConfig() *Config {
	return &Config{
		Enabled:      false,
		Platform:     "cloud",
		TriggerLabel: "pilot",
	}
}

// Platform types.
const (
	PlatformCloud  = "cloud"
	PlatformServer = "server"
)

// Issue states.
const (
	StatusToDo       = "To Do"
	StatusInProgress = "In Progress"
	StatusDone       = "Done"
)

// Priority maps numeric values to Jira priority names.
type Priority int

const (
	PriorityNone    Priority = 0
	PriorityHighest Priority = 1
	PriorityHigh    Priority = 2
	PriorityMedium  Priority = 3
	PriorityLow     Priority = 4
	PriorityLowest  Priority = 5
)

// PriorityFromJira converts a Jira priority name to Priority.
func PriorityFromJira(name string) Priority {
	switch name {
	case "Highest", "Blocker", "Critical":
		return PriorityHighest
	case "High", "Major":
		return PriorityHigh
	case "Medium":
		return PriorityMedium
	case "Low", "Minor":
		return PriorityLow
	case "Lowest", "Trivial":
		return PriorityLowest
	default:
		return PriorityNone
	}
}

// PriorityName returns the human-readable priority name.
func PriorityName(priority Priority) string {
	switch priority {
	case PriorityHighest:
		return "Highest"
	case PriorityHigh:
		return "High"
	case PriorityMedium:
		return "Medium"
	case PriorityLow:
		return "Low"
	case PriorityLowest:
		return "Lowest"
	default:
		return "No Priority"
	}
}

// Issue represents a Jira issue.
type Issue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Self   string `json:"self"`
	Fields Fields `json:"fields"`
}

// Fields represents Jira issue fields.
type Fields struct {
	Summary     string        `json:"summary"`
	Description ADFText       `json:"description"`
	IssueType   IssueType     `json:"issuetype"`
	Status      Status        `json:"status"`
	Priority    *JiraPriority `json:"priority,omitempty"`
	Labels      []string      `json:"labels"`
	Assignee    *User         `json:"assignee,omitempty"`
	Reporter    *User         `json:"reporter,omitempty"`
	Project     Project       `json:"project"`
	Created     string        `json:"created"`
	Updated     string        `json:"updated"`
}

// IssueType represents a Jira issue type.
type IssueType struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description ADFText `json:"description"`
}

// ADFText is a Jira text field that may arrive either as a plain string
// (Server/DC, and some Cloud v2-shaped payloads) or as an Atlassian Document
// Format (ADF) object (Cloud's POST /rest/api/3/search/jql, which returns
// rich-text fields like description as ADF rather than plain text). It always
// unmarshals to plain text, so callers can treat it as a normal string.
type ADFText string

// UnmarshalJSON implements json.Unmarshaler. It first tries to decode data as
// a plain JSON string; if that fails, it falls back to decoding an ADF
// document object and extracting the concatenated text content.
func (t *ADFText) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*t = ""
		return nil
	}

	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*t = ADFText(s)
		return nil
	}

	var adf map[string]interface{}
	if err := json.Unmarshal(data, &adf); err != nil {
		return fmt.Errorf("field is neither a plain string nor an ADF object: %w", err)
	}

	*t = ADFText(extractADFText(adf))
	return nil
}

// extractADFText extracts plain text from an Atlassian Document Format (ADF)
// node tree, as returned for rich-text fields (description, comment body,
// etc.) on Jira Cloud.
func extractADFText(adf map[string]interface{}) string {
	var sb strings.Builder
	extractADFTextRecursive(adf, &sb)
	return strings.TrimSpace(sb.String())
}

// extractADFTextRecursive recursively walks ADF content nodes, concatenating
// text nodes and inserting a newline after block-level nodes (paragraph,
// heading, listItem).
func extractADFTextRecursive(node map[string]interface{}, sb *strings.Builder) {
	if t, ok := node["text"].(string); ok {
		sb.WriteString(t)
	}

	if content, ok := node["content"].([]interface{}); ok {
		for _, item := range content {
			if itemMap, ok := item.(map[string]interface{}); ok {
				extractADFTextRecursive(itemMap, sb)
			}
		}
		if nodeType, ok := node["type"].(string); ok {
			if nodeType == "paragraph" || nodeType == "heading" || nodeType == "listItem" {
				sb.WriteString("\n")
			}
		}
	}
}

// Status represents a Jira status.
type Status struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	StatusCategory StatusCategory `json:"statusCategory"`
}

// StatusCategory represents a Jira status category.
type StatusCategory struct {
	ID   int    `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// JiraPriority represents a Jira priority.
type JiraPriority struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// User represents a Jira user.
type User struct {
	AccountID    string `json:"accountId,omitempty"` // Cloud
	Name         string `json:"name,omitempty"`      // Server
	Key          string `json:"key,omitempty"`       // Server
	EmailAddress string `json:"emailAddress,omitempty"`
	DisplayName  string `json:"displayName"`
}

// Project represents a Jira project.
type Project struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
	Self string `json:"self"`
}

// Transition represents a Jira workflow transition.
type Transition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   Status `json:"to"`
}

// TransitionsResponse represents the response from the transitions API.
type TransitionsResponse struct {
	Transitions []Transition `json:"transitions"`
}

// Comment represents a Jira comment.
type Comment struct {
	ID      string `json:"id"`
	Body    string `json:"body"`
	Author  User   `json:"author"`
	Created string `json:"created"`
	Updated string `json:"updated"`
}

// RemoteLink represents a Jira remote link (for PR linking).
type RemoteLink struct {
	GlobalID string           `json:"globalId,omitempty"`
	Object   RemoteLinkObject `json:"object"`
}

// RemoteLinkObject represents the object in a remote link.
type RemoteLinkObject struct {
	URL     string            `json:"url"`
	Title   string            `json:"title"`
	Summary string            `json:"summary,omitempty"`
	Icon    *RemoteLinkIcon   `json:"icon,omitempty"`
	Status  *RemoteLinkStatus `json:"status,omitempty"`
}

// RemoteLinkIcon represents an icon for a remote link.
type RemoteLinkIcon struct {
	URL16x16 string `json:"url16x16"`
	Title    string `json:"title"`
}

// RemoteLinkStatus represents the status of a remote link.
type RemoteLinkStatus struct {
	Resolved bool            `json:"resolved"`
	Icon     *RemoteLinkIcon `json:"icon,omitempty"`
}
