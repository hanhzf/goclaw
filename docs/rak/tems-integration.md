好的，我先把前面调研到的关键信息整合一下，然后输出架构设计文档。GitHub 渲染页面拿不到源码内容。但从之前搜索结果已经拿到了足够的接口信息——`Channel` 接口、`InboundMessage`/`OutboundMessage`、`MessageBus` 流转模型都已经清楚。现在直接输出设计文档。

---

# GoClaw `internal/channels/teams` 架构设计文档

**版本：** v0.1 Draft  
**作者：** 架构设计  
**状态：** 待开发团队评审

---

## 一、目标与范围

本文档为在 GoClaw 中从零实现 Microsoft Teams 渠道支持提供完整的伪代码级架构设计。开发者按照此文档可独立完成实现，无需依赖任何外部 Go Bot SDK（尤其是已近废弃的 `msbotbuilder-go`）。

**核心原则：**
- 直接对接 Bot Framework REST 协议，不引入第三方 Bot SDK
- 接口规范与 `slack`、`feishu`、`telegram` 目录保持一致
- 认证、Webhook、发送、Graph API 各司其职，分文件管理

---

## 二、支持功能清单（Feature Matrix）

以 OpenClaw Teams 插件为基准，按优先级分三期：

**P0 — MVP，必须实现**

| 功能 | 技术路径 |
|---|---|
| DM（私聊）消息接收 | Bot Framework Webhook Activity |
| DM 消息回复 | Bot Connector REST API |
| 频道（Channel）消息接收（含 @mention） | RSC `ChannelMessage.Read.Group` |
| 频道消息回复 | Bot Connector REST API |
| Group Chat 消息接收 | RSC `ChatMessage.Read.Chat` |
| Webhook 签名鉴权（JWT 验证） | `golang-jwt/jwt` + Microsoft JWKS |
| Bot Token 获取与自动刷新 | OAuth2 Client Credentials Flow |
| 基础文本格式化（bold/italic/code/link） | Teams Markdown 适配层 |
| `requireMention` 开关（频道免 @ 或强制 @） | 本地配置控制 |
| dmPolicy（allowlist / open） | 本地配置控制 |
| groupPolicy（allowlist / open / disabled） | 本地配置控制 |

**P1 — 完整功能**

| 功能 | 技术路径 |
|---|---|
| DM 附件接收（文件/图片） | Bot Framework `contentUrl` 下载 |
| Adaptive Card 发送（用于 Poll 等） | `attachments` 字段构造 |
| 主动消息推送（Proactive Message） | 保存 `ConversationReference`，Bot Connector 主动发起 |
| 消息去重（防 Teams Webhook 重试） | 内存 `sync.Map` 缓存 `activityId` TTL 30s |
| 历史消息拉取（historyLimit） | Microsoft Graph `ChannelMessage.Read.All` |
| 成员信息查询（member-info action） | Microsoft Graph `Member.Read.Group` |

**P2 — 高级功能**

| 功能 | 技术路径 |
|---|---|
| 频道/Group 图片附件下载 | Graph API + SharePoint/OneDrive |
| 证书鉴权（Federated Auth / Certificate） | `crypto/tls` + PEM 加载 |
| Azure Managed Identity 鉴权 | Azure IMDS endpoint token 获取 |
| `allowFrom` AAD Object ID 解析 | Graph `User.Read.All` |
| Teams/Channel allowlist 名称解析为 ID | Graph `Team.ReadBasic.All` |

---

## 三、目录结构

```
internal/channels/teams/
├── channel.go          // Channel 结构体，实现 GoClaw Channel 接口
├── config.go           // TeamsConfig 配置结构体定义
├── webhook.go          // HTTP server，/api/messages 路由，Activity 解析
├── auth.go             // JWT 验证（上行鉴权）+ OAuth2 Token 管理（下行凭证）
├── sender.go           // 消息下行发送，Bot Connector REST 调用
├── formatter.go        // Teams Markdown 适配，Adaptive Card 构造
├── graph.go            // Microsoft Graph API 客户端（P1/P2 功能）
├── activity.go         // Activity/InboundMessage 类型定义与转换
└── dedup.go            // 消息去重，activityId TTL 缓存
```

---

## 四、配置结构体（config.go）

