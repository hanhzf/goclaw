package dingtalk

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

const (
	defaultStreamThrottle = 1000 * time.Millisecond
)

// CardStream manages a DingTalk AI Card streaming session.
//
// Lazy-creation design:
//   - When isLazy=true (firstStream), no card is created upfront.
//   - The card is only created on the FIRST non-reasoning flush() call.
//   - If Stop() is called before any answer content arrives (pure thinking phase),
//     no card is ever created — zero empty bubbles.
//   - When isLazy=false (answer phase), the card is pre-created immediately.
type CardStream struct {
	parent         *DingtalkChannel
	client         *DingtalkClient
	chatID         string
	outTrackID     string
	msgID          string
	conversationID string
	lastText       string
	throttle       time.Duration
	lastEdit       time.Time
	mu             sync.Mutex
	stopped        bool
	pending        string
	// Lazy creation fields
	isLazy         bool // true = don't create card until first answer content
	cardCreated    bool // true = CreateAICard has been called
	isAnswerStream bool // true = mark message as delivered on stop
}

func NewCardStream(parent *DingtalkChannel, client *DingtalkClient, chatID string, outTrackID string, msgID string, conversationID string, lazy bool, isAnswer bool) *CardStream {
	cs := &CardStream{
		parent:         parent,
		client:         client,
		chatID:         chatID,
		outTrackID:     outTrackID,
		msgID:          msgID,
		conversationID: conversationID,
		throttle:       defaultStreamThrottle,
		isLazy:         lazy,
		cardCreated:    !lazy, // if not lazy, card is already created
		isAnswerStream: isAnswer,
	}
	return cs
}

func (s *CardStream) Update(ctx context.Context, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopped {
		return
	}

	if text == s.lastText {
		return
	}

	s.pending = text

	if time.Since(s.lastEdit) < s.throttle {
		return
	}

	s.flush(ctx)
}

// flush pushes the current pending content to the DingTalk card.
// For lazy streams, it skips reasoning content and only creates the card
// when actual answer content arrives.
func (s *CardStream) flush(ctx context.Context) {
	if s.pending == "" || s.pending == s.lastText {
		return
	}

	text := s.pending

	// Check if this is reasoning-phase content (from formatReasoningPreview)
	isReasoning := strings.HasPrefix(text, "_Reasoning:_\n")

	if isReasoning {
		// Reasoning content: for lazy streams, skip entirely (no card yet, no empty update).
		// For non-lazy (answer) streams, this shouldn't happen, but skip anyway.
		slog.Debug("dingtalk: skipping reasoning content in flush", "lazy", s.isLazy)
		s.lastText = s.pending // advance lastText to avoid re-processing
		return
	}

	// Strip any leftover reasoning markers
	text = strings.TrimPrefix(text, "_Reasoning:_\n")
	if text == "" {
		return
	}

	// Lazy creation: create the card on first real answer content
	if s.isLazy && !s.cardCreated {
		outTrackID := fmt.Sprintf("run_%d", time.Now().UnixNano())
		s.outTrackID = outTrackID

		err := s.client.CreateAICard(ctx, s.parent.robotCode, s.chatID, "", outTrackID, "✍️ 环宝 正在回答...")
		if err != nil {
			slog.Warn("dingtalk: lazy card creation failed", "error", err)
			return
		}

		// Register to mapping so Send() fallback can find this card
		if s.msgID != "" {
			s.parent.mu.Lock()
			s.parent.msgIDToCardID[s.msgID] = cardMapping{cardID: outTrackID, createdAt: time.Now()}
			s.parent.mu.Unlock()
			slog.Debug("dingtalk: lazy-created answer card registered", "msgID", s.msgID, "outTrackID", outTrackID)
		}

		s.cardCreated = true
		s.isAnswerStream = true // once a lazy card is created, it's an answer stream
	}

	if !s.cardCreated {
		// Should not happen for non-lazy streams, but guard anyway
		return
	}

	err := s.client.UpdateAICard(ctx, s.outTrackID, text, "✍️ 环宝 正在回答...", "2", false)
	if err != nil {
		slog.Debug("dingtalk: failed to update AI card", "outTrackID", s.outTrackID, "error", err)
		return
	}

	s.lastText = s.pending
	s.lastEdit = time.Now()
}

