# 钉钉用户 ID 适配：组织中心集成 (运维与排障手册)

本文档描述了如何配置、调试和管理钉钉通道的“组织中心 (Organization Center)”身份转换功能。

## 1. 环境准备：钉钉端权限配置

在启用身份转换前，必须确保钉钉开放平台上的应用已开通相应权限，否则系统无法获取用户手机号，导致流程中断。

### 1.1 核心权限

1.  登录 [钉钉开放平台](https://open-dev.dingtalk.com/)。
2.  进入应用详情 -> **权限管理** -> **通讯录管理**。
3.  开通 **“通讯录个人信息读权限” (`Contact.User.Read`)**。
4.  **关键步骤**：修改权限后，必须在 **版本管理与发布** 页面点击 **“保存并发布”**，新权限才会生效。

---

## 2. 运行模式与调试

配置位于渠道实例的 `config` 字段中。

### 2.1 Mock 模式 (快速验证)

当你需要验证 GoClaw 内部逻辑或 Agent 对话是否正常，而不依赖外部接口时，可以使用 `mock` 模式。

- **配置示例**:
  ```json
  "org_center": {
    "enabled": true,
    "mode": "mock",
    "mock_code": "19527"
  }
  ```
- **行为**: 系统会跳过所有 API 调用，直接将该渠道的所有用户映射为 `19527`。

### 2.2 Real 模式 (生产环境)

调用真实 API 进行身份转换。

- **配置示例**:

  ```json
  "org_center": {
    "enabled": true,
    "mode": "real",
    "endpoint": "https://orgcenter.capitaleco-pro.com",
    "access_key": "YOUR_KEY",
    "secret_key": "YOUR_SECRET"
  }
  ```

  ```

  ```

---

## 3. 技术架构：身份双向映射 (ID Two-way Mapping)

在集成了组织中心后，系统必须处理两种截然不同的用户 ID，这是保证功能正常的关键设计：

1.  **内部 ID (person_code)**：
    - **用途**：用于 GoClaw 内部的 Session 管理、对话历史记录和 Agent 上下文。
    - **实现**：在接收到钉钉消息 (`processInbound`) 时，我们将原始的 `staff_id` 转换为 `person_code`，并将其设为 `userID` 和 `chatID`。因此，你在 Dashboard 中看到的 Session ID 会变为 `ding2:direct:19527`。
2.  **外部 ID (staff_id)**：
    - **用途**：用于调用钉钉官方 API 发送回复、渲染流式 AI 卡片。钉钉服务器**不认识** `person_code`，只接受 `staff_id`。
    - **实现 (反向查找机制)**：当 Agent 输出结果并尝试发送到钉钉时（`dingtalk.go` 的 `Send` 和 `streaming.go` 的 `CreateStream`），系统会拦截请求，并调用 `resolveStaffID` 方法。该方法会遍历内存中的身份映射缓存 (`idCache`)，找到与当前 `person_code` (例如 19527) 对应的原始 `staff_id` (例如 manager6580)，然后用真实的 `staff_id` 完成 API 调用。

**排障提示**：如果发现机器人在 Dashboard 里生成了回复，但钉钉端收不到，且日志提示 `card is not exist` 或类似的发送错误，通常是因为“反向查找机制”未能正确找到映射，导致使用了 `person_code` 去调用钉钉 API。

---

## 4. 缓存机制与重置 (重要!)

为了性能和 Session 稳定性，系统实现了双层缓存：

1.  **内存缓存**：服务运行时的快速查找。
2.  **本地文件**：持久化存储，路径为 `/app/data/dingtalk_identity_mappings.json`。

### 3.1 遇到的问题：修改 MockCode 后不生效

**现象**: 你将 `mock_code` 从 `161533` 改成了 `19527`，但 Session ID 依然显示 `161533`。
**原因**: 系统优先从缓存文件中加载了旧的映射关系（`manager6580` -> `161533`）。

### 3.2 如何彻底清空缓存

如果你需要重新开始身份映射，或者修改了测试用的 `mock_code`，请按以下步骤操作：

1.  **删除缓存文件**:
    ```bash
    rm /app/data/dingtalk_identity_mappings.json
    ```
2.  **重启服务或重连**:
    - 重启容器：`docker restart goclaw-goclaw-1`
    - 或在 Dashboard 渠道页面点击 **“Reconnect”**。

### 3.3 软重置技巧 (无需删除文件)

如果你无法删除本地缓存文件（例如权限受限），可以使用“软重置”方案：

1.  **修改配置**：通过 API 将 `ttl_hours` 暂时设置为 `0`。
    ```bash
    curl -X PUT -H "Authorization: Bearer YOUR_TOKEN" \
         -H "Content-Type: application/json" \
         -d '{"config": {"org_center": {"enabled": true, "ttl_hours": 0, ...}}}' \
         http://localhost:18790/v1/channels/instances/{INSTANCE_ID}
    ```
2.  **触发刷新**：给机器人发一条消息。系统会发现缓存已过期（TTL 为 0），从而强制执行全新的身份查询流程并覆盖旧缓存。
3.  **恢复配置**：确认转换成功后，将 `ttl_hours` 改回正常的数值（如 `100`）。

---

## 5. 异常降级与错误追踪

### 5.1 自动降级 (Fallback)

如果你由于以下原因导致转换失败：

- 钉钉权限不足 (403)
- 组织中心接口超时或返回错误
- 手机号在组织中心找不到匹配项

**系统行为**: 会在日志中记录 ERROR，但为了保证机器人可用，**会自动降级使用原始的 `staffId` 作为 userID**。
此时，你会看到 Session ID 依然是 `ding2:direct:managerXXXX`。

### 5.2 日志观察

通过 `docker logs` 观察关键节点：

- `[Dingtalk] lookup user info: staffId=... -> mobile=...` (调用钉钉)
- `[Dingtalk] lookup person code: mobile=... -> personCode=...` (调用组织中心)
- `[Dingtalk] identity mapping hit (cache): ...` (命中缓存)

---

## 6. 配置更新方法

### 6.1 通过 API 更新 (推荐)

```bash
curl -X PUT -H "Authorization: Bearer YOUR_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"config": {"org_center": {"enabled": true, "mode": "real", ...}}}' \
     http://localhost:18790/v1/channels/instances/{INSTANCE_ID}
```

### 6.2 通过数据库更新

```sql
UPDATE channel_instances
SET config = config || '{"org_center": {"enabled": true, "mode": "real", ...}}'::jsonb
WHERE id = '...';
```