```go
// TeamsConfig 对齐 OpenClaw msteams 配置键名，支持 env var 覆盖
type TeamsConfig struct {
    Enabled  bool   `mapstructure:"enabled"`
    AppID    string `mapstructure:"app_id"`     // env: MSTEAMS_APP_ID
    AppPassword string `mapstructure:"app_password"` // env: MSTEAMS_APP_PASSWORD
    TenantID string `mapstructure:"tenant_id"`  // env: MSTEAMS_TENANT_ID

    // 鉴权模式："secret"（默认）| "federated"
    AuthType           string `mapstructure:"auth_type"`
    CertificatePath    string `mapstructure:"certificate_path"`
    UseManagedIdentity bool   `mapstructure:"use_managed_identity"`
    ManagedIdentityClientID string `mapstructure:"managed_identity_client_id"`

    Webhook struct {
        Port int    `mapstructure:"port"` // default: 3978
        Path string `mapstructure:"path"` // default: "/api/messages"
    } `mapstructure:"webhook"`

    // 访问控制
    DMPolicy    string   `mapstructure:"dm_policy"`    // "pairing" | "open"
    GroupPolicy string   `mapstructure:"group_policy"` // "allowlist" | "open" | "disabled"
    AllowFrom   []string `mapstructure:"allow_from"`   // AAD Object ID 列表
    GroupAllowFrom []string `mapstructure:"group_allow_from"`

    // 频道级别白名单
    Teams map[string]TeamEntry `mapstructure:"teams"`

    // 行为开关
    RequireMention bool `mapstructure:"require_mention"` // default: true（频道/群）
    HistoryLimit   int  `mapstructure:"history_limit"`   // default: 50
    ConfigWrites   bool `mapstructure:"config_writes"`   // default: true

    // Graph 相关（P1）
    SharePointSiteID string `mapstructure:"sharepoint_site_id"`

    // 媒体域白名单
    MediaAllowHosts []string `mapstructure:"media_allow_hosts"`
}

type TeamEntry struct {
    Channels map[string]ChannelEntry `mapstructure:"channels"`
}

type ChannelEntry struct {
    RequireMention *bool `mapstructure:"require_mention"` // 覆盖全局设置
}
```

---

## 五、Activity 类型定义（activity.go）

Teams 通过 Bot Framework 协议投递 Activity，这是整个模块最核心的数据结构。

```go
// TeamsActivity 是 Bot Framework Activity 的 Go 结构体映射
// 参考：https://learn.microsoft.com/en-us/azure/bot-service/rest-api/bot-framework-rest-connector-api-reference
type TeamsActivity struct {
    Type           string          `json:"type"`           // "message" | "conversationUpdate" | "invoke" | ...
    ID             string          `json:"id"`             // activityId，用于去重和回复
    Timestamp      time.Time       `json:"timestamp"`
    ServiceURL     string          `json:"serviceUrl"`     // Bot Connector 回调地址，必须保存
    ChannelID      string          `json:"channelId"`      // 固定值 "msteams"
    From           ChannelAccount  `json:"from"`
    Conversation   ConversationRef `json:"conversation"`
    Recipient      ChannelAccount  `json:"recipient"`
    Text           string          `json:"text"`           // 消息正文（可能含 HTML mention stub）
    TextFormat     string          `json:"textFormat"`     // "markdown" | "plain" | "xml"
    Attachments    []Attachment    `json:"attachments"`
    MentionsRaw    []Mention       `json:"entities"`       // mention 列表，类型为 "mention"
    ReplyToID      string          `json:"replyToId"`      // 回复引用
    ChannelData    TeamsChannelData `json:"channelData"`   // Teams 特有扩展数据
    Locale         string          `json:"locale"`
}

type ChannelAccount struct {
    ID   string `json:"id"`   // AAD Object ID
    Name string `json:"name"` // 显示名
    AADObjectID string `json:"aadObjectId"` // 部分场景才有
}

type ConversationRef struct {
    ID           string `json:"id"`
    IsGroup      *bool  `json:"isGroup"`
    ConversationType string `json:"conversationType"` // "personal" | "channel" | "groupChat"
    TenantID     string `json:"tenantId"`
}

type Attachment struct {
    ContentType string          `json:"contentType"`
    ContentURL  string          `json:"contentUrl"`
    Content     json.RawMessage `json:"content"` // Adaptive Card 等结构体内容
    Name        string          `json:"name"`
}

type Mention struct {
    Type     string         `json:"type"`    // "mention"
    Text     string         `json:"text"`    // e.g. "<at>BotName</at>"
    Mentioned ChannelAccount `json:"mentioned"`
}

type TeamsChannelData struct {
    // 频道场景
    Channel *struct {
        ID string `json:"id"` // Teams Channel ID
    } `json:"channel"`
    Team *struct {
        ID string `json:"id"`
    } `json:"team"`
    // 消息来源类型：用于区分 channel / groupchat / personal
    EventType string `json:"eventType"`
}

// toInboundMessage 将 TeamsActivity 转换为 GoClaw 统一的 InboundMessage
// 这是对接 MessageBus 的关键转换函数
func (a *TeamsActivity) toInboundMessage(cfg *TeamsConfig, botID string) (channels.InboundMessage, error) {
    // 1. 判断消息来源类型
    scope := resolveScope(a) // "dm" | "channel" | "groupchat"

    // 2. 提取并清理正文
    //    - 剥离 bot 自身的 <at>BotName</at> mention stub
    //    - 将其他用户 mention stub 替换为 @DisplayName
    text := stripBotMention(a.Text, a.MentionsRaw, botID)
    text = resolveUserMentions(text, a.MentionsRaw)

    // 3. 判断是否包含对 bot 的 mention（频道/群聊 requireMention 判断依据）
    mentioned := isBotMentioned(a.MentionsRaw, botID)

    // 4. 构造 senderID，优先使用 AAD Object ID（稳定），回退到 from.id
    senderID := a.From.AADObjectID
    if senderID == "" {
        senderID = a.From.ID
    }

    // 5. 构造 conversationKey，用于 GoClaw 路由
    //    格式：teams:<conversationType>:<conversationId>
    convKey := fmt.Sprintf("teams:%s:%s", a.Conversation.ConversationType, a.Conversation.ID)

    return channels.InboundMessage{
        ChannelName:    "teams",
        SenderID:       senderID,
        SenderName:     a.From.Name,
        ConversationID: convKey,
        Text:           text,
        Locale:         a.Locale,
        Attachments:    extractAttachments(a.Attachments),
        Metadata: map[string]any{
            "scope":         scope,
            "activityId":    a.ID,
            "serviceUrl":    a.ServiceURL,    // 发送回复时必须用原始 serviceUrl
            "teamId":        safeTeamID(a),
            "channelId":     safeChannelID(a),
            "mentioned":     mentioned,
            "replyToId":     a.ReplyToID,
            "tenantId":      a.Conversation.TenantID,
        },
    }, nil
}
```

