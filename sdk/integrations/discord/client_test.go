package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/studio-sdk/sdk/testutil"
)

// mockTransport is a custom RoundTripper for unit tests.
type mockTransport struct {
	handler func(*http.Request) (*http.Response, error)
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.handler(req)
}

func TestNewClient(t *testing.T) {
	tests := []struct {
		name     string
		botToken string
	}{
		{name: "valid token", botToken: testutil.FakeDiscordToken},
		{name: "empty token", botToken: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient(tt.botToken)
			if c == nil {
				t.Fatal("NewClient returned nil")
			}
			if c.botToken != tt.botToken {
				t.Errorf("botToken = %q, want %q", c.botToken, tt.botToken)
			}
			if c.httpClient == nil {
				t.Error("httpClient is nil")
			}
			if c.httpClient.Timeout != 30*time.Second {
				t.Errorf("httpClient.Timeout = %v, want 30s", c.httpClient.Timeout)
			}
			if c.log == nil {
				t.Error("log is nil")
			}
			if c.maxRetries != 3 {
				t.Errorf("maxRetries = %d, want 3", c.maxRetries)
			}
		})
	}
}

func TestNewClientWithBaseURL(t *testing.T) {
	c := NewClientWithBaseURL(testutil.FakeDiscordToken, "http://localhost:9999")
	if c.baseURL != "http://localhost:9999" {
		t.Errorf("baseURL = %q, want http://localhost:9999", c.baseURL)
	}
}

func TestWithLogger(t *testing.T) {
	c := NewClient(testutil.FakeDiscordToken)
	if c.log == nil {
		t.Fatal("default log is nil")
	}
}

func TestSendMessageSuccess(t *testing.T) {
	want := Message{ID: "msg1", ChannelID: "chan1", Content: "hello"}

	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Errorf("method = %q, want POST", req.Method)
			}
			if !strings.Contains(req.URL.Path, "/channels/chan1/messages") {
				t.Errorf("path = %q, want /channels/chan1/messages", req.URL.Path)
			}
			if auth := req.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bot ") {
				t.Errorf("Authorization = %q, want Bot prefix", auth)
			}
			body, _ := json.Marshal(want)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(body))),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		botToken:   testutil.FakeDiscordToken,
		baseURL:    DiscordAPIURL,
		httpClient: &http.Client{Transport: transport},
		log:        nil,
		maxRetries: 3,
	}

	got, err := c.SendMessage(context.Background(), "chan1", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
}

