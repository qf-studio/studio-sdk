package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/core"
)

// bridge implements core.ChatBridge for Telegram via long-poll (getUpdates).
type bridge struct {
	client     *Client
	deps       core.ChatDeps
	allowedIDs map[int64]bool
	logger     *slog.Logger

	mu     sync.Mutex
	offset int64
}

// Start blocks running the long-poll loop until ctx is cancelled.
func (b *bridge) Start(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		updates, err := b.client.GetUpdates(ctx, b.getOffset(), 30)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			b.logger.Warn("telegram: error fetching updates", slog.Any("error", err))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}

		for _, update := range updates {
			b.processUpdate(ctx, update)
			b.mu.Lock()
			if update.UpdateID >= b.offset {
				b.offset = update.UpdateID + 1
			}
			b.mu.Unlock()
		}
	}
}

// Send delivers an outbound message and returns a ref for future edits.
func (b *bridge) Send(ctx context.Context, m core.OutboundMessage) (core.MessageRef, error) {
	var resp *SendMessageResponse
	var err error

	if len(m.Buttons) > 0 {
		keyboard := buttonsToKeyboard(m.Buttons)
		resp, err = b.client.SendMessageWithKeyboard(ctx, m.ChannelID, m.Text, "", keyboard)
	} else {
		resp, err = b.client.SendMessage(ctx, m.ChannelID, m.Text, "")
	}
	if err != nil {
		return core.MessageRef{}, err
	}

	msgID := ""
	if resp != nil && resp.Result != nil {
		msgID = strconv.FormatInt(resp.Result.MessageID, 10)
	}
	return core.MessageRef{
		ChannelID: m.ChannelID,
		MessageID: msgID,
	}, nil
}

// Edit replaces the text of a previously sent message in-place.
func (b *bridge) Edit(ctx context.Context, ref core.MessageRef, text string) error {
	msgID, err := strconv.ParseInt(ref.MessageID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid message ID %q: %w", ref.MessageID, err)
	}
	return b.client.EditMessage(ctx, ref.ChannelID, msgID, text, "")
}

// Ack acknowledges a callback query (required by Telegram to clear the loading indicator).
func (b *bridge) Ack(ctx context.Context, callbackID string) error {
	return b.client.AnswerCallback(ctx, callbackID, "")
}

// getOffset returns the current update offset.
func (b *bridge) getOffset() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.offset
}

// processUpdate maps a single Telegram Update to a core.MessageEvent and calls the handler.
func (b *bridge) processUpdate(ctx context.Context, update *Update) {
	if update.CallbackQuery != nil {
		b.handleCallback(ctx, update.CallbackQuery)
		return
	}

	if update.Message == nil || update.Message.Text == "" {
		return
	}

	msg := update.Message

	// AllowedIDs enforcement: ignore messages from unauthorized senders/chats.
	if len(b.allowedIDs) > 0 {
		senderID := int64(0)
		if msg.From != nil {
			senderID = msg.From.ID
		}
		if !b.allowedIDs[msg.Chat.ID] && !b.allowedIDs[senderID] {
			b.logger.Debug("telegram: ignoring message from unauthorized sender",
				slog.Int64("chat_id", msg.Chat.ID),
				slog.Int64("sender_id", senderID),
			)
			return
		}
	}

	channelID := strconv.FormatInt(msg.Chat.ID, 10)
	messageID := strconv.FormatInt(msg.MessageID, 10)
	sender := identityFromUser(msg.From)
	text := strings.TrimSpace(msg.Text)

	var ev core.MessageEvent
	if strings.HasPrefix(text, "/") {
		parts := strings.Fields(text)
		ev = core.MessageEvent{
			Action:    "command",
			MessageID: messageID,
			ChannelID: channelID,
			Command:   parts[0],
			Args:      parts[1:],
			Sender:    sender,
		}
	} else {
		ev = core.MessageEvent{
			Action:    "message",
			MessageID: messageID,
			ChannelID: channelID,
			Text:      sanitizeMessageText(text, b.logger), // sanitize before handler sees it
			Sender:    sender,
		}
	}

	if err := b.deps.Handler.HandleMessage(ctx, ev); err != nil {
		b.logger.Warn("telegram: handler error", slog.Any("error", err))
	}
}

// handleCallback maps a CallbackQuery to a core.MessageEvent with Action:"callback".
func (b *bridge) handleCallback(ctx context.Context, cb *CallbackQuery) {
	if len(b.allowedIDs) > 0 && cb.From != nil && !b.allowedIDs[cb.From.ID] {
		b.logger.Debug("telegram: ignoring callback from unauthorized sender",
			slog.Int64("sender_id", cb.From.ID),
		)
		return
	}

	ev := core.MessageEvent{
		Action:     "callback",
		CallbackID: cb.ID,
		Data:       cb.Data,
		Sender:     identityFromUser(cb.From),
	}
	if cb.Message != nil {
		ev.ChannelID = strconv.FormatInt(cb.Message.Chat.ID, 10)
		ev.MessageID = strconv.FormatInt(cb.Message.MessageID, 10)
	}

	if err := b.deps.Handler.HandleMessage(ctx, ev); err != nil {
		b.logger.Warn("telegram: handler error on callback", slog.Any("error", err))
	}
}

// identityFromUser converts a Telegram User to a core.Identity.
func identityFromUser(u *User) core.Identity {
	if u == nil {
		return core.Identity{}
	}
	display := strings.TrimSpace(u.FirstName + " " + u.LastName)
	return core.Identity{
		UserID:      strconv.FormatInt(u.ID, 10),
		DisplayName: display,
		IsBot:       u.IsBot,
	}
}

// buttonsToKeyboard converts core.Button slice to a single-row Telegram inline keyboard.
func buttonsToKeyboard(buttons []core.Button) [][]InlineKeyboardButton {
	row := make([]InlineKeyboardButton, 0, len(buttons))
	for _, btn := range buttons {
		row = append(row, InlineKeyboardButton{
			Text:         btn.Label,
			CallbackData: btn.Data,
		})
	}
	return [][]InlineKeyboardButton{row}
}
