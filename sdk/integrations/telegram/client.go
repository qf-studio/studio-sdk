package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const telegramAPIURL = "https://api.telegram.org/bot"

// Client is a Telegram Bot API client.
type Client struct {
	botToken   string
	baseURL    string
	httpClient *http.Client
	logger     *slog.Logger
}

// Option configures a Client.
type Option func(*Client)

// WithLogger sets the logger used by the client. Defaults to slog.Default().
func WithLogger(logger *slog.Logger) Option {
	return func(c *Client) {
		c.logger = logger
	}
}

// NewClient creates a new Telegram client.
func NewClient(botToken string, opts ...Option) *Client {
	c := &Client{
		botToken: botToken,
		baseURL:  telegramAPIURL,
		httpClient: &http.Client{
			Timeout: 60 * time.Second, // must exceed the long-poll timeout (30 s)
		},
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// NewClientWithBaseURL creates a client pointing at a custom base URL (for tests).
func NewClientWithBaseURL(botToken, baseURL string, opts ...Option) *Client {
	c := &Client{
		botToken: botToken,
		baseURL:  baseURL + "/bot",
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// GetUpdates retrieves updates using long polling.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout int) ([]*Update, error) {
	url := fmt.Sprintf("%s%s/getUpdates?offset=%d&timeout=%d", c.baseURL, c.botToken, offset, timeout)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get updates: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result GetUpdatesResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.OK {
		return nil, fmt.Errorf("telegram API error: %s (code: %d)", result.Description, result.ErrorCode)
	}

	return result.Result, nil
}

// SendMessage sends a plain text message to a chat.
func (c *Client) SendMessage(ctx context.Context, chatID, text, parseMode string) (*SendMessageResponse, error) {
	payload := SendMessageRequest{
		ChatID:    chatID,
		Text:      text,
		ParseMode: parseMode,
	}
	return c.doSend(ctx, "/sendMessage", payload)
}

// SendMessageWithKeyboard sends a message with an inline keyboard.
func (c *Client) SendMessageWithKeyboard(ctx context.Context, chatID, text, parseMode string, keyboard [][]InlineKeyboardButton) (*SendMessageResponse, error) {
	payload := SendMessageRequest{
		ChatID:    chatID,
		Text:      text,
		ParseMode: parseMode,
		ReplyMarkup: &InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	}
	return c.doSend(ctx, "/sendMessage", payload)
}

// EditMessage edits the text of an existing message.
func (c *Client) EditMessage(ctx context.Context, chatID string, messageID int64, text, parseMode string) error {
	type editRequest struct {
		ChatID    string `json:"chat_id"`
		MessageID int64  `json:"message_id"`
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode,omitempty"`
	}
	payload := editRequest{
		ChatID:    chatID,
		MessageID: messageID,
		Text:      text,
		ParseMode: parseMode,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.baseURL + c.botToken + "/editMessageText"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to edit message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var result SendMessageResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.OK {
		return fmt.Errorf("telegram API error: %s (code: %d)", result.Description, result.ErrorCode)
	}

	return nil
}

// AnswerCallback acknowledges a callback query.
func (c *Client) AnswerCallback(ctx context.Context, callbackID, text string) error {
	type answerRequest struct {
		CallbackQueryID string `json:"callback_query_id"`
		Text            string `json:"text,omitempty"`
	}
	payload := answerRequest{
		CallbackQueryID: callbackID,
		Text:            text,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	url := c.baseURL + c.botToken + "/answerCallbackQuery"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to answer callback: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return nil
}

// doSend marshals payload and POSTs it to the given Telegram endpoint.
func (c *Client) doSend(ctx context.Context, endpoint string, payload interface{}) (*SendMessageResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal message: %w", err)
	}

	url := c.baseURL + c.botToken + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result SendMessageResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if !result.OK {
		return nil, fmt.Errorf("telegram API error: %s (code: %d)", result.Description, result.ErrorCode)
	}

	return &result, nil
}