---

## 六、鉴权模块（auth.go）

Teams Bot 鉴权分两个方向，职责完全不同，必须分开管理：

```go
// ============================================================
// 上行鉴权：验证 Teams 发来的 Webhook 请求是否合法
// Teams 在 HTTP Header Authorization: Bearer <jwt> 里携带 token
// 需要从微软 JWKS 端点取公钥做验证
// ============================================================

const (
    // Teams Bot Framework 的 OpenID 元数据地址
    openIDMetaURL = "https://login.botframework.com/v1/.well-known/openidconfiguration"
    // 也需要支持 Government Cloud 等变体，此处先实现公有云
    validAudience = "https://api.botframework.com"
)

type JWKSCache struct {
    mu      sync.RWMutex
    keys    map[string]crypto.PublicKey  // kid -> public key
    fetchedAt time.Time
    ttl     time.Duration               // 建议 24h，key rotation 不频繁
}

// ValidateInboundRequest 验证 Teams 推来的 HTTP 请求签名
// 失败直接返回 401，不进入业务逻辑
func ValidateInboundRequest(ctx context.Context, r *http.Request, cache *JWKSCache) error {
    authHeader := r.Header.Get("Authorization")
    if !strings.HasPrefix(authHeader, "Bearer ") {
        return errors.New("missing bearer token")
    }
    tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

    // 1. 解析 JWT header，取 kid
    kid, err := extractKID(tokenStr)

    // 2. 从缓存（或远程 JWKS）取对应公钥
    pubKey, err := cache.Get(ctx, kid)

    // 3. 验证签名 + claims
    //    - aud 必须是 AppID（或 validAudience）
    //    - iss 必须是 "https://api.botframework.com"
    //    - exp 未过期
    token, err := jwt.ParseWithClaims(tokenStr, &BotClaims{}, func(t *jwt.Token) (any, error) {
        if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
            return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
        }
        return pubKey, nil
    })

    claims := token.Claims.(*BotClaims)
    if claims.Audience != appID {
        return errors.New("audience mismatch")
    }
    return nil
}

// JWKSCache.Get 内部逻辑（伪代码）
func (c *JWKSCache) Get(ctx context.Context, kid string) (crypto.PublicKey, error) {
    c.mu.RLock()
    key, ok := c.keys[kid]
    c.mu.RUnlock()
    if ok && time.Since(c.fetchedAt) < c.ttl {
        return key, nil
    }
    // 超时或 kid 不在缓存，重新拉取
    return c.refresh(ctx, kid)
}

// ============================================================
// 下行凭证：获取 Bot 向微软发消息用的 Access Token
// OAuth2 Client Credentials Flow
// POST https://login.microsoftonline.com/{tenantId}/oauth2/v2.0/token
// ============================================================

type BotTokenProvider struct {
    mu          sync.Mutex
    cfg         *TeamsConfig
    cachedToken string
    expiresAt   time.Time
    httpClient  *http.Client
}

func (p *BotTokenProvider) GetToken(ctx context.Context) (string, error) {
    p.mu.Lock()
    defer p.mu.Unlock()

    // 提前 60s 刷新，避免边界过期
    if p.cachedToken != "" && time.Now().Add(60*time.Second).Before(p.expiresAt) {
        return p.cachedToken, nil
    }

    // 根据 authType 选择不同的 token 获取策略
    switch p.cfg.AuthType {
    case "federated":
        if p.cfg.UseManagedIdentity {
            return p.fetchManagedIdentityToken(ctx)  // P2
        }
        return p.fetchCertificateToken(ctx)           // P2
    default:
        return p.fetchClientSecretToken(ctx)          // P0，优先实现
    }
}

func (p *BotTokenProvider) fetchClientSecretToken(ctx context.Context) (string, error) {
    // POST https://login.microsoftonline.com/{tenantId}/oauth2/v2.0/token
    // body: grant_type=client_credentials
    //       client_id={appId}
    //       client_secret={appPassword}
    //       scope=https://api.botframework.com/.default
    //
    // 解析响应，缓存 access_token + expires_in
    // 返回 token string
}
```

