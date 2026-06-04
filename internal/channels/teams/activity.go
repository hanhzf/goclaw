package teams

import (
	"time"
)

// TeamsActivity represents an incoming Microsoft Bot Framework Activity payload.
type TeamsActivity struct {
	Type         string            `json:"type"` // "message", "conversationUpdate", "invoke"
	ID           string            `json:"id"`
	Timestamp    time.Time         `json:"timestamp"`
	ServiceURL   string            `json:"serviceUrl"`
	ChannelID    string            `json:"channelId"` // "msteams"
	From         ChannelAccount    `json:"from"`
	Conversation ConversationRef   `json:"conversation"`
	Recipient    ChannelAccount    `json:"recipient"`
	Text         string            `json:"text"`
	TextFormat   string            `json:"textFormat,omitempty"`
	Attachments  []Attachment      `json:"attachments,omitempty"`
	Entities     []Entity          `json:"entities,omitempty"`
	ReplyToID    string            `json:"replyToId,omitempty"`
	ChannelData  TeamsChannelData  `json:"channelData,omitempty"`
	Locale       string            `json:"locale,omitempty"`
	Value        map[string]any    `json:"value,omitempty"` // Invoke card submit data
	Name         string            `json:"name,omitempty"`  // Invoke event name
}

type ChannelAccount struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AADObjectID string `json:"aadObjectId,omitempty"`
}

type ConversationRef struct {
	ID               string `json:"id"`
	IsGroup          *bool  `json:"isGroup,omitempty"`
	ConversationType string `json:"conversationType,omitempty"` // "personal" | "channel" | "groupChat"
	TenantID         string `json:"tenantId,omitempty"`
}

type Attachment struct {
	ContentType string `json:"contentType"`
	ContentURL  string `json:"contentUrl,omitempty"`
	Name        string `json:"name,omitempty"`
}

type Entity struct {
	Type      string         `json:"type"` // "mention"
	Text      string         `json:"text,omitempty"`
	Mentioned ChannelAccount `json:"mentioned,omitempty"`
}

type TeamsChannelData struct {
	Channel   *TeamsChannelDetail `json:"channel,omitempty"`
	Team      *TeamsTeamDetail    `json:"team,omitempty"`
	EventType string              `json:"eventType,omitempty"`
}

type TeamsChannelDetail struct {
	ID string `json:"id"`
}

type TeamsTeamDetail struct {
	ID string `json:"id"`
}
