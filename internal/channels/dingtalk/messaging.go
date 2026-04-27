package dingtalk

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"
)


// processInbound converts a DingTalk event into a bus.InboundMessage.
func (c *DingtalkChannel) processInbound(ctx context.Context, event *InboundEvent) error {
	if event.Text.Content == "" {
		// Possibly a non-text message or empty
		return nil
	}

	content := strings.TrimSpace(event.Text.Content)
	peerKind := "direct"
	if event.ConversationType == "2" {
		peerKind = "group"
	}

	metadata := map[string]string{
		"sender_nick":     event.SenderNick,
		"chat_title":      event.ChatTitle,
		"msg_id":          event.MsgID,
		"message_id":      event.MsgID, // GoClaw standard key used by RegisterRun → RunContext.MessageID
		"conversation_id": event.ConversationID,
	}

	// Trigger "Thinking" emotion sticker (fire-and-forget)
	go func() {
		ctx := context.Background()
		_ = c.client.AddEmotionReply(ctx, c.robotCode, event.MsgID, event.ConversationID)
	}()
	if event.SessionWebhook != "" {
		metadata["session_webhook"] = event.SessionWebhook
	}
	if event.SessionWebhookExpiredTime > 0 {
		metadata["session_webhook_expired_time"] = strconv.FormatInt(event.SessionWebhookExpiredTime, 10)
	}


	userID := c.resolvePersonCode(ctx, event.SenderStaffID)
	if userID == "" {
		userID = event.SenderID
	}

	var chatID string
	if peerKind == "group" {
		chatID = event.ConversationID
	} else {
		chatID = userID
	}

	// Use BaseChannel's HandleMessage for standard GoClaw processing.
	// This handles policy, routing, and persistence.
	c.HandleMessage(
		userID,
		chatID,
		content,
		nil, // media
		metadata,
		peerKind,
	)

	return nil

}

// resolvePersonCode translates a DingTalk staffID into a business person_code.
// It uses a memory cache, falls back to DingTalk API + OrgCenter API,
// and persists results to a local JSON file for debugging.
func (c *DingtalkChannel) resolvePersonCode(ctx context.Context, staffID string) string {
	if staffID == "" || !c.cfg.OrgCenter.Enabled {
		return staffID
	}

	// 1. Check Memory Cache
	if val, ok := c.idCache.Load(staffID); ok {
		mapping := val.(UserMapping)
		ttl := time.Duration(c.cfg.OrgCenter.TTLHours) * time.Hour
		if ttl == 0 {
			ttl = 100 * time.Hour
		}
		if time.Since(mapping.UpdatedAt) < ttl {
			return mapping.PersonCode
		}
	}

	// 2. Resolve via APIs (Real or Mock)
	var mobile string
	var personCode string
	var err error

	if c.cfg.OrgCenter.Mode == "mock" {
		personCode, err = c.orgCenter.LookupPersonCode(ctx, "")
		mobile = "MOCK_MOBILE"
	} else {
		// Get Mobile from DingTalk
		userInfo, dtErr := c.client.GetUserInfo(ctx, staffID)
		if dtErr != nil {
			slog.Error("dingtalk: failed to get user info for mapping", "staffID", staffID, "error", dtErr)
			return staffID // Fallback
		}
		if m, ok := userInfo["mobile"].(string); ok {
			mobile = m
		} else {
			slog.Warn("dingtalk: user info has no mobile", "staffID", staffID)
			return staffID
		}

		// Get personCode from Org Center
		personCode, err = c.orgCenter.LookupPersonCode(ctx, mobile)
	}

	if err != nil {
		slog.Error("dingtalk: identity translation failed", "staffID", staffID, "mobile", mobile, "error", err)
		return staffID // Fallback
	}

	// 3. Update Cache & Local Persistence
	mapping := UserMapping{
		StaffID:    staffID,
		Mobile:     mobile,
		PersonCode: personCode,
		UpdatedAt:  time.Now(),
	}
	c.idCache.Store(staffID, mapping)
	if err := c.mappingStore.Save(mapping); err != nil {
		slog.Warn("dingtalk: failed to save identity mapping to file", "error", err)
	}

	slog.Info("dingtalk: identity translated", "staffID", staffID, "personCode", personCode, "mode", c.cfg.OrgCenter.Mode)
	return personCode
}

// FormatMarkdown for DingTalk
func formatMarkdown(text string) string {
	// DingTalk markdown is quite picky about tables and line breaks.
	// We can add some common fixes here if needed.
	return text
}
