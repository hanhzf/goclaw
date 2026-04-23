package dingtalk

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/handler"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"
)

// Health monitor configuration
const (
	// healthCheckInterval is how often the health monitor checks for activity.
	healthCheckInterval = 30 * time.Second
	// healthTimeout is the max duration without any message before we consider the connection dead.
	// The DingTalk server sends system pings periodically, so a healthy connection should
	// always have activity within this window.
	healthTimeout = 90 * time.Second
	// baseBackoff is the initial reconnect delay.
	baseBackoff = 3 * time.Second
	// maxBackoff is the maximum reconnect delay.
	maxBackoff = 30 * time.Second
)

// OnStatusChange is called when the stream connection status changes.
// connected=true means the stream is healthy; connected=false means disconnected/reconnecting.
type OnStatusChange func(connected bool, detail string)

// StreamListener manages the DingTalk Stream connection with application-layer
// health monitoring and automatic reconnection.
//
// Architecture:
//
//	SDK Client (底层)          HealthMonitor (应用层)
//	├─ 120s Ping/Pong          ├─ 30s 检测间隔
//	├─ AutoReconnect (兜底)    ├─ 90s 超时 → 触发重建
//	└─ processLoop             └─ 指数退避重连
type StreamListener struct {
	appKey         string
	appSecret      string
	userHandler    handler.IFrameHandler // original handler from DingtalkChannel
	onStatusChange OnStatusChange

	mu               sync.Mutex
	sdkClient        *client.StreamClient
	lastActivityTime time.Time
	reconnectCount   int
	stopCh           chan struct{}
	stopped          bool
}

// NewStreamListener creates a new stream listener with optional status change callback.
func NewStreamListener(appKey, appSecret string, h handler.IFrameHandler, onStatus OnStatusChange) *StreamListener {
	return &StreamListener{
		appKey:         appKey,
		appSecret:      appSecret,
		userHandler:    h,
		onStatusChange: onStatus,
	}
}

// Start starts the DingTalk Stream WebSocket connection and health monitor.
func (s *StreamListener) Start(ctx context.Context) error {
	slog.Info("dingtalk stream: connecting...")

	s.mu.Lock()
	s.stopCh = make(chan struct{})
	s.stopped = false
	s.lastActivityTime = time.Now()
	s.reconnectCount = 0
	s.mu.Unlock()

	if err := s.createAndStartClient(ctx); err != nil {
		return err
	}

	// Start application-layer health monitor
	go s.healthMonitorLoop()

	slog.Info("dingtalk stream: connected and health monitor started")
	return nil
}

// Stop stops the stream listener and health monitor.
func (s *StreamListener) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return nil
	}
	s.stopped = true

	// Signal health monitor to stop
	if s.stopCh != nil {
		close(s.stopCh)
	}

	// Close SDK client
	if s.sdkClient != nil {
		s.sdkClient.Close()
		s.sdkClient = nil
	}

	slog.Info("dingtalk stream: stopped")
	return nil
}

// createAndStartClient creates a new SDK StreamClient and starts it.
// Must NOT hold s.mu when calling this (SDK Start is blocking during connect).
func (s *StreamListener) createAndStartClient(ctx context.Context) error {
	cli := client.NewStreamClient(
		client.WithAppCredential(client.NewAppCredentialConfig(s.appKey, s.appSecret)),
		client.WithSubscription("CALLBACK", "/v1.0/im/bot/messages/get", s.wrappedHandler),
		client.WithSubscription("EVENT", "/v1.0/im/robot/message/receive", s.wrappedHandler),
		client.WithSubscription("SYSTEM", "*", s.wrappedHandler),
	)

	if err := cli.Start(ctx); err != nil {
		return fmt.Errorf("failed to start dingtalk stream: %w", err)
	}

	s.mu.Lock()
	s.sdkClient = cli
	s.lastActivityTime = time.Now()
	s.mu.Unlock()

	return nil
}

// wrappedHandler intercepts all incoming messages to update lastActivityTime,
// then delegates to the user-provided handler.
func (s *StreamListener) wrappedHandler(ctx context.Context, df *payload.DataFrame) (*payload.DataFrameResponse, error) {
	// Update activity timestamp on every message (including SYSTEM pings)
	s.mu.Lock()
	s.lastActivityTime = time.Now()
	s.mu.Unlock()

	// Delegate to the original handler
	return s.userHandler(ctx, df)
}

