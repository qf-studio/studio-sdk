package core

import "context"

// MessageEvent is the normalized inbound event emitted by all chat adapters.
// Action discriminates the three event kinds ("message", "command", "callback"),
// mirroring the IssueEvent.Action pattern used by issue-tracker adapters.
//
// Text is guaranteed to be sanitized by the adapter live path before emission —
// callers may pass it downstream without an additional sanitize step.
type MessageEvent struct {
	// Action is one of "message", "command", or "callback".
	Action string
	// MessageID is the adapter-native message identifier (for replies and edits).
	MessageID string
	// ChannelID is the channel, chat, or room this event arrived in.
	ChannelID string
	// ThreadID is the thread root message ID; empty means top-level.
	ThreadID string
	// Text is the sanitized message body.
	Text string
	// Command holds the command name (e.g. "/run") when Action == "command".
	Command string
	// Args holds the parsed command arguments when Action == "command".
	Args []string
	// CallbackID is the interactive element ID to Ack when Action == "callback".
	CallbackID string
	// Data is the callback payload (e.g. "approve:TASK123") when Action == "callback".
	Data string
	// Sender identifies the user who triggered the event.
	Sender Identity
	// Mentions lists the user IDs @-mentioned in the message.
	Mentions []string
}

// Identity is the minimal user representation carried in MessageEvent.
// UserID is opaque and platform-specific; host applications resolve it to
// a display name or internal user record.
type Identity struct {
	UserID      string
	DisplayName string
	IsBot       bool
}

// OutboundMessage is the normalized outbound payload for all chat adapters.
// Rich formatting (Block Kit, inline keyboards, Discord embeds) is an adapter
// concern — core only models Text and optional interactive Buttons.
type OutboundMessage struct {
	// ChannelID is the destination channel, chat, or room.
	ChannelID string
	// ThreadID, when set, causes the message to be sent as a reply in that thread.
	ThreadID string
	// Text is the message body.
	Text string
	// Buttons is an optional list of interactive buttons; adapters render them
	// in their native format (Block Kit / inline keyboard / action rows).
	Buttons []Button
}

// Button is a single interactive element within an OutboundMessage.
type Button struct {
	// Label is the human-readable button text.
	Label string
	// ActionID is the stable identifier for this action, used in callback routing.
	ActionID string
	// Data is the payload returned in MessageEvent.Data when the button is pressed.
	Data string
}

// MessageRef is a handle to a previously sent message, used for edits.
type MessageRef struct {
	ChannelID string
	MessageID string
	ThreadID  string
}

// MessageHandler processes a normalized inbound message event.
// It is the chat equivalent of IssueHandler.
type MessageHandler interface {
	HandleMessage(ctx context.Context, ev MessageEvent) error
}

// ChatCapable is implemented by adapters that provide a chat bridge.
// It is the chat equivalent of Pollable.
type ChatCapable interface {
	Adapter

	// NewChatBridge creates a ChatBridge using the given dependencies.
	NewChatBridge(deps ChatDeps) ChatBridge
}

// ChatBridge manages the full lifecycle of a chat integration: inbound
// listening, outbound sending, in-place editing, and callback acknowledgement.
// It is the chat equivalent of Poller.
type ChatBridge interface {
	// Start blocks while the adapter's inbound listener runs (long-poll,
	// WebSocket, or gateway). It returns when ctx is cancelled.
	Start(ctx context.Context) error

	// Send delivers an outbound message and returns a ref for future edits.
	Send(ctx context.Context, m OutboundMessage) (MessageRef, error)

	// Edit replaces the text of a previously sent message in-place.
	Edit(ctx context.Context, ref MessageRef, text string) error

	// Ack acknowledges an interactive callback. Adapters that require an
	// explicit acknowledgement (Slack) must call their platform API; adapters
	// that do not (Telegram) may return nil without a network call.
	Ack(ctx context.Context, callbackID string) error
}

// ChatDeps provides the inbound handler to a ChatBridge. Consuming applications
// supply this — the SDK never constructs it itself. It is the chat equivalent
// of PollerDeps.
type ChatDeps struct {
	// Handler processes each inbound MessageEvent.
	Handler MessageHandler
}
