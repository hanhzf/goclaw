package teams

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Channel connects Microsoft Teams to the GoClaw message router.
type Channel struct {
	*channels.BaseChannel
	cfg           config.TeamsConfig
	jwksCache     *JWKSCache
	tokenProvider *BotTokenProvider
	sender        *TeamsSender
	graph         *GraphClient
	dedup         *DedupCache
	resolver      *UserResolver
	placeholders  sync.Map
	httpServer    *http.Server
	stopCh        chan struct{}
}

// Ensure Channel implements all required interfaces at compile time.
var _ channels.Channel = (*Channel)(nil)
var _ channels.WebhookChannel = (*Channel)(nil)
var _ channels.GroupMemberProvider = (*Channel)(nil)
var _ channels.PendingCompactable = (*Channel)(nil)
var _ channels.BlockReplyChannel = (*Channel)(nil)

// New creates a new Teams channel.
func New(cfg config.TeamsConfig, msgBus *bus.MessageBus, pairingSvc store.PairingStore, pendingStore store.PendingMessageStore) (*Channel, error) {
	if cfg.AppID == "" {
		return nil, fmt.Errorf("teams: app_id is required")
	}
	if cfg.AppPassword == "" && cfg.AuthType != "federated" {
		return nil, fmt.Errorf("teams: app_password is required")
	}

	base := channels.NewBaseChannel(channels.TypeTeams, msgBus, cfg.AllowFrom)
	base.SetType(channels.TypeTeams)
	base.ValidatePolicy(cfg.DMPolicy, cfg.GroupPolicy)

	historyLimit := cfg.HistoryLimit
	if historyLimit == 0 {
		historyLimit = channels.DefaultGroupHistoryLimit
	}

	jwksCache := NewJWKSCache(24 * time.Hour)
	tokenProvider := NewBotTokenProvider(&cfg)
	dedup := NewDedupCache(30 * time.Second)

	var placeholders sync.Map
	sender := NewTeamsSender(tokenProvider, &placeholders)
	graph := NewGraphClient(&cfg)
	resolver := NewUserResolver(cfg.UserMap, graph)

	ch := &Channel{
		BaseChannel:   base,
		cfg:           cfg,
		jwksCache:     jwksCache,
		tokenProvider: tokenProvider,
		sender:        sender,
		graph:         graph,
		dedup:         dedup,
		resolver:      resolver,
		stopCh:        make(chan struct{}),
	}

	ch.SetPairingService(pairingSvc)
	ch.SetGroupHistory(channels.MakeHistory(channels.TypeTeams, pendingStore, base.TenantID()))
	ch.SetHistoryLimit(historyLimit)

	requireMention := true
	if cfg.RequireMention != nil {
		requireMention = *cfg.RequireMention
	}
	ch.SetRequireMention(requireMention)

	return ch, nil
}

// Start begins receiving Teams events via dynamic webhook or standalone server.
func (c *Channel) Start(ctx context.Context) error {
	c.GroupHistory().StartFlusher()
	c.dedup.StartGC(ctx)
	slog.Info("teams: starting bot", "name", c.Name())

	// Warm JWKS cache
	if err := c.jwksCache.Prefetch(ctx); err != nil {
		slog.Warn("teams: failed to prefetch jwks keys, will retry on demand", "error", err)
	}

	c.SetRunning(true)

	// Standalone Server connection mode
	if c.cfg.WebhookPort > 0 {
		return c.startStandaloneServer(ctx)
	}

	slog.Info("teams: webhook mounted on main gateway mux")
	return nil
}

// Stop shuts down the Teams channel.
func (c *Channel) Stop(_ context.Context) error {
	c.GroupHistory().StopFlusher()
	slog.Info("teams: stopping bot", "name", c.Name())
	close(c.stopCh)

	if c.httpServer != nil {
		c.httpServer.Close()
	}

	c.SetRunning(false)
	return nil
}

// Send delivers an outbound message to a Teams chat.
func (c *Channel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return fmt.Errorf("teams: channel not running")
	}
	return c.sender.Send(ctx, msg)
}

// WebhookHandler returns the webhook path and HTTP handler for mounting on the main gateway mux.
func (c *Channel) WebhookHandler() (string, http.Handler) {
	// Only mount on main mux when connection mode is webhook and webhook_port is 0
	if c.cfg.WebhookPort > 0 {
		return "", nil
	}

	path := c.cfg.WebhookPath
	if path == "" {
		path = "/api/messages/teams/" + c.Name()
	}

	return path, http.HandlerFunc(c.serveWebhook)
}

// BlockReplyEnabled returns the per-channel block_reply override (nil = inherit gateway default).
func (c *Channel) BlockReplyEnabled() *bool {
	return c.cfg.BlockReply
}

// SetPendingCompaction configures LLM-based auto-compaction for pending messages.
func (c *Channel) SetPendingCompaction(cfg *channels.CompactionConfig) {
	if gh := c.GroupHistory(); gh != nil {
		gh.SetCompactionConfig(cfg)
	}
}

// SetPendingHistoryTenantID propagates tenant_id to the pending history for DB operations.
func (c *Channel) SetPendingHistoryTenantID(id uuid.UUID) {
	if gh := c.GroupHistory(); gh != nil {
		gh.SetTenantID(id)
	}
}

// ListGroupMembers returns all members of a Teams channel.
func (c *Channel) ListGroupMembers(ctx context.Context, chatID string) ([]channels.GroupMember, error) {
	members, err := c.graph.GetTeamMembers(ctx, chatID)
	if err != nil {
		slog.Warn("teams: list_group_members failed", "chat_id", chatID, "error", err)
		return nil, err
	}

	result := make([]channels.GroupMember, len(members))
	for i, m := range members {
		name := m.DisplayName
		if name == "" {
			name = m.Mail
		}
		result[i] = channels.GroupMember{
			MemberID: m.ID,
			Name:     name,
		}

		// Dynamically learn the user's AAD Object ID -> email mapping
		if m.Mail != "" {
			c.resolver.Learn(m.ID, m.Mail)
		}

		// Auto-sync member into contact store
		if cc := c.ContactCollector(); cc != nil {
			cc.EnsureContact(ctx, channels.TypeTeams, c.Name(), m.ID, m.ID, name, m.Mail, "group", "user", "", "")
		}
	}
	return result, nil
}

func (c *Channel) startStandaloneServer(ctx context.Context) error {
	port := c.cfg.WebhookPort
	path := c.cfg.WebhookPath
	if path == "" {
		path = "/api/messages/teams"
	}

	slog.Info("teams: starting standalone server", "port", port, "path", path)

	mux := http.NewServeMux()
	mux.HandleFunc(path, c.serveWebhook)

	c.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	go func() {
		if err := c.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("teams: standalone server error", "error", err)
		}
	}()

	return nil
}
