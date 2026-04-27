package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"


	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/payload"
)

const mapEntryTTL = 10 * time.Minute

// cardMapping stores a card ID with its creation timestamp for TTL cleanup.
type cardMapping struct {
	cardID    string
	createdAt time.Time
}

// DingtalkChannel implements the DingTalk channel.
type DingtalkChannel struct {
	*channels.BaseChannel
	cfg       config.DingtalkConfig
	client    *DingtalkClient
	stream    *StreamListener
	robotCode string
	mu            sync.RWMutex
	msgIDToCardID map[string]cardMapping // msgID -> cardMapping{cardID, createdAt}
	deliveredMsgs map[string]time.Time   // msgID -> insertion time
	cleanupDone   chan struct{}

	// Identity Adaptation (Org Center)
	orgCenter    *OrgCenterClient
	mappingStore *MappingStore
	idCache      sync.Map // staffID -> UserMapping
}

// New creates a new DingtalkChannel.
func New(cfg config.DingtalkConfig, msgBus *bus.MessageBus, pairingSvc store.PairingStore) (*DingtalkChannel, error) {
	base := channels.NewBaseChannel(channels.TypeDingtalk, msgBus, cfg.AllowFrom)
	base.ValidatePolicy(cfg.DMPolicy, cfg.GroupPolicy)
	
	historyLimit := cfg.HistoryLimit
	if historyLimit == 0 {
		historyLimit = channels.DefaultGroupHistoryLimit
	}
	
	requireMention := true
	if cfg.RequireMention != nil {
		requireMention = *cfg.RequireMention
	}

	appKey := strings.TrimSpace(cfg.AppKey)
	appSecret := strings.TrimSpace(cfg.AppSecret)
	robotCode := strings.TrimSpace(cfg.RobotCode)
	if robotCode == "" {
		robotCode = appKey
	}

	ch := &DingtalkChannel{
		BaseChannel:   base,
		cfg:           cfg,
		client:        NewDingtalkClient(appKey, appSecret),
		robotCode:     robotCode,
		msgIDToCardID: make(map[string]cardMapping),
		deliveredMsgs: make(map[string]time.Time),
		orgCenter:     NewOrgCenterClient(cfg.OrgCenter),
		mappingStore:  NewMappingStore(),
	}
	
	ch.SetPairingService(pairingSvc)
	ch.SetHistoryLimit(historyLimit)
	ch.SetRequireMention(requireMention)

	return ch, nil
}

// Start begins listening to DingTalk events via Stream Mode.
func (c *DingtalkChannel) Start(ctx context.Context) error {
	slog.Info("starting dingtalk channel (stream mode)", "name", c.Name())
	c.MarkStarting("Initializing Stream Mode")

	// Pre-load identity mappings from local file
	if c.cfg.OrgCenter.Enabled {
		if mappings, err := c.mappingStore.LoadAll(); err == nil {
			for k, v := range mappings {
				c.idCache.Store(k, v)
			}
			slog.Info("dingtalk: pre-loaded identity mappings", "count", len(mappings))
		}
	}

	// Probe connection
	if err := c.client.Validate(ctx); err != nil {
		slog.Error("dingtalk connection probe failed", "name", c.Name(), "error", err)
		c.MarkFailed("Connection failed", err.Error(), channels.ChannelFailureKindAuth, true)
		return fmt.Errorf("probe dingtalk connection: %w", err)
	}
	slog.Info("dingtalk connection verified", "name", c.Name())

	stream := NewStreamListener(c.cfg.AppKey, c.cfg.AppSecret, c.onMessage, func(connected bool, detail string) {
		if connected {
			slog.Info("dingtalk stream: connection restored", "name", c.Name(), "detail", detail)
			c.SetRunning(true)
			c.MarkHealthy(detail)
		} else {
			slog.Warn("dingtalk stream: connection degraded", "name", c.Name(), "detail", detail)
			c.MarkDegraded("Stream disconnected", detail, channels.ChannelFailureKindNetwork, true)
		}
	})
	if err := stream.Start(ctx); err != nil {
		return fmt.Errorf("start stream listener: %w", err)
	}
	
	c.stream = stream
	c.SetRunning(true)
	c.MarkHealthy("Connected (Stream Mode)")
	c.startCleanup()
	
	slog.Info("dingtalk channel started", "name", c.Name())
	return nil
}

// Stop shuts down the DingTalk channel.
func (c *DingtalkChannel) Stop(ctx context.Context) error {
	slog.Info("stopping dingtalk channel", "name", c.Name())
	c.SetRunning(false)
	c.MarkStopped("Stopped")

	if c.cleanupDone != nil {
		close(c.cleanupDone)
	}

	if c.stream != nil {
		if err := c.stream.Stop(); err != nil {
			slog.Error("failed to stop dingtalk stream", "error", err)
		}
	}

	return nil
}

