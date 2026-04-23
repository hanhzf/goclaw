package dingtalk

import (
	"encoding/json"
	"fmt"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type dingtalkCreds struct {
	AppKey    string `json:"app_key"`
	AppSecret string `json:"app_secret"`
}

type dingtalkInstanceConfig struct {
	AllowFrom      []string `json:"allow_from,omitempty"`
	DMPolicy       string   `json:"dm_policy,omitempty"`
	GroupPolicy    string   `json:"group_policy,omitempty"`
	RequireMention *bool    `json:"require_mention,omitempty"`
	DMStream       *bool    `json:"dm_stream,omitempty"`
	GroupStream    *bool    `json:"group_stream,omitempty"`
	HistoryLimit    int      `json:"history_limit,omitempty"`
}

func Factory(name string, creds json.RawMessage, cfg json.RawMessage,
	msgBus *bus.MessageBus, pairingSvc store.PairingStore) (channels.Channel, error) {

	var c dingtalkCreds
	if len(creds) > 0 {
		if err := json.Unmarshal(creds, &c); err != nil {
			return nil, fmt.Errorf("decode dingtalk credentials: %w", err)
		}
	}
	if c.AppKey == "" || c.AppSecret == "" {
		return nil, fmt.Errorf("dingtalk app_key and app_secret are required")
	}

	var ic dingtalkInstanceConfig
	if len(cfg) > 0 {
		if err := json.Unmarshal(cfg, &ic); err != nil {
			return nil, fmt.Errorf("decode dingtalk config: %w", err)
		}
	}

	dtCfg := config.DingtalkConfig{
		Enabled:        true,
		AppKey:         c.AppKey,
		AppSecret:      c.AppSecret,
		AllowFrom:      ic.AllowFrom,
		DMPolicy:       ic.DMPolicy,
		GroupPolicy:    ic.GroupPolicy,
		RequireMention: ic.RequireMention,
		DMStream:       ic.DMStream,
		GroupStream:    ic.GroupStream,
		HistoryLimit:   ic.HistoryLimit,
	}

	if dtCfg.GroupPolicy == "" {
		dtCfg.GroupPolicy = "pairing"
	}

	ch, err := New(dtCfg, msgBus, pairingSvc)
	if err != nil {
		return nil, err
	}

	ch.SetName(name)
	return ch, nil
}
