package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// socketListener is the interface the bridge needs from the Socket Mode client.
type socketListener interface {
	Listen(ctx context.Context) (<-chan SocketModeEvent, error)
}

// bridge implements core.ChatBridge for Slack via Socket Mode.
type bridge struct {
	client          *Client
	socket          socketListener
	deps            core.ChatDeps
	allowedChannels map[string]bool
	allowedUsers    map[string]bool
	logger          *slog.Logger
}

// Start runs the Socket Mode event loop until ctx is cancelled.
func (b *bridge) Start(ctx context.Context) error {
	events, err := b.socket.Listen(ctx)
	if err != nil {
		return fmt.Errorf("slack: failed to start socket mode listener: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt, ok := <-events:
			if !ok {
				return nil
			}
			b.processSocketEvent(ctx, evt)
		}
	}
}

// Send delivers an outbound message and returns a ref for future edits.
// If the message has Buttons, renders them as a Block Kit actions block.
func (b *bridge) Send(ctx context.Context, m core.OutboundMessage) (core.MessageRef, error) {
	var resp *PostMessageResponse
	var err error

	if len(m.Buttons) > 0 {
		im := &InteractiveMessage{
			Channel:  m.ChannelID,
			Text:     m.Text,
			Blocks:   buttonsToBlockKit(m.Text, m.Buttons),
			ThreadTS: m.ThreadID,
		}
		resp, err = b.client.PostInteractiveMessage(ctx, im)
	} else {
		msg := &Message{
			Channel:  m.ChannelID,
			Text:     m.Text,
			ThreadTS: m.ThreadID,
		}
		resp, err = b.client.PostMessage(ctx, msg)
	}

	if err != nil {
		return core.MessageRef{}, err
	}

	ts := ""
	if resp != nil {
		ts = resp.TS
	}
	return core.MessageRef{
		ChannelID: m.ChannelID,
		MessageID: ts,
		ThreadID:  m.ThreadID,
	}, nil
}

// Edit replaces the text of a previously sent message in-place.
func (b *bridge) Edit(ctx context.Context, ref core.MessageRef, text string) error {
	return b.client.UpdateMessage(ctx, ref.ChannelID, ref.MessageID, &Message{Text: text})
}

// Ack acknowledges a callback. For Socket Mode, the WS-level ack is handled
// automatically by SocketModeHandler, so this is a no-op.
func (b *bridge) Ack(_ context.Context, _ string) error {
	return nil
}

// processSocketEvent routes a raw SocketModeEvent to the appropriate handler.
func (b *bridge) processSocketEvent(ctx context.Context, evt SocketModeEvent) {
	switch evt.Type {
	case SocketEventMessage:
		b.handleEventsAPI(ctx, evt)
	case SocketEventInteraction:
		b.handleInteractive(ctx, evt)
	}
}

// handleEventsAPI processes an events_api envelope (message or app_mention).
func (b *bridge) handleEventsAPI(ctx context.Context, evt SocketModeEvent) {
	se, err := parseEventsAPIPayload(evt.Payload)
	if err != nil {
		b.logger.Warn("slack: failed to parse events_api payload", slog.Any("error", err))
		return
	}
	if se == nil {
		return
	}
	if se.IsBotMessage() {
		return
	}
	if !b.isAllowed(se.ChannelID, se.UserID) {
		b.logger.Debug("slack: ignoring message from unauthorized channel/user",
			slog.String("channel", se.ChannelID),
			slog.String("user", se.UserID))
		return
	}

	text := strings.TrimSpace(se.Text)
	if text == "" {
		return
	}

	var ev core.MessageEvent
	if strings.HasPrefix(text, "/") {
		parts := strings.Fields(text)
		ev = core.MessageEvent{
			Action:    "command",
			MessageID: se.Timestamp,
			ChannelID: se.ChannelID,
			ThreadID:  se.ThreadTS,
			Command:   parts[0],
			Args:      parts[1:],
			Sender:    core.Identity{UserID: se.UserID},
		}
	} else {
		ev = core.MessageEvent{
			Action:    "message",
			MessageID: se.Timestamp,
			ChannelID: se.ChannelID,
			ThreadID:  se.ThreadTS,
			Text:      sanitizeMessageText(text, b.logger), // sanitize before handler sees it
			Sender:    core.Identity{UserID: se.UserID},
		}
	}

	if err := b.deps.Handler.HandleMessage(ctx, ev); err != nil {
		b.logger.Warn("slack: handler error", slog.Any("error", err))
	}
}

// handleInteractive processes an interactive (block_actions) envelope.
func (b *bridge) handleInteractive(ctx context.Context, evt SocketModeEvent) {
	var payload InteractionPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		b.logger.Warn("slack: failed to parse interactive payload", slog.Any("error", err))
		return
	}
	if payload.Type != "block_actions" || len(payload.Actions) == 0 {
		return
	}

	channelID := ""
	if payload.Channel != nil {
		channelID = payload.Channel.ID
	}
	messageTS := ""
	if payload.Message != nil {
		messageTS = payload.Message.TS
	}
	userID := ""
	if payload.User != nil {
		userID = payload.User.ID
	}

	if !b.isAllowed(channelID, userID) {
		b.logger.Debug("slack: ignoring callback from unauthorized channel/user",
			slog.String("channel", channelID),
			slog.String("user", userID))
		return
	}

	action := payload.Actions[0]
	ev := core.MessageEvent{
		Action:     "callback",
		ChannelID:  channelID,
		MessageID:  messageTS,
		CallbackID: action.ActionID,
		Data:       action.Value,
		Sender:     identityFromSlackUser(payload.User),
	}

	if err := b.deps.Handler.HandleMessage(ctx, ev); err != nil {
		b.logger.Warn("slack: handler error on callback", slog.Any("error", err))
	}
}

// isAllowed reports whether a channel+user pair is authorized.
// If no allowlists are configured, all traffic is allowed.
func (b *bridge) isAllowed(channelID, userID string) bool {
	if len(b.allowedChannels) == 0 && len(b.allowedUsers) == 0 {
		return true
	}
	if len(b.allowedChannels) > 0 && b.allowedChannels[channelID] {
		return true
	}
	if len(b.allowedUsers) > 0 && b.allowedUsers[userID] {
		return true
	}
	return false
}

// identityFromSlackUser converts an InteractionUser to a core.Identity.
func identityFromSlackUser(u *InteractionUser) core.Identity {
	if u == nil {
		return core.Identity{}
	}
	name := u.Name
	if name == "" {
		name = u.Username
	}
	return core.Identity{
		UserID:      u.ID,
		DisplayName: name,
	}
}

// buttonsToBlockKit renders a text string and buttons as Block Kit blocks.
// Returns a section block followed by an actions block.
func buttonsToBlockKit(text string, buttons []core.Button) []interface{} {
	var blocks []interface{}
	if text != "" {
		blocks = append(blocks, map[string]interface{}{
			"type": "section",
			"text": map[string]interface{}{
				"type": "mrkdwn",
				"text": text,
			},
		})
	}

	elements := make([]interface{}, 0, len(buttons))
	for _, btn := range buttons {
		elements = append(elements, map[string]interface{}{
			"type": "button",
			"text": map[string]interface{}{
				"type":  "plain_text",
				"text":  btn.Label,
				"emoji": true,
			},
			"action_id": btn.ActionID,
			"value":     btn.Data,
		})
	}
	blocks = append(blocks, map[string]interface{}{
		"type":     "actions",
		"elements": elements,
	})
	return blocks
}