// Send sends an outbound message to DingTalk.
func (c *DingtalkChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	msgID := msg.Metadata["msg_id"]

	// Determine the actual DingTalk target ID by reverse-lookup
	targetID := c.resolveStaffID(msg.ChatID)

	// 1. Check if this message has already been delivered or is being handled via streaming
	if msgID != "" {
		c.mu.RLock()
		_, delivered := c.deliveredMsgs[msgID]
		_, hasCard := c.msgIDToCardID[msgID]
		c.mu.RUnlock()

		if delivered || hasCard {
			slog.Info("dingtalk: message already delivered or handled via stream, skipping Send", "msgID", msgID, "delivered", delivered, "hasCard", hasCard)
			return nil
		}

		// 1.1 Fallback to msgID (DingTalk AIGC proxy automatic skeleton card)
		err := c.client.UpdateAICard(ctx, msgID, msg.Content, "✅ 环宝 已回答", "3", true)
		if err == nil {
			slog.Debug("dingtalk: successfully updated automatic skeleton card (msgID proxy)", "outTrackId", msgID)
			return nil
		}
	}

	// 2. If no card found/updated, proactively create an AI Card
	fallbackCardID := fmt.Sprintf("card_fb_%d", time.Now().UnixNano())
	if err := c.client.CreateAICard(ctx, c.robotCode, targetID, "", fallbackCardID, "✅ 环宝 已回答"); err == nil {
		if err := c.client.UpdateAICard(ctx, fallbackCardID, msg.Content, "✅ 环宝 已回答", "3", true); err == nil {
			slog.Debug("dingtalk: successfully created and populated proactive AI card", "outTrackId", fallbackCardID)
			return nil
		}
	} else {
		slog.Warn("dingtalk: failed to create proactive AI card", "error", err)
	}

	// 3. Fallback to session webhook (standard chat reply)
	if webhookURL, ok := msg.Metadata["session_webhook"]; ok && webhookURL != "" {
		err := c.replyToWebhook(ctx, webhookURL, msg.Content)
		if err == nil {
			slog.Debug("dingtalk: successfully replied via session webhook")
			return nil
		}
		slog.Warn("dingtalk: session webhook reply failed", "error", err)
	}

	// 4. Final fallback: standard raw text message API
	slog.Info("dingtalk: falling back to standard text message", "targetID", targetID)
	return c.client.SendMessage(ctx, c.robotCode, targetID, msg.Content)
}


func (c *DingtalkChannel) replyToWebhook(ctx context.Context, webhookURL string, content string) error {
	requestBody := map[string]interface{}{
		"msgtype": "text",
		"text": map[string]interface{}{
			"content": content,
		},
	}
	requestJsonBody, _ := json.Marshal(requestBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(requestJsonBody))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")

	httpClient := &http.Client{
		Transport: http.DefaultTransport,
		Timeout:   5 * time.Second,
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		responseJsonBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		return fmt.Errorf("webhook error %d: %s", resp.StatusCode, string(responseJsonBody))
	}
	return nil
}


// startCleanup launches a background goroutine that periodically removes expired
// entries from msgIDToCardID and deliveredMsgs to prevent memory leaks.
func (c *DingtalkChannel) startCleanup() {
	c.cleanupDone = make(chan struct{})
	ticker := time.NewTicker(5 * time.Minute)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.cleanExpiredEntries()
			case <-c.cleanupDone:
				return
			}
		}
	}()
}

// cleanExpiredEntries removes map entries older than mapEntryTTL.
func (c *DingtalkChannel) cleanExpiredEntries() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for id, entry := range c.msgIDToCardID {
		if now.Sub(entry.createdAt) > mapEntryTTL {
			delete(c.msgIDToCardID, id)
		}
	}
	for id, t := range c.deliveredMsgs {
		if now.Sub(t) > mapEntryTTL {
			delete(c.deliveredMsgs, id)
		}
	}

	slog.Debug("dingtalk: cleaned expired map entries",
		"remainingCards", len(c.msgIDToCardID),
		"remainingDelivered", len(c.deliveredMsgs))
}

// Type returns the channel type.
func (c *DingtalkChannel) Type() string {
	return channels.TypeDingtalk
}

func (c *DingtalkChannel) onMessage(ctx context.Context, df *payload.DataFrame) (*payload.DataFrameResponse, error) {
	slog.Info("dingtalk inbound event received", "headers", df.Headers, "data", df.Data)

	// Handle system events separately
	if df.GetTopic() == "SYSTEM" {
		slog.Info("dingtalk system event received", "data", df.Data)
		return payload.NewSuccessDataFrameResponse(), nil
	}

	event, err := parseInboundEvent(df)
	if err != nil {
		slog.Error("failed to parse dingtalk inbound event", "error", err)
		return nil, err
	}

	if err := c.processInbound(ctx, event); err != nil {
		slog.Error("failed to process dingtalk inbound message", "error", err)
		return nil, err
	}

	return payload.NewSuccessDataFrameResponse(), nil
}

// metaKeys returns sorted keys from metadata for diagnostic logging.
func metaKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
