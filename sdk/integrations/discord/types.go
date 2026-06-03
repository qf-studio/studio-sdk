// Package discord provides a Discord REST client and event parsing layer
// for the Studio SDK. Gateway WS transport is in the follow-up issue.
package discord

import (
	"encoding/json"
	"strings"
	"time"
)

// Config holds Discord connector configuration.
type Config struct {
	BotToken        string   `yaml:"bot_token"`
	BotID           string   `yaml:"bot_id"`           // Bot user ID for mention stripping
	AllowedGuilds   []string `yaml:"allowed_guilds"`   // Guild IDs allowed to send tasks
	AllowedChannels []string `yaml:"allowed_channels"` // Channel IDs allowed to send tasks
}

// Discord Gateway intents (https://discord.com/developers/docs/topics/gateway#gateway-intents)
const (
	IntentGuilds                      = 1 << 0
	IntentGuildMembers                = 1 << 1
	IntentGuildModeration             = 1 << 2
	IntentGuildEmojis                 = 1 << 3
	IntentGuildIntegrations           = 1 << 4
	IntentGuildWebhooks               = 1 << 5
	IntentGuildInvites                = 1 << 6
	IntentGuildVoiceStates            = 1 << 7
	IntentGuildPresences              = 1 << 8
	IntentGuildMessages               = 1 << 9
	IntentGuildMessageReactions       = 1 << 10
	IntentGuildMessageTyping          = 1 << 11
	IntentDirectMessages              = 1 << 12
	IntentDirectMessageReactions      = 1 << 13
	IntentDirectMessageTyping         = 1 << 14
	IntentMessageContent              = 1 << 15
	IntentGuildScheduledEvents        = 1 << 16
	IntentAutoModerationConfiguration = 1 << 20
	IntentAutoModerationExecution     = 1 << 21
)

// DefaultIntents for common use: guilds, guild messages, direct messages, message content.
const DefaultIntents = IntentGuilds | IntentGuildMessages | IntentDirectMessages | IntentMessageContent

// Discord API and Gateway constants.
const (
	DiscordAPIURL     = "https://discord.com/api/v10"
	DiscordGatewayURL = "wss://gateway.discord.gg"

	OpcodeDispatch  = 0
	OpcodeHeartbeat = 1
	OpcodeIdentify  = 2
	OpcodeResume    = 6
	OpcodeHello     = 10

	CloseCodeUnknownError         = 4000
	CloseCodeUnknownOpcode        = 4001
	CloseCodeDecodeError          = 4002
	CloseCodeNotAuthenticated     = 4003
	CloseCodeAuthenticationFailed = 4004
	CloseCodeAlreadyAuthenticated = 4005
	CloseCodeInvalidSeq           = 4007
	CloseCodeRateLimited          = 4008
	CloseCodeSessionTimeout       = 4009
	CloseCodeInvalidToken         = 4014
)

// Interaction response types.
const (
	InteractionResponseChannelMessage         = 4
	InteractionResponseDeferredChannelMessage = 5
	InteractionResponseDeferredUpdateMessage  = 6
	InteractionResponseUpdateMessage          = 7
)

// MaxMessageLength is the maximum Discord message length.
const MaxMessageLength = 2000

// GatewayEvent represents a Discord Gateway event.
// D is kept as RawMessage so callers can decode into the concrete type they need.
type GatewayEvent struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  *int            `json:"s"`
	T  *string         `json:"t"`
}

