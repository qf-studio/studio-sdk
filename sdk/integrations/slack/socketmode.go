package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsDialer abstracts WebSocket dialing for testing.
type wsDialer interface {
	DialContext(ctx context.Context, url string) (*websocket.Conn, error)
}

type defaultDialer struct{}

func (d defaultDialer) DialContext(ctx context.Context, url string) (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	return conn, err
}

const (
	initialReconnectDelay = 1 * time.Second
	maxReconnectDelay     = 30 * time.Second
)

// SocketEventType identifies the kind of envelope received over Socket Mode.
type SocketEventType string

const (
	SocketEventMessage     SocketEventType = "events_api"
	SocketEventInteraction SocketEventType = "interactive"
	SocketEventSlashCmd    SocketEventType = "slash_commands"
	SocketEventDisconnect  SocketEventType = "disconnect"
)

// SocketModeEvent is emitted by SocketModeHandler for each received envelope.
// Payload contains the inner payload JSON, already unwrapped from the envelope.
type SocketModeEvent struct {
	Type       SocketEventType
	EnvelopeID string
	Payload    json.RawMessage
}

type envelopeAck struct {
	EnvelopeID string `json:"envelope_id"`
}

// SocketModeHandler manages a Slack Socket Mode WebSocket connection.
// It reads envelopes, acknowledges them, and emits SocketModeEvents on a channel.
type SocketModeHandler struct {
	conn   *websocket.Conn
	events chan SocketModeEvent
	done   chan struct{}
	once   sync.Once
	log    *slog.Logger

	PongWait     time.Duration
	PingInterval time.Duration
}

// NewSocketModeHandler wraps an established WebSocket connection.
// Returns the handler and the channel on which parsed events are emitted.
// Call Run() to start the read loop. logger may be nil to use slog.Default().
func NewSocketModeHandler(conn *websocket.Conn, logger *slog.Logger) (*SocketModeHandler, <-chan SocketModeEvent) {
	if logger == nil {
		logger = slog.Default()
	}
	ch := make(chan SocketModeEvent, 64)
	h := &SocketModeHandler{
		conn:         conn,
		events:       ch,
		done:         make(chan struct{}),
		log:          logger,
		PongWait:     60 * time.Second,
		PingInterval: 30 * time.Second,
	}
	return h, ch
}

// Run starts the read loop and ping ticker. Blocks until the connection closes.
// The events channel is closed on return.
func (h *SocketModeHandler) Run() {
	defer h.cleanup()
	h.wirePongHandler()
	_ = h.conn.SetReadDeadline(time.Now().Add(h.PongWait))
	go h.pingLoop()
	h.readLoop()
}

// Close terminates the handler gracefully.
func (h *SocketModeHandler) Close() {
	h.once.Do(func() {
		close(h.done)
		_ = h.conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
		_ = h.conn.Close()
	})
}

func (h *SocketModeHandler) cleanup() {
	close(h.events)
	h.Close()
}

func (h *SocketModeHandler) wirePongHandler() {
	h.conn.SetPongHandler(func(_ string) error {
		h.log.Debug("slack: pong received")
		return h.conn.SetReadDeadline(time.Now().Add(h.PongWait))
	})
}

func (h *SocketModeHandler) pingLoop() {
	ticker := time.NewTicker(h.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-ticker.C:
			if err := h.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				h.log.Warn("slack: ping write failed", slog.Any("error", err))
				return
			}
		}
	}
}

func (h *SocketModeHandler) readLoop() {
	for {
		select {
		case <-h.done:
			return
		default:
		}
		_, data, err := h.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				h.log.Warn("slack: websocket read error", slog.Any("error", err))
			}
			return
		}
		h.handleRawMessage(data)
	}
}