// healthMonitorLoop periodically checks connection liveness.
// If no activity is seen within healthTimeout, it triggers a reconnect.
func (s *StreamListener) healthMonitorLoop() {
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	slog.Info("dingtalk stream: health monitor started",
		"checkInterval", healthCheckInterval,
		"timeout", healthTimeout)

	for {
		select {
		case <-s.stopCh:
			slog.Info("dingtalk stream: health monitor stopped")
			return
		case <-ticker.C:
			s.checkHealth()
		}
	}
}

// checkHealth evaluates if the connection is still alive.
func (s *StreamListener) checkHealth() {
	s.mu.Lock()
	elapsed := time.Since(s.lastActivityTime)
	stopped := s.stopped
	s.mu.Unlock()

	if stopped {
		return
	}

	if elapsed <= healthTimeout {
		// Connection is healthy
		slog.Debug("dingtalk stream: health check OK",
			"elapsed", elapsed.Round(time.Second),
			"timeout", healthTimeout)
		return
	}

	// Connection appears dead
	slog.Warn("dingtalk stream: health check FAILED, connection may be dead",
		"elapsed", elapsed.Round(time.Second),
		"timeout", healthTimeout)

	// Notify channel that connection is degraded
	if s.onStatusChange != nil {
		s.onStatusChange(false, fmt.Sprintf("No activity for %s, reconnecting...", elapsed.Round(time.Second)))
	}

	// Trigger reconnect
	s.reconnect()
}

// reconnect closes the old client and creates a new one with exponential backoff.
func (s *StreamListener) reconnect() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.reconnectCount++
	attempt := s.reconnectCount
	s.mu.Unlock()

	slog.Info("dingtalk stream: starting reconnect", "attempt", attempt)

	// Close old client
	s.mu.Lock()
	if s.sdkClient != nil {
		s.sdkClient.Close()
		s.sdkClient = nil
	}
	s.mu.Unlock()

	// Retry loop with exponential backoff
	for {
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return
		}
		currentAttempt := s.reconnectCount
		s.mu.Unlock()

		// Calculate backoff delay
		delay := calculateBackoff(currentAttempt)
		slog.Info("dingtalk stream: waiting before reconnect",
			"delay", delay.Round(time.Millisecond),
			"attempt", currentAttempt)

		// Wait with cancellation support
		select {
		case <-s.stopCh:
			slog.Info("dingtalk stream: reconnect cancelled (stopped)")
			return
		case <-time.After(delay):
		}

		// Check again if stopped during wait
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()

		// Attempt to create and start a new client
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := s.createAndStartClient(ctx)
		cancel()

		if err != nil {
			s.mu.Lock()
			s.reconnectCount++
			s.mu.Unlock()

			slog.Error("dingtalk stream: reconnect failed",
				"attempt", currentAttempt,
				"error", err)
			continue
		}

		// Reconnect succeeded
		s.mu.Lock()
		s.reconnectCount = 0
		s.mu.Unlock()

		slog.Info("dingtalk stream: reconnect succeeded", "afterAttempts", currentAttempt)

		// Notify channel that connection is restored
		if s.onStatusChange != nil {
			s.onStatusChange(true, "Reconnected (Stream Mode)")
		}

		return
	}
}

// calculateBackoff returns the exponential backoff delay for a given attempt number.
// Formula: min(baseBackoff * 2^attempt + jitter, maxBackoff)
func calculateBackoff(attempt int) time.Duration {
	exp := math.Pow(2, float64(attempt-1))
	delay := time.Duration(float64(baseBackoff) * exp)
	if delay > maxBackoff {
		delay = maxBackoff
	}
	return delay
}

// Data models for DingTalk events
type InboundEvent struct {
	MsgType           string `json:"msgtype"`
	MsgID             string `json:"msgId"`
	CreateAt          int64  `json:"createAt"`
	ConversationType  string `json:"conversationType"` // "1" for OTO, "2" for Group
	ConversationID    string `json:"conversationId"`
	ChatTitle         string `json:"chatTitle"`
	SenderID          string `json:"senderId"`
	SenderNick        string `json:"senderNick"`
	SenderStaffID     string `json:"senderStaffId"`
	SessionWebhook    string `json:"sessionWebhook"`
	SessionWebhookExpiredTime int64 `json:"sessionWebhookExpiredTime"`
	Text              struct {

		Content string `json:"content"`
	} `json:"text"`
	// Add other rich media fields as needed
}

func parseInboundEvent(df *payload.DataFrame) (*InboundEvent, error) {
	var event InboundEvent
	if err := json.Unmarshal([]byte(df.Data), &event); err != nil {
		return nil, err
	}
	return &event, nil
}
