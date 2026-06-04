# GoClaw Microsoft Teams 用户身份解析方案

本方案旨在解决 Teams 平台与内部系统（Confluence、Redash 等）的身份鸿沟。我们将把 Teams Webhook 消息中的 `AAD Object ID`（GUID）解析为用户邮箱，使上层系统无缝对接。

---

## 核心设计

解析器（`UserResolver`）将按以下优先级依次解析身份：
1. **静态映射表 (`user_map`)**：在 `goclaw.yaml` 配置文件中手动维护（适合 40 人小团队）。
2. **运行时缓存 (`sync.Map`)**：保存已解析成功的结果，避免重复查询。
3. **Graph API 动态查询**（可选）：当配置了 Graph 权限时，动态请求 `/users/{aadObjectID}` 获取邮箱并写入缓存。
4. **显示名（DisplayName）兜底**：当以上都不可用时，使用用户显示名作为 SenderID，并打印 WARN 警告日志提示维护映射，保证消息不中断。

---

## 涉及的代码改动

### 1. 全局配置与实例配置改动
* **`internal/config/config_channels.go`**: 在 `TeamsConfig` 结构体中添加 `UserMap map[string]string json:"user_map,omitempty"` 字段。
* **`internal/channels/teams/config.go`**: 在 `teamsInstanceConfig` 中同样添加 `UserMap` 字段，以支持从数据库加载配置。
* **`internal/channels/teams/factory.go`**: 实例化时，将配置中的 `UserMap` 传递给新建的通道实例。

### 2. 新增解析器文件 `internal/channels/teams/resolver.go`
* 实现 `UserResolver` 结构体：
  ```go
  type UserResolver struct {
      staticMap map[string]string
      cache     sync.Map
      graph     *GraphClient
  }
  ```
* 提供 `Resolve(ctx, aadObjectID, displayName)` 方法，执行四级身份解析逻辑。
* 提供 `Learn(aadObjectID, email)` 方法，用于在 ListGroupMembers（读取群成员）等操作中获取到邮箱时，自动注入缓存。

### 3. Graph API 客户端扩展
* **`internal/channels/teams/graph.go`**: 增加 `GetUserEmail(ctx, aadObjectID)` 方法，当缓存不命中且配置了 Graph Client 时，请求 `/users/{id}?$select=mail,userPrincipalName` 接口。

### 4. 接入身份转换
* **`internal/channels/teams/channel.go`**: 在 `New` 初始化通道时创建 `UserResolver`。同时，在 `ListGroupMembers` 方法中，调用 `resolver.Learn` 自动学习群成员的邮箱并存入缓存。
* **`internal/channels/teams/webhook.go`**: 在处理接收到的消息时，调用 `c.resolver.Resolve` 将原本的 `AAD Object ID` 替换为解析后的邮箱（或兜底名），作为 `SenderID` 流转入 GoClaw。

---

## 验证计划

1. **自动化单元测试**：
   - 编写 `TestUserResolver`，覆盖：静态映射匹配、缓存命中、Graph API 模拟请求以及未知用户显示名兜底逻辑。
   - 运行测试命令确保通过：
     ```bash
     go test -v ./internal/channels/teams/...
     ```
2. **构建测试**：
   - 确认整个项目能够正常编译：
     ```bash
     go build -o /dev/null ./...
     ```
