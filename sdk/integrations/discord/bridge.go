package discord

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// gatewayListener is the interface the bridge needs from the Gateway client.
type gatewayListener interface {
	StartListening(ctx context.Context) (<-chan GatewayEvent, error)
}

// bridge implements core.ChatBridge for Discord via the Gateway WebSocket.
type bridge struct {
	client          *Client
	gateway         gatewayListener
	deps            core.ChatDeps
	botID           string
	allowedGuilds   map[string]bool
	allowedChannels map[string]bool
	log             *slog.Logger
}

// Start runs the Gateway event loop until ctx is cancelled.
func (b *bridge) Start(ctx context.Context) error {
	events, err := b.gateway.StartListening(ctx)
	if err != nil {
		return fmt.Errorf("discord: failed to start gateway listener: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case evt, ok := <-events:
			if !ok {
				return nil
			}
			b.processGatewayEvent(ctx, evt)
		}
	}
}

// Send delivers an outbound message. Renders Buttons as a Discord action row.
func (b *bridge) Send(ctx context.Context, m core.OutboundMessage) (core.MessageRef, error) {
	var msg *Message
	var err error

	if len(m.Buttons) > 0 {
		msg, err = b.client.SendMessageWithComponents(ctx, m.ChannelID, m.Text, buttonsToActionRow(m.Buttons))
	} else {
		msg, err = b.client.SendMessage(ctx, m.ChannelID, m.Text)
	}
	if err != nil {
		return core.MessageRef{}, err
	}

	ref := core.MessageRef{ChannelID: m.ChannelID, ThreadID: m.ThreadID}
	if msg != nil {
		ref.MessageID = msg.ID
	}
	return ref, nil
}

// Edit replaces the text of a previously sent message in-place.
func (b *bridge) Edit(ctx context.Context, ref core.MessageRef, text string) error {
	return b.client.EditMessage(ctx, ref.ChannelID, ref.MessageID, text)
}

// Ack acknowledges an interaction. callbackID must be in the form "interactionID/interactionToken".
func (b *bridge) Ack(ctx context.Context, callbackID string) error {
	idx := strings.Index(callbackID, "/")
	if idx < 0 {
		return fmt.Errorf("discord: invalid callbackID %q (expected id/token)", callbackID)
	}
	interactionID := callbackID[:idx]
	interactionToken := callbackID[idx+1:]
	return b.client.CreateInteractionResponse(ctx, interactionID, interactionToken, InteractionResponseDeferredUpdateMessage, "")
}

// processGatewayEvent routes a raw GatewayEvent to the appropriate handler.
func (b *bridge) processGatewayEvent(ctx context.Context, evt GatewayEvent) {
	if evt.Op != OpcodeDispatch || evt.T == nil {
		return
	}
	switch *evt.T {
	case "MESSAGE_CREATE":
		b.handleMessageCreate(ctx, evt)
	case "INTERACTION_CREATE":
		b.handleInteractionCreate(ctx, evt)
	}
}

func (b *bridge) handleMessageCreate(ctx context.Context, evt GatewayEvent) {
	msg, err := evt.ParseMessageCreate()
	if err != nil {
		b.log.Warn("discord: failed to parse MESSAGE_CREATE", slog.Any("error", err))
		return
	}

	if msg.Author.Bot {
		return
	}
	if !b.isAllowed(msg.GuildID, msg.ChannelID) {
		b.log.Debug("discord: ignoring message from unauthorized guild/channel",
			slog.String("guild_id", msg.GuildID),
			slog.String("channel_id", msg.ChannelID))
		return
	}

	text := strings.TrimSpace(StripBotMention(msg.Content, b.botID))
	if text == "" {
		return
	}

	var ev core.MessageEvent
	if strings.HasPrefix(text, "/") {
		parts := strings.Fields(text)
		ev = core.MessageEvent{
			Action:    "command",
			MessageID: msg.ID,
			ChannelID: msg.ChannelID,
			Command:   parts[0],
			Args:      parts[1:],
			Sender:    core.Identity{UserID: msg.Author.ID, DisplayName: msg.Author.Username},
		}
	} else {
		ev = core.MessageEvent{
			Action:    "message",
			MessageID: msg.ID,
			ChannelID: msg.ChannelID,
			Text:      sanitizeMessageText(text, b.log), // sanitize before handler sees it
			Sender:    core.Identity{UserID: msg.Author.ID, DisplayName: msg.Author.Username},
		}
	}

	if err := b.deps.Handler.HandleMessage(ctx, ev); err != nil {
		b.log.Warn("discord: handler error on message", slog.Any("error", err))
	}
}

func (b *bridge) handleInteractionCreate(ctx context.Context, evt GatewayEvent) {
	ic, err := evt.ParseInteractionCreate()
	if err != nil {
		b.log.Warn("discord: failed to parse INTERACTION_CREATE", slog.Any("error", err))
		return
	}

	// Only handle MESSAGE_COMPONENT (type 3).
	if ic.Type != 3 {
		return
	}

	userID := ""
	displayName := ""
	if ic.User != nil {
		userID = ic.User.ID
		displayName = ic.User.Username
	} else if ic.Member != nil {
		userID = ic.Member.User.ID
		displayName = ic.Member.User.Username
		if ic.Member.Nick != "" {
			displayName = ic.Member.Nick
		}
	}

	if !b.isAllowed(ic.GuildID, ic.ChannelID) {
		b.log.Debug("discord: ignoring interaction from unauthorized guild/channel",
			slog.String("guild_id", ic.GuildID),
			slog.String("channel_id", ic.ChannelID))
		return
	}

	ev := core.MessageEvent{
		Action:     "callback",
		ChannelID:  ic.ChannelID,
		CallbackID: ic.ID + "/" + ic.Token, // encoded for Ack
		Data:       ic.Data.CustomID,
		Sender:     core.Identity{UserID: userID, DisplayName: displayName},
	}

	if err := b.deps.Handler.HandleMessage(ctx, ev); err != nil {
		b.log.Warn("discord: handler error on interaction", slog.Any("error", err))
	}
}

// isAllowed reports whether a guild+channel pair is authorized.
// DMs have an empty guildID; channel check takes precedence over guild check.
func (b *bridge) isAllowed(guildID, channelID string) bool {
	if len(b.allowedGuilds) == 0 && len(b.allowedChannels) == 0 {
		return true
	}
	if len(b.allowedChannels) > 0 && b.allowedChannels[channelID] {
		return true
	}
	if len(b.allowedGuilds) > 0 {
		if guildID == "" {
			return len(b.allowedChannels) == 0
		}
		return b.allowedGuilds[guildID]
	}
	return false
}

// buttonsToActionRow converts core.Button slice into a single Discord action row component.
func buttonsToActionRow(buttons []core.Button) []Component {
	btns := make([]Button, 0, len(buttons))
	for _, b := range buttons {
		btns = append(btns, Button{
			Type:     2, // BUTTON
			Style:    1, // PRIMARY
			Label:    b.Label,
			CustomID: b.ActionID,
		})
	}
	return []Component{{
		Type:       1, // ACTION_ROW
		Components: btns,
	}}
}