func TestSendMessageAPIError(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Body:       io.NopCloser(strings.NewReader(`{"message":"Missing Permissions"}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		botToken:   testutil.FakeDiscordToken,
		baseURL:    DiscordAPIURL,
		httpClient: &http.Client{Transport: transport},
		maxRetries: 0,
	}

	_, err := c.SendMessage(context.Background(), "chan1", "hi")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("error = %q, want HTTP 403", err.Error())
	}
}

func TestSendMessageNetworkError(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}

	c := &Client{
		botToken:   testutil.FakeDiscordToken,
		baseURL:    DiscordAPIURL,
		httpClient: &http.Client{Transport: transport},
		maxRetries: 0,
	}

	_, err := c.SendMessage(context.Background(), "chan1", "hi")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "send request") {
		t.Errorf("error = %q, want 'send request'", err.Error())
	}
}

func TestSendMessageInvalidJSON(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("not valid json")),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		botToken:   testutil.FakeDiscordToken,
		baseURL:    DiscordAPIURL,
		httpClient: &http.Client{Transport: transport},
		maxRetries: 0,
	}

	_, err := c.SendMessage(context.Background(), "chan1", "hi")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "parse response") {
		t.Errorf("error = %q, want 'parse response'", err.Error())
	}
}

func TestEditMessageSuccess(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPatch {
				t.Errorf("method = %q, want PATCH", req.Method)
			}
			if !strings.Contains(req.URL.Path, "/channels/chan1/messages/msg1") {
				t.Errorf("path = %q, want /channels/chan1/messages/msg1", req.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"id":"msg1"}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		botToken:   testutil.FakeDiscordToken,
		baseURL:    DiscordAPIURL,
		httpClient: &http.Client{Transport: transport},
		maxRetries: 0,
	}

	err := c.EditMessage(context.Background(), "chan1", "msg1", "updated content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEditMessageError(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader(`{"message":"Unknown Message"}`)),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		botToken:   testutil.FakeDiscordToken,
		baseURL:    DiscordAPIURL,
		httpClient: &http.Client{Transport: transport},
		maxRetries: 0,
	}

	err := c.EditMessage(context.Background(), "chan1", "msg1", "content")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "edit message") {
		t.Errorf("error = %q, want 'edit message'", err.Error())
	}
}

func TestSendMessageWithComponentsSuccess(t *testing.T) {
	want := Message{ID: "msg2", ChannelID: "chan1"}

	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			var body map[string]interface{}
			_ = json.NewDecoder(req.Body).Decode(&body)
			if _, ok := body["components"]; !ok {
				t.Error("expected components in request body")
			}
			data, _ := json.Marshal(want)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(data))),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		botToken:   testutil.FakeDiscordToken,
		baseURL:    DiscordAPIURL,
		httpClient: &http.Client{Transport: transport},
		maxRetries: 0,
	}

	components := []Component{
		{
			Type: 1,
			Components: []Button{
				{Type: 2, Style: 1, Label: "Execute", CustomID: "execute_task"},
			},
		},
	}

	got, err := c.SendMessageWithComponents(context.Background(), "chan1", "choose an action", components)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
}

func TestCreateInteractionResponseSuccess(t *testing.T) {
	var receivedType int

	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			if !strings.Contains(req.URL.Path, "/interactions/int1/tok1/callback") {
				t.Errorf("path = %q, want /interactions/int1/tok1/callback", req.URL.Path)
			}
			var payload struct {
				Type int `json:"type"`
			}
			_ = json.NewDecoder(req.Body).Decode(&payload)
			receivedType = payload.Type
			return &http.Response{
				StatusCode: http.StatusNoContent,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		botToken:   testutil.FakeDiscordToken,
		baseURL:    DiscordAPIURL,
		httpClient: &http.Client{Transport: transport},
		maxRetries: 0,
	}

	err := c.CreateInteractionResponse(context.Background(), "int1", "tok1", InteractionResponseDeferredUpdateMessage, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedType != InteractionResponseDeferredUpdateMessage {
		t.Errorf("type = %d, want %d", receivedType, InteractionResponseDeferredUpdateMessage)
	}
}

func TestRateLimitHandling(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.Header().Set("Retry-After", "0.01")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg1","channel_id":"chan1"}`))
	}))
	defer server.Close()

	c := NewClientWithBaseURL(testutil.FakeDiscordToken, server.URL)
	msg, err := c.SendMessage(context.Background(), "chan1", "hello")
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if msg == nil || msg.ID != "msg1" {
		t.Error("expected valid message after rate limit retry")
	}
	if attempt != 2 {
		t.Errorf("expected 2 attempts, got %d", attempt)
	}
}

func TestRateLimitExhausted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0.01")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"rate limited"}`))
	}))
	defer server.Close()

	c := NewClientWithBaseURL(testutil.FakeDiscordToken, server.URL)
	_, err := c.SendMessage(context.Background(), "chan1", "hi")
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error = %q, want rate limited", err.Error())
	}
}

func TestRateLimitContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	c := NewClientWithBaseURL(testutil.FakeDiscordToken, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.SendMessage(ctx, "chan1", "hi")
	if err == nil {
		t.Fatal("expected error due to cancelled context")
	}
}

func TestContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"id":"msg1"}`))
	}))
	defer server.Close()

	c := NewClientWithBaseURL(testutil.FakeDiscordToken, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.SendMessage(ctx, "chan1", "test")
	if err == nil {
		t.Error("expected error due to canceled context")
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected time.Duration
	}{
		{"float seconds", "1.5", 1500 * time.Millisecond},
		{"integer seconds", "2", 2 * time.Second},
		{"empty", "", 0},
		{"invalid", "not-a-number", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := make(http.Header)
			if tt.header != "" {
				h.Set("Retry-After", tt.header)
			}
			got := parseRetryAfter(h)
			if got != tt.expected {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.header, got, tt.expected)
			}
		})
	}
}
