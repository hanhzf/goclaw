# Teams Channel 用户身份解析设计文档

**版本：** v1.0  
**状态：** 待实现  
**涉及范围：** `internal/channels/teams/resolver.go` 及相关配置

---

## 背景与问题

Teams Bot 收到消息时，Activity payload 里携带的用户身份是 **AAD Object ID**（一个 GUID），而公司其他系统（Confluence、Redash 等）统一使用 **Outlook 邮箱**作为用户唯一标识。

两者之间存在一个天然的身份鸿沟：

```
Teams Activity 里的身份          其他系统里的身份
─────────────────────────        ─────────────────
aadObjectId: "xxxxxxxx-guid"  ≠  frank@raksmart.com
name: "Frank Han"
```

**目标：** 在 Teams Channel 层完成身份转换，让 GoClaw 内部流转的 `InboundMessage.SenderID` 统一为邮箱，与其他系统的用户体系对齐，上层 agent 无感知。

---

## 设计原则

- 转换逻辑完全封装在 `internal/channels/teams/` 内部，不影响 GoClaw 主代码
- 优先静态配置，简单可控，适合 40 人规模
- 预留 Graph API 动态查询的扩展点，但 P0 阶段不强依赖
- 解析失败时有明确的兜底策略，不阻断消息流转

---

## 身份流转路径

```
Teams Webhook Activity
  └─ from.aadObjectId = "xxxxxxxx-guid"
  └─ from.name        = "Frank Han"
          │
          ▼
  [resolver.Resolve()]
    1. 查静态配置表 (user_map)       ← P0 主路径
    2. 查运行时缓存 (sync.Map)       ← 减少重复查询
    3. 查 Graph API                  ← P1 扩展，可选
    4. 兜底返回 displayName + 告警   ← 保障可用性
          │
          ▼
  InboundMessage
  └─ SenderID   = "frank@raksmart.com"   ← 邮箱，跨系统通用
  └─ SenderName = "Frank Han"            ← 显示名，保留用于展示
          │
          ▼
  MessageBus → Agent Loop
  → Confluence / Redash / 其他系统      ← 用邮箱直接对接，无需转换
```

---

## 新增文件

### `internal/channels/teams/resolver.go`

**职责：** AAD Object ID → 邮箱 的单一职责解析器。

**核心结构：**

```go
type UserResolver struct {
    staticMap map[string]string  // 从配置文件加载，key: aadObjectId, value: email
    cache     sync.Map           // 运行时缓存
    graph     *GraphClient       // P1：Graph API 兜底，P0 阶段可为 nil
}
```

**解析优先级：**

| 优先级 | 来源 | 说明 |
|---|---|---|
| 1 | 静态配置表 `user_map` | P0 主路径，40 人手动维护 |
| 2 | 运行时缓存 | 避免重复查询 |
| 3 | Graph API | P1 扩展，需要额外权限 |
| 4 | displayName 兜底 | 打告警日志，不阻断流程 |

**关键方法：**

```go
// Resolve 给定 AAD Object ID，返回邮箱（或兜底的 displayName）
func (r *UserResolver) Resolve(ctx context.Context, aadObjectID, displayName string) string

// Learn 当 Activity 里意外携带了邮箱信息时，顺手写入运行时缓存
func (r *UserResolver) Learn(aadObjectID, email string)
```

---

## 配置文件改动

### `goclaw.yaml` 新增 `user_map` 字段

```yaml
channels:
  teams:
    enabled: true
    app_id: "..."
    app_password: "..."
    tenant_id: "..."

    # 用户身份映射表：AAD Object ID → 邮箱
    # 一次性收集，团队 40 人规模手动维护即可
    user_map:
      "aad-object-id-of-frank":   "frank@raksmart.com"
      "aad-object-id-of-user2":   "user2@raksmart.com"
      "aad-object-id-of-user3":   "user3@raksmart.com"
      # ... 其余成员
```

### `internal/channels/teams/config.go` 新增字段

```go
type TeamsConfig struct {
    // ... 现有字段不变 ...

    // UserMap 是 AAD Object ID → 邮箱 的静态映射表
    // 对应配置文件的 channels.teams.user_map
    UserMap map[string]string `mapstructure:"user_map"`
}
```

---

## 接入点

改动只在 `activity.go` 的 `toInboundMessage` 函数里，加一行解析调用：

```go
func (a *TeamsActivity) toInboundMessage(
    ctx context.Context,
    resolver *UserResolver,
    botID string,
) (channels.InboundMessage, error) {

    // 身份解析：AAD Object ID → 邮箱
    senderID := resolver.Resolve(ctx, a.From.AADObjectID, a.From.Name)

    return channels.InboundMessage{
        SenderID:   senderID,        // "frank@raksmart.com"
        SenderName: a.From.Name,     // "Frank Han"，仅用于展示
        // ... 其他字段不变
    }, nil
}
```

---

## 对主代码的影响

| 文件 | 改动内容 | 改动量 |
|---|---|---|
| `internal/channels/teams/resolver.go` | 新增文件 | 新增 |
| `internal/channels/teams/config.go` | 增加 `UserMap` 字段 | 1 行 |
| `internal/channels/teams/activity.go` | `toInboundMessage` 增加 resolver 参数和调用 | 2 行 |
| `internal/channels/teams/channel.go` | 初始化 `UserResolver` 并传入 webhook server | 3～5 行 |
| **主代码其他文件** | **不涉及** | **0 行** |

---

## 一次性收集 AAD Object ID 的操作步骤

Bot 上线后，临时在 `webhook.go` 的消息处理入口加一行日志：

```go
slog.Info("teams user seen",
    "aad_object_id", activity.From.AADObjectID,
    "display_name",  activity.From.Name,
)
```

然后按以下步骤操作：

1. 部署 bot，通知团队全体成员各向 bot 发送任意一条消息
2. 从 GoClaw 日志中检索 `teams user seen`，整理出 `aadObjectId → displayName` 对照表
3. 对照成员名单补全邮箱，填入 `goclaw.yaml` 的 `user_map`
4. 删除临时日志代码，重新部署

**完成后该映射表无需再维护**，除非有新员工入职或邮箱变更。

---

## P1 扩展：Graph API 动态解析

P0 阶段静态表完全够用。P1 阶段如果需要支持新用户自动识别（无需手动更新配置），可以在 `GraphClient` 里增加：

```go
// GetUserEmail 通过 AAD Object ID 查询用户邮箱
// 需要 Graph API 权限：User.Read.All（Application，需 admin consent）
func (g *GraphClient) GetUserEmail(ctx context.Context, aadObjectID string) (string, error) {
    url := fmt.Sprintf("%s/users/%s?$select=mail,userPrincipalName", g.baseURL, aadObjectID)
    // GET + Bearer token
    // 优先返回 mail 字段，mail 为空时返回 userPrincipalName
}
```

启用条件：`TeamsConfig` 里配置了 Graph API 权限，且 `UserResolver` 初始化时传入了非 nil 的 `GraphClient`。

---

## 兜底行为说明

当某个用户在静态表和缓存里都查不到，且未配置 Graph API 时，`Resolve` 返回 `displayName`（如 `"Frank Han"`），同时打印 `WARN` 级别日志：

```
WARN  teams user not resolved, falling back to display name
      aad_object_id=xxxxxxxx-guid  display_name=Frank Han
```

运维人员看到此日志后，应将该用户补充进 `user_map`。消息本身**不会被丢弃**，正常流转，只是跨系统身份对齐会失效。