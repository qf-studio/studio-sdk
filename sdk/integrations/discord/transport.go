package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsDialer abstracts WebSocket dialing for testing.
type wsDialer interface {
	DialContext(ctx context.Context, url string) (*websocket.Conn, error)
}

type defaultWsDialer struct{}

func (d defaultWsDialer) DialContext(ctx context.Context, url string) (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	return conn, err
}

// GatewayClient connects to Discord Gateway and handles event streaming.
// It handles IDENTIFY/RESUME, the heartbeat loop, and reconnection.
type GatewayClient struct {
	botToken      string
	intents       int
	apiClient     *Client
	dialer        wsDialer
	conn          *websocket.Conn
	sessionID     string
	botUserID     string
	seq           *int
	heartbeatTick *time.Ticker
	stopCh        chan struct{}
	mu            sync.Mutex
	closeOnce     sync.Once
	log           *slog.Logger
}

// NewGatewayClient creates a new Discord Gateway client.
// logger may be nil to use slog.Default().
func NewGatewayClient(botToken string, intents int, logger *slog.Logger) *GatewayClient {
	if logger == nil {
		logger = slog.Default()
	}
	return &GatewayClient{
		botToken:  botToken,
		intents:   intents,
		apiClient: NewClient(botToken),
		dialer:    defaultWsDialer{},
		stopCh:    make(chan struct{}),
		log:       logger,
	}
}

// BotUserID returns the bot's user ID extracted from the READY event.
// May be empty if READY has not yet been received.
func (g *GatewayClient) BotUserID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.botUserID
}

// Connect establishes a WebSocket connection and performs IDENTIFY.
// The caller must hold no locks; Connect acquires g.mu internally.
func (g *GatewayClient) Connect(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	gatewayURL, err := g.apiClient.GetGatewayURL(ctx)
	if err != nil {
		return fmt.Errorf("get gateway url: %w", err)
	}

	conn, err := g.dialer.DialContext(ctx, gatewayURL+"?v=10&encoding=json")
	if err != nil {
		return fmt.Errorf("dial gateway: %w", err)
	}
	g.conn = conn
	g.log.Info("discord: connected to gateway")

	if err := g.handleHello(); err != nil {
		_ = g.conn.Close()
		g.conn = nil
		return fmt.Errorf("handle hello: %w", err)
	}
	return nil
}

// handleHello receives the HELLO opcode and sends IDENTIFY.
// Must be called with g.mu held.
func (g *GatewayClient) handleHello() error {
	_ = g.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer func() { _ = g.conn.SetReadDeadline(time.Time{}) }()

	var event GatewayEvent
	if err := g.conn.ReadJSON(&event); err != nil {
		return fmt.Errorf("read hello: %w", err)
	}
	if event.Op != OpcodeHello {
		return fmt.Errorf("expected opcode %d (HELLO), got %d", OpcodeHello, event.Op)
	}

	var hello Hello
	if err := json.Unmarshal(event.D, &hello); err != nil {
		return fmt.Errorf("parse hello: %w", err)
	}

	identify := Identify{
		Op: OpcodeIdentify,
		D: IdentifyData{
			Token:   g.botToken,
			Intents: g.intents,
			Properties: map[string]string{
				"os":      "linux",
				"browser": "studio-sdk",
				"device":  "studio-sdk",
			},
		},
	}
	if err := g.conn.WriteJSON(identify); err != nil {
		return fmt.Errorf("send identify: %w", err)
	}

	g.log.Info("discord: sent IDENTIFY", slog.Int("heartbeat_interval_ms", hello.HeartbeatInterval))
	g.heartbeatTick = time.NewTicker(time.Duration(hello.HeartbeatInterval) * time.Millisecond)
	go g.heartbeatLoop()
	return nil
}

func (g *GatewayClient) heartbeatLoop() {
	defer g.heartbeatTick.Stop()
	for {
		select {
		case <-g.stopCh:
			return
		case <-g.heartbeatTick.C:
			g.mu.Lock()
			if g.conn == nil {
				g.mu.Unlock()
				return
			}
			hb := Heartbeat{Op: OpcodeHeartbeat, D: g.seq}
			_ = g.conn.WriteJSON(hb)
			g.mu.Unlock()
		}
	}
}

// Resume sends a RESUME opcode to restore an existing session.
func (g *GatewayClient) Resume() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.conn == nil || g.sessionID == "" || g.seq == nil {
		return fmt.Errorf("discord: cannot resume: missing session state")
	}
	resume := Resume{
		Op: OpcodeResume,
		D:  ResumeData{Token: g.botToken, SessionID: g.sessionID, Seq: *g.seq},
	}
	if err := g.conn.WriteJSON(resume); err != nil {
		return fmt.Errorf("send resume: %w", err)
	}
	g.log.Info("discord: sent RESUME", slog.String("session_id", g.sessionID))
	return nil
}