func (s *CardStream) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stopped = true


	// If card was never created (lazy stream stopped before any answer arrived),
	// there is nothing to finalize. Just recall the emotion sticker and return.
	if !s.cardCreated {
		s.recallEmotion()
		return nil
	}

	// Mark as delivered to avoid duplicate delivery via Send()
	if s.isAnswerStream && s.msgID != "" {
		s.parent.mu.Lock()
		s.parent.deliveredMsgs[s.msgID] = time.Now()
		s.parent.mu.Unlock()
	}

	// Final content: use pending or fall back to lastText
	text := s.pending
	if text == "" {
		text = s.lastText
	}
	text = strings.TrimPrefix(text, "_Reasoning:_\n")

	// Recall emotion sticker asynchronously
	s.recallEmotion()

	return s.client.UpdateAICard(ctx, s.outTrackID, text, "✅ 环宝 已回答", "3", true)
}

func (s *CardStream) recallEmotion() {
	if s.msgID == "" || s.conversationID == "" {
		slog.Debug("dingtalk: skipping emotion recall, missing IDs", "msgID", s.msgID, "conversationID", s.conversationID)
		return
	}
	go func() {
		ctx := context.Background()
		if err := s.client.RecallEmotionReply(ctx, s.parent.robotCode, s.msgID, s.conversationID); err != nil {
			slog.Warn("dingtalk: failed to recall emotion", "msgID", s.msgID, "error", err)
		} else {
			slog.Debug("dingtalk: emotion recalled successfully", "msgID", s.msgID)
		}
	}()
}

func (s *CardStream) MessageID() int {
	return 0
}

// StreamingChannel implementation

func (c *DingtalkChannel) StreamEnabled(isGroup bool) bool {
	if isGroup {
		return c.cfg.GroupStream != nil && *c.cfg.GroupStream
	}
	return c.cfg.DMStream != nil && *c.cfg.DMStream
}

func (c *DingtalkChannel) ReasoningStreamEnabled() bool {
	return true
}

// CreateStream handles the lifecycle of DingTalk streams.
//
// firstStream=true  → Lazy CardStream: card only created on first answer content.
//                     If the model has thinking, this stream only sees reasoning text
//                     and gets stopped before any card is created → zero empty bubbles.
//                     If no thinking, first chunk triggers lazy card creation → works fine.
//
// firstStream=false → Eager CardStream: card created immediately for the answer phase.
//                     This path is taken when transitioning from thinking→answer.
func (c *DingtalkChannel) CreateStream(ctx context.Context, chatID string, firstStream bool) (channels.ChannelStream, error) {
	msgID, _ := ctx.Value(channels.ContextKeyMsgID).(string)
	convID, _ := ctx.Value(channels.ContextKeyConversationID).(string)


	c.mu.Lock()
	defer c.mu.Unlock()

	if firstStream {
		return NewCardStream(c, c.client, chatID, "", msgID, convID, true, false), nil
	}

	// Eager answer stream — create card immediately.
	outTrackID := fmt.Sprintf("run_%d", time.Now().UnixNano())
	err := c.client.CreateAICard(ctx, c.robotCode, chatID, "", outTrackID, "✍️ 环宝 正在回答...")
	if err != nil {
		return nil, fmt.Errorf("create answer card: %w", err)
	}

	if msgID != "" {
		c.msgIDToCardID[msgID] = cardMapping{cardID: outTrackID, createdAt: time.Now()}
	}

	return NewCardStream(c, c.client, chatID, outTrackID, msgID, convID, false, true), nil
}

func (c *DingtalkChannel) FinalizeStream(ctx context.Context, chatID string, stream channels.ChannelStream) {
}
