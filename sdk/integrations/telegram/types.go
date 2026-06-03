// Package telegram provides a Telegram Bot API client for the Studio SDK.
// It handles long-poll delivery (getUpdates) and outbound messaging.
package telegram

// Config holds configuration for the Telegram connector.
type Config struct {
	BotToken   string
	AllowedIDs []int64
}

// Update represents a Telegram update from getUpdates.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// Message represents a Telegram message.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from,omitempty"`
	Chat      *Chat  `json:"chat"`
	Date      int64  `json:"date"`
	Text      string `json:"text,omitempty"`
	Caption   string `json:"caption,omitempty"`
}

// CallbackQuery represents a callback query from an inline keyboard.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// User represents a Telegram user or bot.
type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
	IsBot     bool   `json:"is_bot,omitempty"`
}

// Chat represents a Telegram chat.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// InlineKeyboardMarkup represents an inline keyboard attached to a message.
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// InlineKeyboardButton represents a single button in an inline keyboard.
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
}

// GetUpdatesResponse is the API response from getUpdates.
type GetUpdatesResponse struct {
	OK          bool      `json:"ok"`
	Result      []*Update `json:"result,omitempty"`
	Description string    `json:"description,omitempty"`
	ErrorCode   int       `json:"error_code,omitempty"`
}

// SendMessageRequest represents a Telegram sendMessage request.
type SendMessageRequest struct {
	ChatID      string                `json:"chat_id"`
	Text        string                `json:"text"`
	ParseMode   string                `json:"parse_mode,omitempty"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// SendMessageResponse is the API response from sendMessage / editMessageText.
type SendMessageResponse struct {
	OK          bool    `json:"ok"`
	Result      *Result `json:"result,omitempty"`
	Description string  `json:"description,omitempty"`
	ErrorCode   int     `json:"error_code,omitempty"`
}

// Result carries the message identifiers returned by send/edit calls.
type Result struct {
	MessageID int64 `json:"message_id"`
	ChatID    int64 `json:"chat_id,omitempty"`
}
