package dingtalk

import (
	"context"
	"strconv"
	"strings"
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


	userID := event.SenderStaffID
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

// FormatMarkdown for DingTalk
func formatMarkdown(text string) string {
	// DingTalk markdown is quite picky about tables and line breaks.
	// We can add some common fixes here if needed.
	return text
}
