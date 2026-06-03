package slack

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

func TestNewClient(t *testing.T) {
	tests := []struct {
		name     string
		botToken string
	}{
		{name: "valid token", botToken: testutil.FakeSlackBotToken},
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
			if c.logger == nil {
				t.Error("logger is nil")
			}
		})
	}
}

func TestNewClientWithBaseURL(t *testing.T) {
	c := NewClientWithBaseURL(testutil.FakeSlackBotToken, "http://localhost:9999")
	if c.baseURL != "http://localhost:9999" {
		t.Errorf("baseURL = %q, want http://localhost:9999", c.baseURL)
	}
}

// mockTransport is a custom RoundTripper for testing.
type mockTransport struct {
	handler func(*http.Request) (*http.Response, error)
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.handler(req)
}

func TestPostMessageSuccess(t *testing.T) {
	want := PostMessageResponse{OK: true, TS: "1234567890.123456", Channel: "C1234567890"}

	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Errorf("method = %q, want POST", req.Method)
			}
			if !strings.HasSuffix(req.URL.Path, "/chat.postMessage") {
				t.Errorf("path = %q, want /chat.postMessage suffix", req.URL.Path)
			}
			if ct := req.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if auth := req.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
				t.Errorf("Authorization = %q, want Bearer prefix", auth)
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
		botToken:   testutil.FakeSlackBotToken,
		baseURL:    slackAPIURL,
		httpClient: &http.Client{Transport: transport},
		logger:     nil,
	}

	got, err := c.PostMessage(context.Background(), &Message{Channel: "#general", Text: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TS != want.TS {
		t.Errorf("TS = %q, want %q", got.TS, want.TS)
	}
}

func TestPostMessageAPIError(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			body, _ := json.Marshal(PostMessageResponse{OK: false, Error: "channel_not_found"})
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(body))),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		botToken:   testutil.FakeSlackBotToken,
		baseURL:    slackAPIURL,
		httpClient: &http.Client{Transport: transport},
	}

	_, err := c.PostMessage(context.Background(), &Message{Channel: "#none", Text: "hi"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("error = %q, want to contain channel_not_found", err.Error())
	}
}

func TestPostMessageNetworkError(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("network error: connection refused")
		},
	}

	c := &Client{
		botToken:   testutil.FakeSlackBotToken,
		baseURL:    slackAPIURL,
		httpClient: &http.Client{Transport: transport},
	}

	_, err := c.PostMessage(context.Background(), &Message{Channel: "#test", Text: "test"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to post message") {
		t.Errorf("error = %q, want to contain 'failed to post message'", err.Error())
	}
}

func TestPostMessageInvalidJSON(t *testing.T) {
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
		botToken:   testutil.FakeSlackBotToken,
		baseURL:    slackAPIURL,
		httpClient: &http.Client{Transport: transport},
	}

	_, err := c.PostMessage(context.Background(), &Message{Channel: "#test", Text: "test"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to parse response") {
		t.Errorf("error = %q, want to contain 'failed to parse response'", err.Error())
	}
}

func TestUpdateMessageSuccess(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(req.URL.Path, "/chat.update") {
				t.Errorf("path = %q, want /chat.update suffix", req.URL.Path)
			}
			body, _ := json.Marshal(map[string]interface{}{"ok": true})
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(body))),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		botToken:   testutil.FakeSlackBotToken,
		baseURL:    slackAPIURL,
		httpClient: &http.Client{Transport: transport},
	}

	err := c.UpdateMessage(context.Background(), "C123", "1234.5678", &Message{Text: "updated"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpdateMessageAPIError(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			body, _ := json.Marshal(map[string]interface{}{"ok": false, "error": "message_not_found"})
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(body))),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		botToken:   testutil.FakeSlackBotToken,
		baseURL:    slackAPIURL,
		httpClient: &http.Client{Transport: transport},
	}

	err := c.UpdateMessage(context.Background(), "C123", "0.0", &Message{Text: "fail"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "message_not_found") {
		t.Errorf("error = %q, want to contain message_not_found", err.Error())
	}
}

func TestUpdateMessageNetworkError(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("network error")
		},
	}

	c := &Client{
		botToken:   testutil.FakeSlackBotToken,
		baseURL:    slackAPIURL,
		httpClient: &http.Client{Transport: transport},
	}

	err := c.UpdateMessage(context.Background(), "C123", "123.456", &Message{Text: "test"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to update message") {
		t.Errorf("error = %q, want to contain 'failed to update message'", err.Error())
	}
}

func TestUpdateMessageInvalidJSON(t *testing.T) {
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
		botToken:   testutil.FakeSlackBotToken,
		baseURL:    slackAPIURL,
		httpClient: &http.Client{Transport: transport},
	}

	err := c.UpdateMessage(context.Background(), "C123", "123.456", &Message{Text: "test"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "failed to parse response") {
		t.Errorf("error = %q, want to contain 'failed to parse response'", err.Error())
	}
}

func TestPostInteractiveMessageSuccess(t *testing.T) {
	want := PostMessageResponse{OK: true, TS: "9999.8888", Channel: "C1"}

	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(req.URL.Path, "/chat.postMessage") {
				t.Errorf("path = %q, want /chat.postMessage suffix", req.URL.Path)
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
		botToken:   testutil.FakeSlackBotToken,
		baseURL:    slackAPIURL,
		httpClient: &http.Client{Transport: transport},
	}

	got, err := c.PostInteractiveMessage(context.Background(), &InteractiveMessage{
		Channel: "C1",
		Text:    "choose",
		Blocks:  []interface{}{map[string]string{"type": "section"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TS != want.TS {
		t.Errorf("TS = %q, want %q", got.TS, want.TS)
	}
}

func TestUpdateInteractiveMessageSuccess(t *testing.T) {
	transport := &mockTransport{
		handler: func(req *http.Request) (*http.Response, error) {
			body, _ := json.Marshal(map[string]interface{}{"ok": true})
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(body))),
				Header:     make(http.Header),
			}, nil
		},
	}

	c := &Client{
		botToken:   testutil.FakeSlackBotToken,
		baseURL:    slackAPIURL,
		httpClient: &http.Client{Transport: transport},
	}

	err := c.UpdateInteractiveMessage(context.Background(), "C1", "123.456", nil, "done")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClientContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		resp := PostMessageResponse{OK: true, TS: "123"}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := NewClientWithBaseURL(testutil.FakeSlackBotToken, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.PostMessage(ctx, &Message{Channel: "#test", Text: "test"})
	if err == nil {
		t.Error("expected error due to canceled context")
	}
}

func TestMessageJSONRoundTrip(t *testing.T) {
	original := Message{
		Channel: "#dev",
		Text:    "hello",
		Blocks: []Block{
			{Type: "section", Text: &TextObject{Type: "mrkdwn", Text: "*bold*"}},
		},
		Attachments: []Attachment{{Color: "good", Title: "ok"}},
		ThreadTS:    "1234567890.000000",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Message
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Channel != original.Channel {
		t.Errorf("Channel = %q, want %q", decoded.Channel, original.Channel)
	}
	if decoded.ThreadTS != original.ThreadTS {
		t.Errorf("ThreadTS = %q, want %q", decoded.ThreadTS, original.ThreadTS)
	}
	if len(decoded.Blocks) != len(original.Blocks) {
		t.Errorf("Blocks len = %d, want %d", len(decoded.Blocks), len(original.Blocks))
	}
}
