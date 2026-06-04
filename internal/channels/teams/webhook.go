package teams

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// serveWebhook handles incoming HTTP requests from Teams Bot Framework.
func (c *Channel) serveWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ctx = store.WithTenantID(ctx, c.TenantID())

	// 1. Authenticate Request
	claims, err := ValidateInboundRequest(ctx, r, c.cfg.AppID, c.jwksCache)
	if err != nil {
		slog.Warn("teams: inbound auth failed", "channel", c.Name(), "error", err)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 2. Decode Activity
	var activity TeamsActivity
	if err := json.NewDecoder(r.Body).Decode(&activity); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// 3. Immediately ack 200 OK (Teams expects response within 15s)
	w.WriteHeader(http.StatusOK)

	// 4. Process asynchronously to avoid blocking HTTP worker
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("teams: panic in processActivity", "panic", r)
			}
		}()
		c.processActivity(ctx, &activity, claims)
	}()
}

func (c *Channel) processActivity(ctx context.Context, activity *TeamsActivity, claims *BotClaims) {
	if activity.Type != "message" {
		// Ignore conversationUpdate/invoke for now (can be added as P1 features)
		slog.Debug("teams: activity type ignored", "type", activity.Type)
		return
	}

	if activity.ID == "" {
		return
	}

	// Message Deduplication
	if c.dedup.Seen(activity.ID) {
		slog.Debug("teams: duplicate activity dropped", "id", activity.ID)
		return
	}
	c.dedup.Mark(activity.ID)

	// Extract Sender details
	senderID := activity.From.AADObjectID
	if senderID == "" {
		senderID = activity.From.ID
	}
	if senderID == "" {
		slog.Warn("teams: empty sender ID")
		return
	}

	displayName := activity.From.Name
	if displayName == "" {
		displayName = senderID
	}

	// Determine PeerKind
	peerKind := "group"
	if activity.Conversation.ConversationType == "personal" {
		peerKind = "direct"
	}

	// Policy checks
	dmPolicy := c.cfg.DMPolicy
	if dmPolicy == "" {
		dmPolicy = "pairing"
	}
	groupPolicy := c.cfg.GroupPolicy
	if groupPolicy == "" {
		groupPolicy = "pairing"
	}

	if !c.CheckPolicy(peerKind, dmPolicy, groupPolicy, senderID) {
		slog.Debug("teams: message rejected by policy", "sender", senderID, "peer_kind", peerKind)
		return
	}

	// Resolve botID from config or recipient ID
	botID := c.cfg.AppID
	if activity.Recipient.ID != "" && botID == "" {
		botID = activity.Recipient.ID
	}

	// Strip mentions & clean content
	content := stripBotMention(activity.Text, activity.Entities, botID)
	content = resolveUserMentions(content, activity.Entities)
	content = strings.TrimSpace(content)

	if content == "" {
		return
	}

	localKey := activity.Conversation.ID
	isDM := peerKind == "direct"

	// Mention checking for groups
	if !isDM && c.RequireMention() {
		mentioned := isBotMentioned(activity.Entities, botID)
		if !mentioned {
			// Record to history without reacting
			c.GroupHistory().Record(localKey, channels.HistoryEntry{
				Sender:    displayName,
				SenderID:  senderID,
				Body:      content,
				Timestamp: time.Now(),
				MessageID: activity.ID,
			}, c.HistoryLimit())

			slog.Debug("teams: group message recorded (no mention)", "chat_id", localKey, "user", displayName)
			return
		}
	}

	slog.Debug("teams: message received", "sender_id", senderID, "chat_id", localKey, "content", content)

	// Send "Thinking..." placeholder
	placeholderID, err := c.sender.PostActivity(ctx, activity.ServiceURL, activity.Conversation.ID, activity.ID, "Thinking...", nil)
	if err == nil {
		c.placeholders.Store(localKey, placeholderID)
	} else {
		slog.Warn("teams: failed to post placeholder message", "error", err)
	}

	// Build final content with group history
	finalContent := content
	if !isDM {
		annotated := fmt.Sprintf("[From: %s]\n%s", displayName, content)
		if c.HistoryLimit() > 0 {
			finalContent = c.GroupHistory().BuildContext(localKey, annotated, c.HistoryLimit())
		} else {
			finalContent = annotated
		}
	}

	metadata := map[string]string{
		"activity_id":     activity.ID,
		"service_url":     activity.ServiceURL,
		"conversation_id": activity.Conversation.ID,
		"user_id":         senderID,
		"username":        displayName,
		"display_name":    channels.SanitizeDisplayName(displayName),
		"is_dm":           fmt.Sprintf("%t", isDM),
		"local_key":       localKey,
		"placeholder_key": localKey,
	}

	c.HandleMessage(senderID, activity.Conversation.ID, finalContent, nil, metadata, peerKind)

	if !isDM {
		c.GroupHistory().Clear(localKey)
	}
}

func stripBotMention(text string, entities []Entity, botID string) string {
	for _, e := range entities {
		if e.Type == "mention" && (e.Mentioned.ID == botID || strings.Contains(e.Mentioned.ID, botID) || e.Mentioned.Name == botID) {
			if e.Text != "" {
				text = strings.ReplaceAll(text, e.Text, "")
			}
		}
	}
	return text
}

func resolveUserMentions(text string, entities []Entity) string {
	for _, e := range entities {
		if e.Type == "mention" && e.Mentioned.Name != "" {
			if e.Text != "" {
				text = strings.ReplaceAll(text, e.Text, "@"+e.Mentioned.Name)
			}
		}
	}
	return text
}

func isBotMentioned(entities []Entity, botID string) bool {
	for _, e := range entities {
		if e.Type == "mention" && (e.Mentioned.ID == botID || strings.Contains(e.Mentioned.ID, botID) || e.Mentioned.Name == botID) {
			return true
		}
	}
	return false
}