---

## 七、Webhook 服务（webhook.go）

```go
// TeamsWebhookServer 是一个内嵌的 HTTP 服务，专门处理 Teams 推来的 Activity
// 与 GoClaw 其他 Webhook 渠道（如 Feishu）的模式一致：
// 启动独立 goroutine 监听，通过 MessageBus 将消息投递到 agent loop

type TeamsWebhookServer struct {
    cfg         *TeamsConfig
    validator   *JWKSCache          // 上行鉴权
    tokenProvider *BotTokenProvider // 下行凭证
    bus         bus.MessageBus      // GoClaw 内部消息总线
    dedup       *DedupCache         // 去重
    botID       string              // 启动后从 /api/messages 第一条消息或配置获取
    server      *http.Server
}

func (s *TeamsWebhookServer) Start(ctx context.Context) error {
    mux := http.NewServeMux()
    mux.HandleFunc(s.cfg.Webhook.Path, s.handleActivity)

    s.server = &http.Server{
        Addr:         fmt.Sprintf(":%d", s.cfg.Webhook.Port),
        Handler:      mux,
        ReadTimeout:  10 * time.Second,
        WriteTimeout: 15 * time.Second,  // Teams 要求在 15s 内响应，否则重试
    }

    go func() {
        <-ctx.Done()
        s.server.Shutdown(context.Background())
    }()

    slog.Info("teams webhook listening", "port", s.cfg.Webhook.Port, "path", s.cfg.Webhook.Path)
    return s.server.ListenAndServe()
}

func (s *TeamsWebhookServer) handleActivity(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()

    // 1. 上行 JWT 鉴权，失败 → 401
    if err := ValidateInboundRequest(ctx, r, s.validator); err != nil {
        slog.Warn("teams auth failed", "err", err, "remote", r.RemoteAddr)
        http.Error(w, "Unauthorized", http.StatusUnauthorized)
        return
    }

    // 2. 解析 Activity body
    var activity TeamsActivity
    if err := json.NewDecoder(r.Body).Decode(&activity); err != nil {
        http.Error(w, "Bad Request", http.StatusBadRequest)
        return
    }

    // 3. 立刻返回 200（Teams 要求快速响应，回复通过 proactive 发送）
    w.WriteHeader(http.StatusOK)

    // 4. 异步处理，不阻塞 HTTP handler
    go s.processActivity(ctx, &activity)
}

func (s *TeamsWebhookServer) processActivity(ctx context.Context, activity *TeamsActivity) {
    switch activity.Type {
    case "message":
        s.handleMessageActivity(ctx, activity)
    case "conversationUpdate":
        s.handleConversationUpdate(ctx, activity)
        // 可用于记录 bot 被添加到频道/群聊的事件
    case "invoke":
        // Adaptive Card 按钮点击等，P1 阶段实现
        s.handleInvokeActivity(ctx, activity)
    default:
        slog.Debug("teams activity ignored", "type", activity.Type, "id", activity.ID)
    }
}

func (s *TeamsWebhookServer) handleMessageActivity(ctx context.Context, activity *TeamsActivity) {
    // 1. 去重检查（Teams 在超时时会重发）
    if s.dedup.Seen(activity.ID) {
        slog.Debug("teams duplicate activity dropped", "id", activity.ID)
        return
    }
    s.dedup.Mark(activity.ID)

    // 2. 访问控制
    if !s.isAllowed(activity) {
        slog.Debug("teams message rejected by policy",
            "sender", activity.From.ID,
            "scope", resolveScope(activity),
        )
        return
    }

    // 3. requireMention 检查（频道/群聊场景）
    scope := resolveScope(activity)
    if scope != "dm" {
        requireMention := s.resolveRequireMention(activity)
        if requireMention && !isBotMentioned(activity.MentionsRaw, s.botID) {
            return // 未 @ bot，忽略
        }
    }

    // 4. 转换为 InboundMessage
    msg, err := activity.toInboundMessage(s.cfg, s.botID)
    if err != nil {
        slog.Error("teams activity conversion failed", "err", err)
        return
    }

    // 5. 保存 ConversationReference，供 Proactive Message 使用（P1）
    s.saveConversationRef(activity)

    // 6. 投递到 GoClaw MessageBus
    s.bus.Publish(ctx, msg)
}

// isAllowed 综合 dmPolicy / groupPolicy / allowFrom 做访问控制
func (s *TeamsWebhookServer) isAllowed(activity *TeamsActivity) bool {
    scope := resolveScope(activity)
    senderID := activity.From.AADObjectID
    if senderID == "" {
        senderID = activity.From.ID
    }

    switch scope {
    case "dm":
        switch s.cfg.DMPolicy {
        case "open":
            return true
        default: // "pairing" 模式，需在 allowFrom 中
            return contains(s.cfg.AllowFrom, senderID)
        }
    case "channel", "groupchat":
        switch s.cfg.GroupPolicy {
        case "disabled":
            return false
        case "open":
            return true
        default: // "allowlist"
            allowList := s.cfg.GroupAllowFrom
            if len(allowList) == 0 {
                allowList = s.cfg.AllowFrom
            }
            return contains(allowList, senderID)
        }
    }
    return false
}
```