func (h *SocketModeHandler) handleRawMessage(data []byte) {
	var env socketEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		h.log.Error("slack: failed to parse envelope", slog.Any("error", err))
		return
	}

	// hello confirms the connection is established — no ack needed.
	if env.Type == "hello" {
		h.log.Debug("slack: socket mode connection established")
		return
	}

	if env.EnvelopeID == "" {
		h.log.Warn("slack: envelope missing envelope_id, skipping")
		return
	}

	// Slack requires acknowledgement within 3 seconds.
	if err := h.acknowledge(env.EnvelopeID); err != nil {
		h.log.Error("slack: failed to acknowledge envelope",
			slog.String("envelope_id", env.EnvelopeID),
			slog.Any("error", err))
		// Continue — Slack will redeliver if ack fails.
	}

	h.log.Debug("slack: envelope received",
		slog.String("type", env.Type),
		slog.String("envelope_id", env.EnvelopeID))

	if env.Type == "disconnect" {
		reason := env.Reason
		if reason == "" {
			reason = "server requested disconnect"
		}
		h.log.Info("slack: disconnect received", slog.String("reason", reason))
		h.emit(SocketModeEvent{
			Type:       SocketEventDisconnect,
			EnvelopeID: env.EnvelopeID,
			Payload:    data,
		})
		h.Close()
		return
	}

	evtType, ok := mapSocketEventType(env.Type)
	if !ok {
		h.log.Debug("slack: unknown envelope type, skipping", slog.String("type", env.Type))
		return
	}

	h.emit(SocketModeEvent{
		Type:       evtType,
		EnvelopeID: env.EnvelopeID,
		Payload:    env.Payload,
	})
}

func (h *SocketModeHandler) acknowledge(envelopeID string) error {
	data, err := json.Marshal(envelopeAck{EnvelopeID: envelopeID})
	if err != nil {
		return fmt.Errorf("marshal ack: %w", err)
	}
	return h.conn.WriteMessage(websocket.TextMessage, data)
}

func (h *SocketModeHandler) emit(evt SocketModeEvent) {
	select {
	case h.events <- evt:
	default:
		h.log.Warn("slack: event channel full, dropping event",
			slog.String("type", string(evt.Type)),
			slog.String("envelope_id", evt.EnvelopeID))
	}
}

func mapSocketEventType(raw string) (SocketEventType, bool) {
	switch raw {
	case "events_api":
		return SocketEventMessage, true
	case "interactive":
		return SocketEventInteraction, true
	case "slash_commands":
		return SocketEventSlashCmd, true
	case "disconnect":
		return SocketEventDisconnect, true
	default:
		return "", false
	}
}

// connectionsOpenResponse is the JSON response from apps.connections.open.
type connectionsOpenResponse struct {
	OK    bool   `json:"ok"`
	URL   string `json:"url,omitempty"`
	Error string `json:"error,omitempty"`
}

// ErrAuthFailure indicates the app-level token was rejected by Slack.
var ErrAuthFailure = fmt.Errorf("slack socket mode: authentication failed")

// ErrConnectionOpen indicates a failure opening the Socket Mode connection.
var ErrConnectionOpen = fmt.Errorf("slack socket mode: failed to open connection")

// SocketModeClient connects to Slack's Socket Mode API using an app-level token.
// It handles the initial HTTP handshake, WebSocket management, and reconnection.
type SocketModeClient struct {
	appToken   string
	apiURL     string
	httpClient *http.Client
	dialer     wsDialer
	log        *slog.Logger
}

// NewSocketModeClient creates a Socket Mode client for the given app token.
// logger may be nil to use slog.Default().
func NewSocketModeClient(appToken string, logger *slog.Logger) *SocketModeClient {
	if logger == nil {
		logger = slog.Default()
	}
	return &SocketModeClient{
		appToken:   appToken,
		apiURL:     slackAPIURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		dialer:     defaultDialer{},
		log:        logger,
	}
}

