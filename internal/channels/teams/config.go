package teams

// teamsCreds maps the decrypted credentials JSON from the channel_instances table.
type teamsCreds struct {
	AppID       string `json:"app_id"`
	AppPassword string `json:"app_password"`
	TenantID    string `json:"tenant_id,omitempty"`
}

// teamsInstanceConfig maps the non-secret config JSONB from the channel_instances table.
type teamsInstanceConfig struct {
	ConnectionMode string   `json:"connection_mode,omitempty"`
	WebhookPort    int      `json:"webhook_port,omitempty"`
	WebhookPath    string   `json:"webhook_path,omitempty"`
	DMPolicy       string   `json:"dm_policy,omitempty"`
	GroupPolicy    string   `json:"group_policy,omitempty"`
	RequireMention *bool    `json:"require_mention,omitempty"`
	HistoryLimit   int      `json:"history_limit,omitempty"`
	AllowFrom      []string `json:"allow_from,omitempty"`
	GroupAllowFrom []string          `json:"group_allow_from,omitempty"`
	BlockReply     *bool             `json:"block_reply,omitempty"`
	UserMap        map[string]string `json:"user_map,omitempty"`
}