---

## 八、消息发送（sender.go）

```go
// TeamsSender 负责所有下行消息，通过 Bot Connector REST API 发送
// 核心端点：{serviceUrl}/v3/conversations/{conversationId}/activities/{activityId}

type TeamsSender struct {
    tokenProvider *BotTokenProvider
    httpClient    *http.Client
}

// Send 实现 Channel 接口的 Send 方法，被 GoClaw Manager.dispatchOutbound 调用
func (s *TeamsSender) Send(ctx context.Context, msg channels.OutboundMessage) error {
    // 从 OutboundMessage.Metadata 中取回 activity 上下文
    serviceURL := msg.Metadata["serviceUrl"].(string)
    conversationID := extractConversationID(msg.ConversationID) // 从 "teams:channel:xxx" 解析
    replyToID := msg.Metadata["activityId"].(string)

    activity := s.buildReplyActivity(msg, replyToID)

    url := fmt.Sprintf("%s/v3/conversations/%s/activities/%s",
        serviceURL, conversationID, replyToID)

    return s.doPost(ctx, url, activity)
}

// SendProactive 主动推送消息，不依赖 replyToId（P1）
// 需要事先保存的 ConversationReference
func (s *TeamsSender) SendProactive(ctx context.Context, ref ConversationReference, text string) error {
    url := fmt.Sprintf("%s/v3/conversations/%s/activities",
        ref.ServiceURL, ref.Conversation.ID)

    activity := BotActivity{
        Type: "message",
        Text: text,
    }
    return s.doPost(ctx, url, activity)
}

func (s *TeamsSender) doPost(ctx context.Context, url string, body any) error {
    token, err := s.tokenProvider.GetToken(ctx)
    if err != nil {
        return fmt.Errorf("teams get token: %w", err)
    }

    payload, _ := json.Marshal(body)
    req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
    req.Header.Set("Authorization", "Bearer "+token)
    req.Header.Set("Content-Type", "application/json")

    resp, err := s.httpClient.Do(req)
    if err != nil {
        return fmt.Errorf("teams send failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode >= 300 {
        body, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("teams connector error %d: %s", resp.StatusCode, string(body))
    }
    return nil
}

// buildReplyActivity 构造回复 Activity，包含文本格式化
func (s *TeamsSender) buildReplyActivity(msg channels.OutboundMessage, replyToID string) BotActivity {
    formatted := FormatForTeams(msg.Text) // 调用 formatter.go

    activity := BotActivity{
        Type:      "message",
        ReplyToID: replyToID,
        Text:      formatted,
        TextFormat: "markdown",
    }

    // 如果有 Adaptive Card（P1）
    if msg.Card != nil {
        activity.Attachments = []Attachment{
            {
                ContentType: "application/vnd.microsoft.card.adaptive",
                Content:     msg.Card,
            },
        }
        activity.Text = "" // Card 和 Text 不同时使用
    }

    return activity
}
```

