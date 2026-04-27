# 钉钉用户 ID 适配：组织中心集成 (运维与配置指南)

本文档描述了如何配置、管理和测试钉钉通道的“组织中心 (Organization Center)”身份转换功能。

## 1. 功能概述

系统通过以下流程将钉钉的 `staffId` 转换为业务系统所需的 `person_code`：

1.  **缓存检查**：首先检查内存和本地文件缓存。
2.  **获取手机号**：调用钉钉 API 通过 `staffId` 获取用户绑定的手机号。
3.  **组织中心查询**：通过手机号调用组织中心接口，获取 `person_code`。
4.  **持久化**：将映射关系保存到本地 JSON 文件以便调试。

## 2. 配置参数详解

配置位于钉钉渠道实例的 `config` 字段中。

| 字段         | 类型   | 说明                                               | 示例                                     |
| :----------- | :----- | :------------------------------------------------- | :--------------------------------------- |
| `enabled`    | bool   | 是否启用身份转换                                   | `true`                                   |
| `mode`       | string | 运行模式：`real` (调用 API) 或 `mock` (返回固定值) | `"real"`                                 |
| `endpoint`   | string | 组织中心 API 的根地址                              | `"https://orgcenter.capitaleco-pro.com"` |
| `access_key` | string | 组织中心 API Header: `Access-Key`                  | `"7z87iM1q3A03rR1W"`                     |
| `secret_key` | string | 组织中心 API Header: `Secret-Key`                  | `"zNun20NW8elk76XoLG6BVPACeHGlZrxa"`     |
| `mock_code`  | string | `mock` 模式下为所有用户返回的固定代码              | `"161533"`                               |
| `ttl_hours`  | int    | 身份映射的缓存时长（单位：小时）                   | `100`                                    |

## 3. 如何修改配置 (运维操作)

由于该配置属于渠道实例的私有配置，目前建议通过以下两种方式之一进行修改：

### 方式 A：通过 API 修改 (推荐)

这是最安全的方式。假设你的 GoClaw 地址是 `http://localhost:18790`，网关 Token 为 `YOUR_TOKEN`。

1.  **获取当前配置**：
    ```bash
    curl -H "Authorization: Bearer YOUR_TOKEN" \
         http://localhost:18790/v1/channels/instances/{INSTANCE_ID}
    ```
2.  **更新配置**：
    将 `org_center` 块合并到 `config` 字段中并提交。
    ```bash
    curl -X PUT -H "Authorization: Bearer YOUR_TOKEN" \
         -H "Content-Type: application/json" \
         -d '{
           "config": {
             "org_center": {
               "enabled": true,
               "mode": "real",
               "endpoint": "https://orgcenter.capitaleco-pro.com",
               "access_key": "...",
               "secret_key": "...",
               "ttl_hours": 100
             }
           }
         }' \
         http://localhost:18790/v1/channels/instances/{INSTANCE_ID}
    ```

### 方式 B：直接在数据库中修改 (仅限紧急情况)

如果无法访问 Dashboard 或 API，可以直接修改数据库（以 PostgreSQL 为例）：

```sql
-- 1. 查找钉钉实例 ID
SELECT id, name FROM channel_instances WHERE channel_type = 'dingtalk';

-- 2. 更新 config 字段 (注意：这会覆盖原有的 JSON，请先备份)
UPDATE channel_instances
SET config = config || '{
  "org_center": {
    "enabled": true,
    "mode": "real",
    "endpoint": "https://orgcenter.capitaleco-pro.com",
    "access_key": "...",
    "secret_key": "...",
    "ttl_hours": 100
  }
}'::jsonb
WHERE id = '你的实例ID';
```

## 4. 调试与验证

### 4.1 本地映射文件

系统会将所有成功的转换记录在本地：

- **路径**: `~/.goclaw/data/dingtalk_identity_mappings.json` (或 Docker 容器内的 `/app/data/dingtalk_identity_mappings.json`)
- **内容示例**:
  ```json
  {
    "mappings": {
      "manager123": {
        "staff_id": "manager123",
        "mobile": "13800138000",
        "person_code": "161533",
        "updated_at": "2024-04-27T10:00:00Z"
      }
    }
  }
  ```

### 4.2 通过 API 测试

你可以调用 GoClaw 的内部调试接口或直接观察日志。

**模拟消息流测试**：
发送一条钉钉消息给机器人。观察后台日志：

- `[Dingtalk] identity mapping hit (cache): staffId=xxx -> personCode=yyy` 表示命中缓存。
- `[Dingtalk] lookup user info: staffId=xxx -> mobile=***` 表示正在调用钉钉 API。
- `[Dingtalk] lookup person code: mobile=*** -> personCode=yyy` 表示正在调用组织中心。

## 5. 常见问题 (FAQ)

- **Q: 如果组织中心挂了怎么办？**
  - A: 系统会自动降级，记录错误后继续使用原始 `staffId` 发送消息，不会导致服务中断。
- **Q: 修改配置后需要重启吗？**
  - A: **不需要**。GoClaw 的渠道驱动支持热加载配置，但在某些版本中，为了确保缓存完全刷新，建议在修改后点击 Dashboard 上的 "Reconnect" 按钮。
- **Q: 缓存多久失效？**
  - A: 由 `ttl_hours` 控制，默认 100 小时。重启服务会清空内存缓存，但会从本地 JSON 文件中重新加载。
