package teams

import (
	"encoding/json"
	"fmt"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Factory creates a Teams channel from DB instance credentials and config.
func Factory(name string, creds json.RawMessage, cfg json.RawMessage,
	msgBus *bus.MessageBus, pairingSvc store.PairingStore) (channels.Channel, error) {
	return FactoryWithPendingStore(nil)(name, creds, cfg, msgBus, pairingSvc)
}

// FactoryWithPendingStore returns a ChannelFactory supporting persistent group history.
func FactoryWithPendingStore(pendingStore store.PendingMessageStore) channels.ChannelFactory {
	return func(name string, creds json.RawMessage, cfg json.RawMessage,
		msgBus *bus.MessageBus, pairingSvc store.PairingStore) (channels.Channel, error) {

		var cr teamsCreds
		if len(creds) > 0 {
			if err := json.Unmarshal(creds, &cr); err != nil {
				return nil, fmt.Errorf("teams: decode credentials: %w", err)
			}
		}
		if cr.AppID == "" {
			return nil, fmt.Errorf("teams: app_id is required")
		}
		if cr.AppPassword == "" {
			return nil, fmt.Errorf("teams: app_password is required")
		}

		var ic teamsInstanceConfig
		if len(cfg) > 0 {
			if err := json.Unmarshal(cfg, &ic); err != nil {
				return nil, fmt.Errorf("teams: decode config: %w", err)
			}
		}

		teamsCfg := config.TeamsConfig{
			Enabled:        true,
			AppID:          cr.AppID,
			AppPassword:    cr.AppPassword,
			TenantID:       cr.TenantID,
			ConnectionMode: ic.ConnectionMode,
			WebhookPort:    ic.WebhookPort,
			WebhookPath:    ic.WebhookPath,
			DMPolicy:       ic.DMPolicy,
			GroupPolicy:    ic.GroupPolicy,
			RequireMention: ic.RequireMention,
			HistoryLimit:   ic.HistoryLimit,
			AllowFrom:      ic.AllowFrom,
			GroupAllowFrom: ic.GroupAllowFrom,
			BlockReply:     ic.BlockReply,
			UserMap:        ic.UserMap,
		}

		// Secure default: DB loaded instances default to "pairing" for group messages
		if teamsCfg.GroupPolicy == "" {
			teamsCfg.GroupPolicy = "pairing"
		}

		ch, err := New(teamsCfg, msgBus, pairingSvc, pendingStore)
		if err != nil {
			return nil, err
		}

		ch.SetName(name)
		return ch, nil
	}
}