---

## 九、格式化层（formatter.go）

Teams Markdown 能力比 Slack/Discord 弱，需要做降级处理：

```go
// FormatForTeams 将 GoClaw 通用文本格式化为 Teams 兼容的 Markdown
// Teams 支持：**bold** *italic* `code` [link](url)
// Teams 不支持：表格（需转 ASCII）、多级嵌套列表（平铺处理）
func FormatForTeams(text string) string {
    // 1. 将 GoClaw 内部格式（如有）标准化
    // 2. Markdown 表格 → <pre> ASCII 表格
    // 3. 超长消息自动分块（Teams 单条消息建议 < 28KB）
    // 4. 代码块包在 <pre><code> 中确保等宽渲染
    return processed
}

// BuildAdaptiveCard 构造 Adaptive Card JSON（用于 Poll / 富交互）
// 参考 Teams Adaptive Card schema v1.5
func BuildAdaptiveCard(title, body string, actions []CardAction) json.RawMessage {
    card := map[string]any{
        "$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
        "type":    "AdaptiveCard",
        "version": "1.5",
        "body": []map[string]any{
            {"type": "TextBlock", "text": title, "weight": "Bolder", "size": "Medium"},
            {"type": "TextBlock", "text": body, "wrap": true},
        },
        "actions": buildCardActions(actions),
    }
    raw, _ := json.Marshal(card)
    return raw
}
```

---

## 十、主 Channel 结构体（channel.go）

这是对接 GoClaw `Channel` 接口的入口：

```go
// Channel 实现 GoClaw 的 channels.Channel 接口
// 与 internal/channels/slack/channel.go、feishu/channel.go 结构一致
type Channel struct {
    cfg           *TeamsConfig
    webhookServer *TeamsWebhookServer
    sender        *TeamsSender
    tokenProvider *BotTokenProvider
    jwksCache     *JWKSCache
    dedup         *DedupCache
    graph         *GraphClient  // P1，可为 nil
    status        channels.ChannelStatus
    mu            sync.RWMutex
}

func New(cfg *TeamsConfig, bus bus.MessageBus) (*Channel, error) {
    if !cfg.Enabled {
        return nil, channels.ErrChannelDisabled
    }
    // 验证必填项
    if cfg.AppID == "" || (cfg.AuthType != "federated" && cfg.AppPassword == "") {
        return nil, errors.New("teams: app_id and app_password are required")
    }

    tokenProvider := &BotTokenProvider{cfg: cfg, httpClient: &http.Client{Timeout: 10 * time.Second}}
    jwksCache := &JWKSCache{ttl: 24 * time.Hour, httpClient: &http.Client{Timeout: 10 * time.Second}}
    dedup := NewDedupCache(30 * time.Second) // activityId TTL

    webhookServer := &TeamsWebhookServer{
        cfg:           cfg,
        validator:     jwksCache,
        tokenProvider: tokenProvider,
        bus:           bus,
        dedup:         dedup,
    }

    sender := &TeamsSender{
        tokenProvider: tokenProvider,
        httpClient:    &http.Client{Timeout: 30 * time.Second},
    }

    return &Channel{
        cfg:           cfg,
        webhookServer: webhookServer,
        sender:        sender,
        tokenProvider: tokenProvider,
        jwksCache:     jwksCache,
        dedup:         dedup,
    }, nil
}

// Name 实现 Channel 接口
func (c *Channel) Name() string { return "teams" }

// Start 实现 Channel 接口：启动 Webhook HTTP 服务
func (c *Channel) Start(ctx context.Context) error {
    c.mu.Lock()
    c.status = channels.StatusStarting
    c.mu.Unlock()

    // 预热 JWKS 缓存（避免第一条消息时延迟）
    if err := c.jwksCache.Prefetch(ctx); err != nil {
        slog.Warn("teams jwks prefetch failed, will retry on demand", "err", err)
    }

    c.mu.Lock()
    c.status = channels.StatusRunning
    c.mu.Unlock()

    // 启动 HTTP 服务（阻塞直到 ctx 取消）
    return c.webhookServer.Start(ctx)
}

// Send 实现 Channel 接口：发送消息到 Teams
func (c *Channel) Send(ctx context.Context, msg channels.OutboundMessage) error {
    return c.sender.Send(ctx, msg)
}

// Status 实现 Channel 接口
func (c *Channel) Status() channels.ChannelStatus {
    c.mu.RLock()
    defer c.mu.RUnlock()
    return c.status
}
```