// listen reads events from the current connection until it closes or ctx is done.
// Returns the channel of events; caller drains it. Must NOT hold g.mu.
func (g *GatewayClient) listen(ctx context.Context) <-chan GatewayEvent {
	out := make(chan GatewayEvent, 64)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case <-g.stopCh:
				return
			default:
			}

			g.mu.Lock()
			c := g.conn
			g.mu.Unlock()
			if c == nil {
				g.log.Warn("discord: listen: connection is nil, ending read loop")
				return
			}

			var event GatewayEvent
			if err := c.ReadJSON(&event); err != nil {
				code := extractCloseCode(err)
				if code != 0 {
					if isFatalCloseCode(code) {
						g.log.Error("discord: fatal close code", slog.Int("code", code))
					} else if isResumableCloseCode(code) {
						g.log.Warn("discord: resumable close code", slog.Int("code", code))
					} else {
						g.log.Warn("discord: gateway closed", slog.Int("code", code))
					}
				} else {
					g.log.Warn("discord: read error", slog.Any("error", err))
				}
				return
			}

			// Track sequence for RESUME.
			if event.S != nil {
				g.mu.Lock()
				g.seq = event.S
				g.mu.Unlock()
			}

			// Extract session ID and bot user ID from READY.
			if event.T != nil && *event.T == "READY" {
				g.handleReady(event.D)
			}

			select {
			case out <- event:
			case <-ctx.Done():
				return
			case <-g.stopCh:
				return
			}
		}
	}()
	return out
}

func (g *GatewayClient) handleReady(d json.RawMessage) {
	var ready struct {
		SessionID string `json:"session_id"`
		User      struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(d, &ready); err != nil {
		return
	}
	g.mu.Lock()
	g.sessionID = ready.SessionID
	if ready.User.ID != "" {
		g.botUserID = ready.User.ID
	}
	g.mu.Unlock()
	g.log.Info("discord: READY",
		slog.String("session_id", ready.SessionID),
		slog.String("bot_user_id", ready.User.ID))
}

// StartListening connects, then streams GatewayEvents with automatic reconnection.
// Returns a channel that closes when ctx is cancelled or a fatal error occurs.
func (g *GatewayClient) StartListening(ctx context.Context) (<-chan GatewayEvent, error) {
	if err := g.Connect(ctx); err != nil {
		return nil, fmt.Errorf("discord: initial connect: %w", err)
	}

	out := make(chan GatewayEvent, 64)
	go g.reconnectLoop(ctx, out)
	return out, nil
}

func (g *GatewayClient) reconnectLoop(ctx context.Context, out chan<- GatewayEvent) {
	defer close(out)

	const (
		minBackoff = 1 * time.Second
		maxBackoff = 60 * time.Second
	)
	backoff := minBackoff

	for {
		events := g.listen(ctx)

		for evt := range events {
			backoff = minBackoff
			select {
			case out <- evt:
			case <-ctx.Done():
				return
			case <-g.stopCh:
				return
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-g.stopCh:
			return
		default:
		}

		g.mu.Lock()
		canResume := g.sessionID != "" && g.seq != nil
		if g.conn != nil {
			_ = g.conn.Close()
			g.conn = nil
		}
		if g.heartbeatTick != nil {
			g.heartbeatTick.Stop()
		}
		g.mu.Unlock()

		g.log.Info("discord: reconnecting", slog.Duration("backoff", backoff), slog.Bool("resume", canResume))
		select {
		case <-ctx.Done():
			return
		case <-g.stopCh:
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}

		if err := g.Connect(ctx); err != nil {
			g.log.Error("discord: reconnect failed", slog.Any("error", err))
			continue
		}
		if canResume {
			if err := g.Resume(); err != nil {
				g.log.Warn("discord: resume failed, will re-identify", slog.Any("error", err))
				g.mu.Lock()
				g.sessionID = ""
				g.seq = nil
				g.mu.Unlock()
			}
		}
	}
}

// Close closes the gateway connection. Safe to call multiple times.
func (g *GatewayClient) Close() error {
	var closeErr error
	g.closeOnce.Do(func() {
		close(g.stopCh)
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.heartbeatTick != nil {
			g.heartbeatTick.Stop()
		}
		if g.conn != nil {
			closeErr = g.conn.Close()
		}
	})
	return closeErr
}

func isResumableCloseCode(code int) bool {
	return code >= CloseCodeUnknownError && code <= CloseCodeSessionTimeout
}

func isFatalCloseCode(code int) bool {
	return code == CloseCodeAuthenticationFailed || code == CloseCodeInvalidToken
}

func extractCloseCode(err error) int {
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) {
		return closeErr.Code
	}
	return 0
}
