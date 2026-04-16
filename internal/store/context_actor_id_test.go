package store

import (
	"context"
	"testing"
)

func TestActorIDFromContext_SenderSetReturnsSender(t *testing.T) {
	ctx := WithUserID(context.Background(), "group:telegram:-100123")
	ctx = WithSenderID(ctx, "42")
	if got := ActorIDFromContext(ctx); got != "42" {
		t.Errorf("sender_set: got %q, want %q", got, "42")
	}
}

func TestActorIDFromContext_SenderEmptyFallbackToUser(t *testing.T) {
	ctx := WithUserID(context.Background(), "user-7")
	if got := ActorIDFromContext(ctx); got != "user-7" {
		t.Errorf("fallback_to_user: got %q, want %q", got, "user-7")
	}
}

func TestActorIDFromContext_BothSetSenderWins(t *testing.T) {
	ctx := WithUserID(context.Background(), "group:discord:G|U")
	ctx = WithSenderID(ctx, "123456")
	if got := ActorIDFromContext(ctx); got != "123456" {
		t.Errorf("both_set_sender_wins: got %q, want %q", got, "123456")
	}
}

func TestActorIDFromContext_NeitherSetReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	if got := ActorIDFromContext(ctx); got != "" {
		t.Errorf("neither_set: got %q, want %q", got, "")
	}
}

// SenderID with "|" delimiter (Discord guild:user|user format) is returned as-is —
// ActorIDFromContext is not responsible for numeric split; callers do that themselves
// (see config_permission_store.go:68 strings.SplitN(senderID, "|", 2)[0]).
func TestActorIDFromContext_DelimitedSenderReturnedAsIs(t *testing.T) {
	ctx := WithSenderID(context.Background(), "42|extra")
	if got := ActorIDFromContext(ctx); got != "42|extra" {
		t.Errorf("delimited_sender_as_is: got %q, want %q", got, "42|extra")
	}
}