---

## 十一、去重缓存（dedup.go）

Teams 在 HTTP 超时时会重发 Webhook，必须做 Activity ID 去重：

```go
// DedupCache 基于 sync.Map 的 TTL 去重缓存
// TTL 建议 30s，覆盖 Teams 的重试窗口
type DedupCache struct {
    m   sync.Map
    ttl time.Duration
}

type dedupEntry struct {
    expiresAt time.Time
}

func (d *DedupCache) Seen(activityID string) bool {
    v, ok := d.m.Load(activityID)
    if !ok {
        return false
    }
    entry := v.(dedupEntry)
    if time.Now().After(entry.expiresAt) {
        d.m.Delete(activityID)
        return false
    }
    return true
}

func (d *DedupCache) Mark(activityID string) {
    d.m.Store(activityID, dedupEntry{expiresAt: time.Now().Add(d.ttl)})
}

// 后台定期 GC（避免 sync.Map 无限增长）
func (d *DedupCache) startGC(ctx context.Context) {
    ticker := time.NewTicker(5 * time.Minute)
    go func() {
        for {
            select {
            case <-ticker.C:
                now := time.Now()
                d.m.Range(func(k, v any) bool {
                    if now.After(v.(dedupEntry).expiresAt) {
                        d.m.Delete(k)
                    }
                    return true
                })
            case <-ctx.Done():
                ticker.Stop()
                return
            }
        }
    }()
}
```

---

## 十二、Graph API 客户端（graph.go，P1）

```go
// GraphClient 封装 Microsoft Graph API 调用
// P1 阶段需要：历史消息拉取、成员信息查询
// P2 阶段需要：附件下载（SharePoint/OneDrive）

type GraphClient struct {
    tokenProvider *BotTokenProvider
    httpClient    *http.Client
    baseURL       string // https://graph.microsoft.com/v1.0
}

// GetChannelMessages 拉取频道历史消息（需要 ChannelMessage.Read.All 权限 + admin consent）
func (g *GraphClient) GetChannelMessages(
    ctx context.Context,
    teamID, channelID string,
    limit int,
) ([]GraphMessage, error) {
    url := fmt.Sprintf("%s/teams/%s/channels/%s/messages?$top=%d", g.baseURL, teamID, channelID, limit)
    // GET + Bearer token
    // 返回 value[] 数组，包含 from、body、createdDateTime
}

// GetMember 查询频道成员信息（需要 Member.Read.Group RSC 权限）
func (g *GraphClient) GetMember(ctx context.Context, groupID, userID string) (*GraphUser, error) {
    url := fmt.Sprintf("%s/groups/%s/members/%s", g.baseURL, groupID, userID)
    // GET + Bearer token
}

// DownloadHostedContent 下载 Teams 消息内嵌媒体（图片等）
// 需要 Graph Application permission + admin consent（P2）
func (g *GraphClient) DownloadHostedContent(ctx context.Context, contentURL string) ([]byte, string, error) {
    // GET contentURL with Bearer token（Graph token，非 Bot token）
    // 返回 (bytes, mimeType, error)
}
```

---

## 十三、注册到 GoClaw Channel Manager

在 GoClaw 的 channel 注册入口文件（通常是 `internal/channels/manager.go` 或 `channels/registry.go`）中添加：

```go
// 参考 slack、feishu 的注册方式，添加以下逻辑：

import "github.com/nextlevelbuilder/goclaw/internal/channels/teams"

// 在 Manager 初始化时：
if cfg.Channels.Teams.Enabled {
    teamsChannel, err := teams.New(&cfg.Channels.Teams, bus)
    if err != nil {
        slog.Error("teams channel init failed", "err", err)
    } else {
        m.Register(teamsChannel)
    }
}
```