// newSocketModeClientForTest creates a client with a custom API base URL and dialer.
func newSocketModeClientForTest(appToken, apiURL string, d wsDialer, logger *slog.Logger) *SocketModeClient {
	if logger == nil {
		logger = slog.Default()
	}
	return &SocketModeClient{
		appToken:   appToken,
		apiURL:     apiURL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		dialer:     d,
		log:        logger,
	}
}

// OpenConnection calls apps.connections.open with the app-level token and
// returns the WebSocket URL for event streaming.
func (s *SocketModeClient) OpenConnection(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL+"/apps.connections.open", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+s.appToken)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrConnectionOpen, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("%w: failed to read response: %w", ErrConnectionOpen, err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: HTTP %d: %s", ErrConnectionOpen, resp.StatusCode, string(body))
	}

	var result connectionsOpenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("%w: failed to parse response: %w", ErrConnectionOpen, err)
	}

	if !result.OK {
		switch result.Error {
		case "invalid_auth", "not_authed", "account_inactive", "token_revoked":
			return "", fmt.Errorf("%w: %s", ErrAuthFailure, result.Error)
		default:
			return "", fmt.Errorf("%w: %s", ErrConnectionOpen, result.Error)
		}
	}

	if result.URL == "" {
		return "", fmt.Errorf("%w: empty WebSocket URL in response", ErrConnectionOpen)
	}

	return result.URL, nil
}

// Listen establishes the Socket Mode connection and returns a channel of raw
// SocketModeEvents (all envelope types: events_api, interactive, slash_commands).
// Reconnects automatically on disconnect or connection drop.
// Blocks in a background goroutine until ctx is cancelled; the channel is closed
// when the loop exits.
func (s *SocketModeClient) Listen(ctx context.Context) (<-chan SocketModeEvent, error) {
	wssURL, err := s.OpenConnection(ctx)
	if err != nil {
		return nil, fmt.Errorf("initial connection: %w", err)
	}
	out := make(chan SocketModeEvent, 64)
	go s.listenLoop(ctx, wssURL, out)
	return out, nil
}

func (s *SocketModeClient) listenLoop(ctx context.Context, initialURL string, out chan<- SocketModeEvent) {
	defer close(out)

	wssURL := initialURL
	delay := initialReconnectDelay

	for {
		if ctx.Err() != nil {
			return
		}

		reconnect := s.runConnection(ctx, wssURL, out)
		if !reconnect || ctx.Err() != nil {
			return
		}

		s.log.Info("slack: reconnecting to Socket Mode", slog.Duration("delay", delay))
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		newURL, err := s.OpenConnection(ctx)
		if err != nil {
			s.log.Error("slack: failed to get WSS URL for reconnect", slog.Any("error", err))
			delay = min(delay*2, maxReconnectDelay)
			continue
		}
		wssURL = newURL
		delay = initialReconnectDelay
	}
}

// runConnection dials the WebSocket, runs the handler, and forwards events.
// Returns true if the caller should reconnect.
func (s *SocketModeClient) runConnection(ctx context.Context, wssURL string, out chan<- SocketModeEvent) bool {
	conn, err := s.dialer.DialContext(ctx, wssURL)
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		s.log.Error("slack: failed to dial WebSocket",
			slog.String("url", wssURL),
			slog.Any("error", err))
		return true
	}

	handler, rawEvents := NewSocketModeHandler(conn, s.log)
	go handler.Run()

	shouldReconnect := false
	for {
		select {
		case <-ctx.Done():
			handler.Close()
			for range rawEvents {
			}
			return false

		case raw, ok := <-rawEvents:
			if !ok {
				return shouldReconnect || true
			}
			if raw.Type == SocketEventDisconnect {
				s.log.Info("slack: disconnect received, will reconnect",
					slog.String("envelope_id", raw.EnvelopeID))
				shouldReconnect = true
				continue
			}
			select {
			case out <- raw:
			case <-ctx.Done():
				handler.Close()
				for range rawEvents {
				}
				return false
			}
		}
	}
}
