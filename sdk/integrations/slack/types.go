// Package slack provides a Slack REST client and event parsing layer
// for the Studio SDK. Delivery is via Socket Mode (WebSocket); this
// file covers only types — no WS client here.
package slack

// Config holds configuration for the Slack connector.
type Config struct {
	AppToken        string
	BotToken        string
	AllowedChannels []string
	AllowedUsers    []string
}

// Message represents a Slack chat.postMessage / chat.update payload.
type Message struct {
	Channel     string       `json:"channel"`
	Text        string       `json:"text,omitempty"`
	Blocks      []Block      `json:"blocks,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	ThreadTS    string       `json:"thread_ts,omitempty"`
}

// Block represents a Slack Block Kit block element.
type Block struct {
	Type     string       `json:"type"`
	Text     *TextObject  `json:"text,omitempty"`
	Elements []TextObject `json:"elements,omitempty"`
}

// TextObject represents a text composition object in Block Kit.
type TextObject struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emoji bool   `json:"emoji,omitempty"`
}

// ButtonElement represents an interactive button in a Block Kit actions block.
type ButtonElement struct {
	Type     string      `json:"type"`
	Text     *TextObject `json:"text"`
	ActionID string      `json:"action_id"`
	Value    string      `json:"value,omitempty"`
	Style    string      `json:"style,omitempty"` // "primary" or "danger"
}

// ActionsBlock represents a Block Kit actions block containing interactive elements.
type ActionsBlock struct {
	Type     string          `json:"type"`
	BlockID  string          `json:"block_id,omitempty"`
	Elements []ButtonElement `json:"elements"`
}

// Attachment represents a Slack legacy attachment.
type Attachment struct {
	Color  string `json:"color,omitempty"`
	Title  string `json:"title,omitempty"`
	Text   string `json:"text,omitempty"`
	Footer string `json:"footer,omitempty"`
}

// PostMessageResponse is the API response from chat.postMessage.
type PostMessageResponse struct {
	OK       bool   `json:"ok"`
	TS       string `json:"ts"`
	Channel  string `json:"channel"`
	Error    string `json:"error,omitempty"`
	ErrorMsg string `json:"error_msg,omitempty"`
}

// InteractiveMessage represents a message with Block Kit interactive elements.
type InteractiveMessage struct {
	Channel string        `json:"channel"`
	Text    string        `json:"text,omitempty"`
	Blocks  []interface{} `json:"blocks,omitempty"`
}

// InteractionPayload represents a Slack block_actions webhook payload.
type InteractionPayload struct {
	Type        string                 `json:"type"`
	Token       string                 `json:"token"`
	ActionTS    string                 `json:"action_ts"`
	Team        *InteractionTeam       `json:"team"`
	User        *InteractionUser       `json:"user"`
	Channel     *InteractionChannel    `json:"channel"`
	Message     *InteractionMessage    `json:"message"`
	ResponseURL string                 `json:"response_url"`
	TriggerID   string                 `json:"trigger_id"`
	Actions     []InteractionActionDef `json:"actions"`
}

// InteractionTeam identifies the Slack workspace in an interaction.
type InteractionTeam struct {
	ID     string `json:"id"`
	Domain string `json:"domain"`
}

// InteractionUser identifies the user who triggered an interaction.
type InteractionUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	TeamID   string `json:"team_id"`
}

// InteractionChannel identifies the channel where an interaction occurred.
type InteractionChannel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// InteractionMessage is the original message from an interaction payload.
type InteractionMessage struct {
	Type     string `json:"type"`
	TS       string `json:"ts"`
	Text     string `json:"text"`
	BotID    string `json:"bot_id,omitempty"`
	Subtype  string `json:"subtype,omitempty"`
	Username string `json:"username,omitempty"`
}

// InteractionActionDef is a single action entry in an interaction payload.
type InteractionActionDef struct {
	Type     string `json:"type"`
	ActionID string `json:"action_id"`
	BlockID  string `json:"block_id"`
	Value    string `json:"value"`
	ActionTS string `json:"action_ts"`
}

// InteractionAction is the normalised, handler-friendly representation of an action.
type InteractionAction struct {
	ActionID    string
	Value       string
	UserID      string
	Username    string
	ChannelID   string
	MessageTS   string
	ResponseURL string
}
