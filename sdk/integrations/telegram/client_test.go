package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// newTestServer starts an httptest server and returns it along with a Client
// pointed at it. The handler receives each request and writes resp as JSON.
func newTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := NewClientWithBaseURL(testutil.FakeTelegramToken, srv.URL)
	return srv, client
}

func TestNewClient(t *testing.T) {
	c := NewClient(testutil.FakeTelegramToken)
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
	if c.botToken != testutil.FakeTelegramToken {
		t.Errorf("botToken = %q, want %q", c.botToken, testutil.FakeTelegramToken)
	}
	if c.httpClient == nil {
		t.Fatal("httpClient is nil")
	}
	if c.httpClient.Timeout < 30*time.Second {
		t.Errorf("Timeout = %v, want >= 30s (long-poll safe)", c.httpClient.Timeout)
	}
	if c.logger == nil {
		t.Error("logger is nil")
	}
}

func TestGetUpdates(t *testing.T) {
	tests := []struct {
		name      string
		response  GetUpdatesResponse
		wantErr   bool
		wantCount int
	}{
		{
			name: "empty result",
			response: GetUpdatesResponse{
				OK:     true,
				Result: []*Update{},
			},
			wantCount: 0,
		},
		{
			name: "two updates",
			response: GetUpdatesResponse{
				OK: true,
				Result: []*Update{
					{UpdateID: 1, Message: &Message{MessageID: 1, Chat: &Chat{ID: 100}}},
					{UpdateID: 2, Message: &Message{MessageID: 2, Chat: &Chat{ID: 100}}},
				},
			},
			wantCount: 2,
		},
		{
			name: "API error",
			response: GetUpdatesResponse{
				OK:          false,
				ErrorCode:   401,
				Description: "Unauthorized",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Path, "/getUpdates") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.response)
			})

			updates, err := client.GetUpdates(context.Background(), 0, 1)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(updates) != tt.wantCount {
				t.Errorf("len(updates) = %d, want %d", len(updates), tt.wantCount)
			}
		})
	}
}

func TestSendMessage(t *testing.T) {
	tests := []struct {
		name      string
		chatID    string
		text      string
		parseMode string
		response  SendMessageResponse
		wantErr   bool
	}{
		{
			name:   "plain text",
			chatID: "12345",
			text:   "Hello!",
			response: SendMessageResponse{
				OK:     true,
				Result: &Result{MessageID: 42},
			},
		},
		{
			name:      "markdown",
			chatID:    "12345",
			text:      "*bold*",
			parseMode: "Markdown",
			response:  SendMessageResponse{OK: true, Result: &Result{MessageID: 43}},
		},
		{
			name:   "API error",
			chatID: "bad",
			text:   "x",
			response: SendMessageResponse{
				OK:          false,
				ErrorCode:   400,
				Description: "Bad Request: chat not found",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("method = %q, want POST", r.Method)
				}
				if ct := r.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}
				body, _ := io.ReadAll(r.Body)
				var req SendMessageRequest
				if err := json.Unmarshal(body, &req); err != nil {
					t.Errorf("parse request: %v", err)
				}
				if req.ChatID != tt.chatID {
					t.Errorf("chat_id = %q, want %q", req.ChatID, tt.chatID)
				}
				if req.Text != tt.text {
					t.Errorf("text = %q, want %q", req.Text, tt.text)
				}
				_ = json.NewEncoder(w).Encode(tt.response)
			})

			resp, err := client.SendMessage(context.Background(), tt.chatID, tt.text, tt.parseMode)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Result.MessageID != tt.response.Result.MessageID {
				t.Errorf("MessageID = %d, want %d", resp.Result.MessageID, tt.response.Result.MessageID)
			}
		})
	}
}

func TestSendMessageWithKeyboard(t *testing.T) {
	keyboard := [][]InlineKeyboardButton{
		{{Text: "Approve", CallbackData: "approve:123"}, {Text: "Cancel", CallbackData: "cancel:123"}},
	}

	_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req SendMessageRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("parse request: %v", err)
		}
		if req.ReplyMarkup == nil {
			t.Error("ReplyMarkup is nil")
		} else if len(req.ReplyMarkup.InlineKeyboard) != 1 {
			t.Errorf("keyboard rows = %d, want 1", len(req.ReplyMarkup.InlineKeyboard))
		} else if len(req.ReplyMarkup.InlineKeyboard[0]) != 2 {
			t.Errorf("buttons in row = %d, want 2", len(req.ReplyMarkup.InlineKeyboard[0]))
		}
		_ = json.NewEncoder(w).Encode(SendMessageResponse{OK: true, Result: &Result{MessageID: 99}})
	})

	resp, err := client.SendMessageWithKeyboard(context.Background(), "123", "Pick:", "", keyboard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Result.MessageID != 99 {
		t.Errorf("MessageID = %d, want 99", resp.Result.MessageID)
	}
}

func TestEditMessage(t *testing.T) {
	tests := []struct {
		name      string
		chatID    string
		messageID int64
		text      string
		parseMode string
		response  SendMessageResponse
		wantErr   bool
	}{
		{
			name:      "successful edit",
			chatID:    "12345",
			messageID: 42,
			text:      "updated",
			response:  SendMessageResponse{OK: true, Result: &Result{MessageID: 42}},
		},
		{
			name:      "API error",
			chatID:    "12345",
			messageID: 42,
			text:      "x",
			response:  SendMessageResponse{OK: false, ErrorCode: 400, Description: "message to edit not found"},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Path, "/editMessageText") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(tt.response)
			})

			err := client.EditMessage(context.Background(), tt.chatID, tt.messageID, tt.text, tt.parseMode)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestAnswerCallback(t *testing.T) {
	tests := []struct {
		name       string
		callbackID string
		text       string
	}{
		{name: "silent ack", callbackID: "cbq1", text: ""},
		{name: "with toast", callbackID: "cbq2", text: "Done!"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Path, "/answerCallbackQuery") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				w.WriteHeader(http.StatusOK)
			})

			err := client.AnswerCallback(context.Background(), tt.callbackID, tt.text)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestTypeStructures exercises the type definitions to confirm JSON tags.
func TestTypeStructures(t *testing.T) {
	// Round-trip an Update through JSON.
	original := &Update{
		UpdateID: 999,
		Message: &Message{
			MessageID: 1,
			From:      &User{ID: 7, FirstName: "Alice", Username: "alice"},
			Chat:      &Chat{ID: 100, Type: "private"},
			Date:      1700000000,
			Text:      "hi",
		},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Update
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.UpdateID != original.UpdateID {
		t.Errorf("UpdateID = %d, want %d", got.UpdateID, original.UpdateID)
	}
	if got.Message.From.Username != "alice" {
		t.Errorf("Username = %q, want alice", got.Message.From.Username)
	}
}

func TestConfigFields(t *testing.T) {
	cfg := Config{
		BotToken:   testutil.FakeTelegramToken,
		AllowedIDs: []int64{111, 222, 333},
	}
	if cfg.BotToken != testutil.FakeTelegramToken {
		t.Errorf("BotToken = %q", cfg.BotToken)
	}
	if len(cfg.AllowedIDs) != 3 {
		t.Errorf("AllowedIDs len = %d, want 3", len(cfg.AllowedIDs))
	}
}
