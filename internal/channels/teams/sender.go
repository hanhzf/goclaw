package teams

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
)

const (
	maxTeamsMessageLen = 15000 // Teams markdown length limit is large (up to 28KB), 15000 is safe
)

// TeamsSender handles posting outgoing activities to Microsoft Bot Connector API.
type TeamsSender struct {
	tokenProvider *BotTokenProvider
	httpClient    *http.Client
	placeholders  *sync.Map
}

// NewTeamsSender creates a new TeamsSender.
func NewTeamsSender(tokenProvider *BotTokenProvider, placeholders *sync.Map) *TeamsSender {
	return &TeamsSender{
		tokenProvider: tokenProvider,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
		placeholders:  placeholders,
	}
}

// Send delivers an outbound message to Microsoft Teams.
func (s *TeamsSender) Send(ctx context.Context, msg bus.OutboundMessage) error {
	conversationID := msg.ChatID
	if conversationID == "" {
		return fmt.Errorf("teams: empty chat ID for send")
	}

	serviceURL := msg.Metadata["service_url"]
	if serviceURL == "" {
		return fmt.Errorf("teams: missing service_url in metadata")
	}

	placeholderKey := conversationID
	if pk := msg.Metadata["placeholder_key"]; pk != "" {
		placeholderKey = pk
	}

	// Placeholder update (LLM retry notification)
	if msg.Metadata["placeholder_update"] == "true" {
		if pTS, ok := s.placeholders.Load(placeholderKey); ok {
			ts := pTS.(string)
			_ = s.UpdateActivity(ctx, serviceURL, conversationID, ts, msg.Content)
		}
		return nil
	}

	content := FormatForTeams(msg.Content)

	// NO_REPLY: delete placeholder, return
	if content == "" {
		if pTS, ok := s.placeholders.Load(placeholderKey); ok {
			s.placeholders.Delete(placeholderKey)
			ts := pTS.(string)
			_ = s.DeleteActivity(ctx, serviceURL, conversationID, ts)
		}
		return nil
	}

	// Edit placeholder with first chunk if it exists
	if pTS, ok := s.placeholders.Load(placeholderKey); ok {
		s.placeholders.Delete(placeholderKey)
		ts := pTS.(string)

		chunks := channels.ChunkMarkdown(content, maxTeamsMessageLen)
		if len(chunks) > 0 {
			err := s.UpdateActivity(ctx, serviceURL, conversationID, ts, chunks[0])
			if err == nil {
				// Send remaining chunks as new messages
				for i := 1; i < len(chunks); i++ {
					_, _ = s.PostActivity(ctx, serviceURL, conversationID, msg.Metadata["activity_id"], chunks[i], nil)
				}
				return nil
			}
			slog.Warn("teams placeholder edit failed, posting new message", "conversation_id", conversationID, "error", err)
		}
	}

	// Normal path: Post new message (with optional attachments or split chunks)
	chunks := channels.ChunkMarkdown(content, maxTeamsMessageLen)
	if len(chunks) == 0 {
		return nil
	}

	// Post first chunk (and include replyToId from original user trigger)
	replyToID := msg.Metadata["activity_id"]
	var card []byte
	if cardJSON, ok := msg.Metadata["card_json"]; ok && cardJSON != "" {
		card = []byte(cardJSON)
	}

	_, err := s.PostActivity(ctx, serviceURL, conversationID, replyToID, chunks[0], card)
	if err != nil {
		return err
	}

	// Post subsequent chunks
	for i := 1; i < len(chunks); i++ {
		_, _ = s.PostActivity(ctx, serviceURL, conversationID, replyToID, chunks[i], nil)
	}

	return nil
}

type botActivityPayload struct {
	Type        string        `json:"type"`
	Text        string        `json:"text,omitempty"`
	TextFormat  string        `json:"textFormat,omitempty"`
	ReplyToID   string        `json:"replyToId,omitempty"`
	Attachments []cardAtmtObj `json:"attachments,omitempty"`
}

type cardAtmtObj struct {
	ContentType string `json:"contentType"`
	Content     any    `json:"content"`
}

// PostActivity posts a new activity to Teams and returns the created activity ID.
func (s *TeamsSender) PostActivity(ctx context.Context, serviceURL, conversationID, replyToID, text string, card []byte) (string, error) {
	urlStr := fmt.Sprintf("%s/v3/conversations/%s/activities", strings.TrimSuffix(serviceURL, "/"), conversationID)

	payload := botActivityPayload{
		Type:       "message",
		Text:       text,
		TextFormat: "markdown",
		ReplyToID:  replyToID,
	}

	if len(card) > 0 {
		var cardObj any
		if err := json.Unmarshal(card, &cardObj); err == nil {
			payload.Attachments = []cardAtmtObj{
				{
					ContentType: "application/vnd.microsoft.card.adaptive",
					Content:     cardObj,
				},
			}
			// For card attachments, clear direct Text if desired (or keep for fallback)
			payload.Text = ""
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	respBytes, err := s.doRequest(ctx, http.MethodPost, urlStr, body)
	if err != nil {
		return "", err
	}

	var res struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBytes, &res); err != nil {
		return "", nil
	}
	return res.ID, nil
}

// UpdateActivity updates an existing activity (edits a message).
func (s *TeamsSender) UpdateActivity(ctx context.Context, serviceURL, conversationID, activityID, text string) error {
	urlStr := fmt.Sprintf("%s/v3/conversations/%s/activities/%s", strings.TrimSuffix(serviceURL, "/"), conversationID, activityID)

	payload := botActivityPayload{
		Type:       "message",
		Text:       text,
		TextFormat: "markdown",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	_, err = s.doRequest(ctx, http.MethodPut, urlStr, body)
	return err
}

// DeleteActivity removes a posted activity.
func (s *TeamsSender) DeleteActivity(ctx context.Context, serviceURL, conversationID, activityID string) error {
	urlStr := fmt.Sprintf("%s/v3/conversations/%s/activities/%s", strings.TrimSuffix(serviceURL, "/"), conversationID, activityID)
	_, err := s.doRequest(ctx, http.MethodDelete, urlStr, nil)
	return err
}

func (s *TeamsSender) doRequest(ctx context.Context, method, urlStr string, body []byte) ([]byte, error) {
	token, err := s.tokenProvider.GetToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("teams: get token for send: %w", err)
	}

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, urlStr, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("teams: send request returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}
