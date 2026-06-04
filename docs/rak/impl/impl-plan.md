# Microsoft Teams Channel Integration for GoClaw

This document defines the implementation plan for integrating Microsoft Teams support as a native channel in GoClaw. 

The implementation directly integrates with the **Microsoft Bot Framework REST API** and **Microsoft Graph API**, avoiding third-party Bot SDKs (like `msbotbuilder-go`).

---

## User Review Required

> [!IMPORTANT]
> **Webhook Path Mounting**
> To support multi-instance / multi-tenant Teams integrations without opening multiple dynamic ports (which makes containerization and firewall routing complex), we will implement `channels.WebhookChannel`. 
> This mounts a handler on the main gateway HTTP router under the path `/api/messages/teams/:instance_name` (e.g. `/api/messages/teams/my-bot`).
> A config option `webhook_port` will still be provided. If `webhook_port > 0`, it spins up a dedicated HTTP server on that port, otherwise it falls back to sharing the main server port. This mirrors the flexible Lark/Feishu connection pattern.

---

## Open Questions

> [!NOTE]
> 1. **Default Group Policy**: Should the default policy for new Teams instances be `pairing` (requiring passcode pairing for safety) or `open`? Following other channels in the GoClaw DB setup (e.g., Slack), we will set the default policy to `pairing` for DB-loaded channels, and `open` for config-loaded channels, ensuring maximum out-of-the-box safety.
> 2. **Teams Metadata String Conversion**: GoClaw's `bus.InboundMessage.Metadata` is a `map[string]string`. Teams webhook payload metadata (such as `serviceUrl`, `activityId`, `tenantId`) will be serialized/converted to string keys inside this map. 

---

## Proposed Changes

### Go Backend Components

We will add a new channel type `teams` and implement it in `internal/channels/teams`.

---

#### [MODIFY] [internal/channels/channel.go](file:///opt/projects/github/goclaw/internal/channels/channel.go)
* Add `TypeTeams = "teams"` to the platform constants list (around line 72-83).

---

#### [MODIFY] [internal/config/config_channels.go](file:///opt/projects/github/goclaw/internal/config/config_channels.go)
* Add a `TeamsConfig` struct representing Teams config options:
  ```go
  type TeamsConfig struct {
      Enabled                 bool                `json:"enabled"`
      AppID                   string              `json:"app_id"`
      AppPassword             string              `json:"app_password"`
      TenantID                string              `json:"tenant_id"`
      AuthType                string              `json:"auth_type"` // "secret" (default) | "federated"
      CertificatePath         string              `json:"certificate_path,omitempty"`
      UseManagedIdentity      bool                `json:"use_managed_identity,omitempty"`
      ManagedIdentityClientID string              `json:"managed_identity_client_id,omitempty"`
      ConnectionMode          string              `json:"connection_mode,omitempty"` // "webhook"
      WebhookPort             int                 `json:"webhook_port,omitempty"`    // default 0 (share gateway)
      WebhookPath             string              `json:"webhook_path,omitempty"`    // default "/api/messages/teams"
      AllowFrom               FlexibleStringSlice `json:"allow_from"`
      GroupAllowFrom          FlexibleStringSlice `json:"group_allow_from,omitempty"`
      DMPolicy                string              `json:"dm_policy,omitempty"`       // "pairing", "open", "allowlist", "disabled"
      GroupPolicy             string              `json:"group_policy,omitempty"`    // "pairing", "open", "allowlist", "disabled"
      RequireMention          *bool               `json:"require_mention,omitempty"` // default true
      HistoryLimit            int                 `json:"history_limit,omitempty"`   // default 50
      BlockReply              *bool               `json:"block_reply,omitempty"`     // override block_reply
  }
  ```
* Add `Teams TeamsConfig `json:"teams"`` to the `ChannelsConfig` struct.

---

#### [NEW] [internal/channels/teams/config.go](file:///opt/projects/github/goclaw/internal/channels/teams/config.go)
* Map DB instance configuration JSON fields into Go structures.

#### [NEW] [internal/channels/teams/auth.go](file:///opt/projects/github/goclaw/internal/channels/teams/auth.go)
* **JWKSCache**: Fetch OpenID configuration dynamically from `https://login.botframework.com/v1/.well-known/openidconfiguration` to retrieve `jwks_uri`. Parse RSA keys (`kid`, `n`, `e`) and cache them.
* **JWT Verification**: Validate Bearer Token on incoming HTTP Requests. Enforce RS256 algorithm, verify `aud` equals App ID, and verify `iss` matches Bot Framework.
* **Token Provider**: Implement OAuth2 Client Credentials Flow with caching to retrieve access tokens for sending messages.

#### [NEW] [internal/channels/teams/webhook.go](file:///opt/projects/github/goclaw/internal/channels/teams/webhook.go)
* HTTP request receiver for webhooks (handles POST events). Decodes activity payloads and forwards them to `processActivity`.
* Implements the `WebhookHandler()` method to return the route path and the handler when sharing the main gateway port.