在 `config.ChannelsConfig` 中添加：

```go
type ChannelsConfig struct {
    // ... 现有字段 ...
    Teams teams.TeamsConfig `mapstructure:"teams" json:"teams"`
}
```

---

## 十四、开发顺序建议

建议开发者按以下顺序逐步实现，每一步都可独立测试：

**第一步：建骨架**
- 创建目录和所有文件，全部写空实现（`channel.go` 返回 stub，编译通过即可）

**第二步：实现鉴权层（auth.go）**
- `BotTokenProvider.fetchClientSecretToken` — 先跑通 token 获取
- `JWKSCache` — 实现 JWKS 拉取和缓存（可用 Bot Framework Emulator 做本地验证）

**第三步：实现 Webhook 接收（webhook.go + activity.go）**
- 启动 HTTP 服务，能接收 Teams Activity 并打印日志
- 实现 `toInboundMessage` 转换
- 用 ngrok 暴露本地端口，在 Azure Bot 注册真实 Teams App 做端到端测试

**第四步：实现消息发送（sender.go）**
- `doPost` + `buildReplyActivity`
- 测试 DM 回复链路打通

**第五步：访问控制 + 去重（webhook.go + dedup.go）**
- `isAllowed`、`requireMention` 逻辑
- `DedupCache`

**第六步：接入 MessageBus，注册到 Manager**

**第七步（P1）：Graph API、Proactive Message、Adaptive Card**

---

## 十五、关键注意事项

1. **`serviceUrl` 必须原样保留**：Teams 不同租户、不同区域的 `serviceUrl` 不同（如 `https://smba.trafficmanager.net/apac/`），回复时必须用来自原始 Activity 的 `serviceUrl`，不能硬编码。

2. **Token 区分**：上行鉴权用微软签发给 Bot Framework 的公钥验证，下行发消息用 `client_credentials` 换来的 token，是两套完全不同的 token，不要混用。

3. **频道 vs 群聊 vs DM 的 `conversationId` 格式不同**：频道消息的 conversationId 形如 `19:xxx@thread.tacv2`，群聊形如 `19:xxx@thread.v2`，DM 形如 `29:xxx`，路由时需要正确区分。

4. **`requireMention` 在频道默认开启**：Teams 频道里 bot 会收到所有消息（RSC 权限），不过滤的话会处理大量无关消息，默认应该要求 @mention。

5. **Teams Webhook 15 秒超时**：HTTP handler 必须立刻 200 响应，LLM 推理结果通过 Proactive Message 异步回传，这是与 Telegram/Discord 轮询模式最大的架构差异。


对，你的理解是正确的。Teams channel 本身的代码全部在 `internal/channels/teams/` 目录里，但要让 GoClaw 认识它，确实需要在主代码里动几个地方。

根据 GoClaw 的架构，最少只需要碰以下几个文件：

---

**1. `internal/channels/manager.go`（或类似的注册入口）**

只需加两三行——实例化 `teams.New()`，然后 `Register()` 进去。改动量极小，和 Slack、Feishu 的注册方式完全一样，照猫画虎即可。

**2. `internal/config/config.go`（或 `channels_config.go`）**

在 `ChannelsConfig` 结构体里加一个字段：

```go
Teams TeamsConfig `mapstructure:"teams" json:"teams"`
```

就这一行。

**3. `goclaw.yaml` / `goclaw.json5`（配置文件示例或默认模板）**

如果项目里有配置文件模板，加一个 `teams:` 的注释示例段。这个严格来说不是代码改动，但对使用者友好。

---

**总结：主代码改动只有 2 个 Go 文件，各加 1～5 行。**

其余所有逻辑——鉴权、Webhook、发送、格式化、Graph API——全部封装在 `internal/channels/teams/` 目录内部，对主干代码零侵入。

这也是为什么说直接基于 Bot Framework REST 协议自实现比引入外部 SDK 更合适——外部 SDK 往往会污染 `go.mod`，影响整个项目的依赖树，而自实现只依赖标准库加上 `golang-jwt/jwt` 这一个轻量依赖。


---

这份文档覆盖了从零到 P0 MVP 的完整实现路径，P1/P2 功能有明确的扩展点，开发者可以按步骤独立推进。有没有哪个模块需要更细化？比如 JWT 验证的具体实现、Adaptive Card 格式，或者 Graph API 的分页处理？