// ParseMessageCreate decodes the D field into a MessageCreate.
func (e *GatewayEvent) ParseMessageCreate() (*MessageCreate, error) {
	var msg MessageCreate
	if err := json.Unmarshal(e.D, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// ParseInteractionCreate decodes the D field into an InteractionCreate.
func (e *GatewayEvent) ParseInteractionCreate() (*InteractionCreate, error) {
	var ic InteractionCreate
	if err := json.Unmarshal(e.D, &ic); err != nil {
		return nil, err
	}
	return &ic, nil
}

// Heartbeat is sent by the client to maintain the connection.
type Heartbeat struct {
	Op int  `json:"op"`
	D  *int `json:"d"`
}

// Identify is sent by the client on connection.
type Identify struct {
	Op int          `json:"op"`
	D  IdentifyData `json:"d"`
}

// IdentifyData contains the identify payload.
type IdentifyData struct {
	Token      string            `json:"token"`
	Intents    int               `json:"intents"`
	Properties map[string]string `json:"properties"`
}

// Resume is sent by the client to resume a session.
type Resume struct {
	Op int        `json:"op"`
	D  ResumeData `json:"d"`
}

// ResumeData contains the resume payload.
type ResumeData struct {
	Token     string `json:"token"`
	SessionID string `json:"session_id"`
	Seq       int    `json:"seq"`
}

// Hello is sent by the server on connect.
type Hello struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

// MessageCreate event data.
type MessageCreate struct {
	ID        string    `json:"id"`
	ChannelID string    `json:"channel_id"`
	GuildID   string    `json:"guild_id,omitempty"`
	Author    User      `json:"author"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// User represents a Discord user.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Bot      bool   `json:"bot,omitempty"`
	Email    string `json:"email,omitempty"`
}

// InteractionCreate event data.
type InteractionCreate struct {
	ID        string          `json:"id"`
	Token     string          `json:"token"`
	Type      int             `json:"type"` // 1=PING, 2=APPLICATION_COMMAND, 3=MESSAGE_COMPONENT
	GuildID   string          `json:"guild_id,omitempty"`
	ChannelID string          `json:"channel_id,omitempty"`
	Member    *Member         `json:"member,omitempty"`
	User      *User           `json:"user,omitempty"`
	Data      InteractionData `json:"data"`
	Message   *Message        `json:"message,omitempty"`
}

// Member represents a guild member.
type Member struct {
	User User   `json:"user"`
	Nick string `json:"nick,omitempty"`
}

// InteractionData contains interaction payload data.
type InteractionData struct {
	CustomID string `json:"custom_id,omitempty"`
}

// Message represents a Discord message (REST).
type Message struct {
	ID         string      `json:"id,omitempty"`
	ChannelID  string      `json:"channel_id,omitempty"`
	Content    string      `json:"content,omitempty"`
	Embeds     []Embed     `json:"embeds,omitempty"`
	Components []Component `json:"components,omitempty"`
}

// Embed represents a Discord embed.
type Embed struct {
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description,omitempty"`
	Color       int          `json:"color,omitempty"`
	Footer      *EmbedFooter `json:"footer,omitempty"`
}

// EmbedFooter represents an embed footer.
type EmbedFooter struct {
	Text string `json:"text,omitempty"`
}

// Component represents an interactive component (action row with buttons).
type Component struct {
	Type       int      `json:"type"` // 1=ACTION_ROW
	Components []Button `json:"components,omitempty"`
}

// Button represents a button in a component.
type Button struct {
	Type     int    `json:"type"`  // 2=BUTTON
	Style    int    `json:"style"` // 1=PRIMARY, 4=DANGER
	Label    string `json:"label"`
	CustomID string `json:"custom_id"`
}

// InteractionResponse is sent to acknowledge an interaction.
type InteractionResponse struct {
	Type int             `json:"type"`
	Data InteractionData `json:"data,omitempty"`
}

// StripBotMention removes a leading <@botID> or <@!botID> mention from content.
// When botID is empty, strips any leading <@...> mention (fallback for unknown bot ID).
func StripBotMention(content, botID string) string {
	if botID != "" {
		prefix1 := "<@" + botID + ">"
		prefix2 := "<@!" + botID + ">"
		content = strings.TrimPrefix(content, prefix1)
		content = strings.TrimPrefix(content, prefix2)
		return strings.TrimSpace(content)
	}
	// Fallback: strip any leading <@...> or <@!...>
	if strings.HasPrefix(content, "<@") {
		if idx := strings.Index(content, ">"); idx != -1 {
			content = strings.TrimSpace(content[idx+1:])
		}
	}
	return content
}