#### [NEW] [internal/channels/teams/sender.go](file:///opt/projects/github/goclaw/internal/channels/teams/sender.go)
* Delivers outbound messages by posting to `{serviceUrl}/v3/conversations/{conversationId}/activities/{activityId}`.
* Resolves `serviceUrl` and `activityId` from the outbound metadata.
* Implements placeholder updates (such as Progress Stream / "Thinking..." messages).

#### [NEW] [internal/channels/teams/formatter.go](file:///opt/projects/github/goclaw/internal/channels/teams/formatter.go)
* Converts standard Markdown to Teams-compatible Markdown.
* Handles Adaptive Card JSON construction for rich messaging formats.

#### [NEW] [internal/channels/teams/dedup.go](file:///opt/projects/github/goclaw/internal/channels/teams/dedup.go)
* Provides a thread-safe TTL-based cache using `sync.Map` to eliminate duplicate activities caused by Teams retry cycles.

#### [NEW] [internal/channels/teams/graph.go](file:///opt/projects/github/goclaw/internal/channels/teams/graph.go)
* Implements paginated Graph API clients to load history logs and parse member IDs.

#### [NEW] [internal/channels/teams/channel.go](file:///opt/projects/github/goclaw/internal/channels/teams/channel.go)
* Implements the main `channels.Channel` and `channels.WebhookChannel` interfaces. 
* Embeds `*channels.BaseChannel` to inherit standard policy validation, contacts registry, and allowlist filters.

#### [NEW] [internal/channels/teams/factory.go](file:///opt/projects/github/goclaw/internal/channels/teams/factory.go)
* Implements the `ChannelFactory` method so that the `InstanceLoader` can instantiate Teams dynamically from database entries.

---

#### [MODIFY] [cmd/gateway.go](file:///opt/projects/github/goclaw/cmd/gateway.go)
* Register the Teams factory:
  ```go
  instanceLoader.RegisterFactory(channels.TypeTeams, teams.FactoryWithPendingStore(pgStores.PendingMessages))
  ```

---

### Web UI Components

---

#### [MODIFY] [ui/web/src/constants/channels.ts](file:///opt/projects/github/goclaw/ui/web/src/constants/channels.ts)
* Add the option `{ value: "teams", label: "Microsoft Teams" }` to the `CHANNEL_TYPES` array.

---

#### [MODIFY] [ui/web/src/pages/channels/channel-schemas.ts](file:///opt/projects/github/goclaw/ui/web/src/pages/channels/channel-schemas.ts)
* Define credentials form schema for `teams`:
  ```typescript
  teams: [
    { key: "app_id", label: "App ID", type: "text", required: true, placeholder: "Application (client) ID" },
    { key: "app_password", label: "App Password", type: "password", required: true, placeholder: "Client secret value" },
    { key: "tenant_id", label: "Tenant ID (Optional)", type: "text", placeholder: "Directory (tenant) ID" },
  ]
  ```
* Define config form schema for `teams`:
  ```typescript
  teams: [
    { key: "connection_mode", label: "Connection Mode", type: "select", options: [{ value: "webhook", label: "Webhook" }], defaultValue: "webhook" },
    { key: "webhook_port", label: "Webhook Port", type: "number", defaultValue: 0, help: "0 = share main gateway port (recommended)" },
    { key: "webhook_path", label: "Webhook Path", type: "text", defaultValue: "/api/messages/teams", help: "Mount path on the main gateway" },
    { key: "dm_policy", label: "DM Policy", type: "select", options: dmPolicyOptions, defaultValue: "pairing" },
    { key: "group_policy", label: "Group Policy", type: "select", options: groupPolicyOptions, defaultValue: "pairing" },
    { key: "require_mention", label: "Require @mention in groups", type: "boolean", defaultValue: true },
    { key: "history_limit", label: "Group History Limit", type: "number", defaultValue: 50 },
    { key: "allow_from", label: "Allowed Users (UPN or Object ID)", type: "tags", help: "Allowed senders; empty = no restriction" },
    { key: "block_reply", label: "Block Reply", type: "select", options: blockReplyOptions, defaultValue: "inherit" },
  ]
  ```

---

## Verification Plan

### Automated Tests
* Implement unit tests in `internal/channels/teams/channel_test.go` covering:
  1. JWKS configuration fetching and dynamic caching.
  2. JWT signature validation (including test tokens and simulated failures for wrong `aud`/`iss`).
  3. OAuth2 token generation mock handler.
  4. Activity parsing and conversion to GoClaw message format.
  5. Formatters for Markdown conversion.
* Run Go tests:
  ```bash
  go test -v ./internal/channels/teams/...
  ```

### Manual Verification
1. Boot the GoClaw service and ensure `teams` is correctly loaded as an option in the channels list.
2. Configure a Teams channel in the web UI.
3. Verify that incoming activity payloads hitting `/api/messages/teams` are accepted, authenticated, and trigger a response message.
