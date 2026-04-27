Edited todo.md
Ran command: `gs`
Ran command: `vim .gitignore`
Ran command: `cd deleteme`
Ran command: `ls`
Ran command: `cp -r ~/.openclaw/extensions/dingtalk-connector .`
Viewed todo.md:1-20
Listed directory media
Viewed image.ts:1-82
Viewed common.ts:1-155
Viewed card.ts:1-343
Viewed channel.ts:1-463
Viewed send.ts:1-142
Viewed message-handler.ts:1-800

现在我已经充分理解了 TS 版本的媒体处理逻辑，可以给出清晰的分析了。

---

## 分析：DingTalk 图片支持机制

### 一、图片的两个方向

| 方向                    | 描述                                                                           |
| ----------------------- | ------------------------------------------------------------------------------ |
| **入站（用户 → 模型）** | 用户在钉钉发图片，GoClaw 需要把图片下载下来，作为 URL 或本地路径传给 LLM       |
| **出站（模型 → 用户）** | LLM 生成了图片（本地路径），GoClaw 需要上传到钉钉媒体服务，再在 AI Card 里渲染 |

---

### 二、入站图片：`downloadCode` 机制

TS 版本的完整流程如下：

```
钉钉 Stream 事件
  ↓ msgtype = "picture" / "richText"
  ↓ content.downloadCode = "xxx"          ← 钉钉 OSS 资产凭证
  ↓
POST /v1.0/robot/messageFiles/download    ← 用 robotCode + downloadCode 换取临时下载 URL
  ↓ resp.downloadUrl = "https://..."      ← 有时效的 OSS 直链
  ↓
HTTP GET downloadUrl                      ← 下载图片到本地
  ↓
保存到 agentWorkspaceDir/media/inbound/openclaw-media-{timestamp}.jpg
  ↓
作为本地文件路径 传给 LLM (附带 file:// 前缀)
```

**关键 API**：

- `POST https://api.dingtalk.com/v1.0/robot/messageFiles/download`
- Body: `{ downloadCode, robotCode }`
- 返回: `{ downloadUrl }`

**GoClaw Go 版本现状**：
当前 `internal/channels/dingtalk/messaging.go` 只提取了文本内容，**没有处理 `picture` 和 `richText` 类型中的 `downloadCode`**，图片消息会被完全忽略。

---

### 三、出站图片：上传到钉钉媒体服务

TS 版本的出站流程：

1. LLM 返回包含本地路径的 markdown，如 `![图片](/tmp/output/foo.png)`
2. `processLocalImages()` 函数扫描内容中的 `LOCAL_IMAGE_RE` 匹配项
3. 调用 `POST https://oapi.dingtalk.com/media/upload` 上传图片，获得 `media_id`（如 `@xxx`，需去掉 `@` 前缀）
4. 将路径替换为 `![alt](media_id)` 的形式，AI Card 的 Markdown 渲染器能识别 `media_id` 并展示图片

**关键 API**：

- `POST https://oapi.dingtalk.com/media/upload`
- Params: `access_token=xxx&type=image`
- Form: `media` 字段上传文件
- 返回: `{ media_id: "@xxx" }`（需去掉 `@` 前缀）

**文件保存位置（TS 版本）**：

- 入站缓存：`{agentWorkspaceDir}/media/inbound/` （GoClaw 里等价于进程临时目录）
- 出站上传：本地文件直接读取，不缓存，上传后只保留 `media_id`

---

### 四、Go 版本需要实现的工作

| 任务                                                            | 位置                                                       | 说明                                       |
| --------------------------------------------------------------- | ---------------------------------------------------------- | ------------------------------------------ |
| **入站**：解析 `picture`/`richText` 消息中的 `downloadCode`     | `messaging.go`                                             | 目前只处理了 `text`，图片被丢弃            |
| **入站**：调用 `/v1.0/robot/messageFiles/download` 换取下载 URL | `client.go` (新增方法)                                     | 需要 `robotCode` + `downloadCode`          |
| **入站**：下载图片到临时目录，以 `file://` 路径传给 LLM         | `messaging.go`                                             | 需要用到 GoClaw 的 media/temp 机制         |
| **出站**：扫描 LLM 输出中的本地图片路径                         | `streaming.go` (在 `Stop` 前) 或 `dingtalk.go` 的 `Send()` | 仿照 `processLocalImages()`                |
| **出站**：上传图片到 `oapi.dingtalk.com/media/upload`           | `client.go` (新增方法)                                     | 需要旧版 OAPI Token (非 API Token)         |
| **出站**：替换 AI Card 内容中的本地路径为 `media_id`            | 同上                                                       | 替换后的格式: `![alt](media_id_without_@)` |

---

**建议的优先级**：先做**入站图片**（用户体验直接可感知），再做**出站图片**（出站依赖 OAPI Token 的获取，稍复杂一些）。

Viewed message-handler.ts:692-755
Viewed media.ts:1-800

现在可以回答你的两个问题了：

---

### 一、钉钉 OSS 是免费的吗？

**是的，完全免费。**

这里涉及两个不同的 API：

| API                                      | 用途                                                           | 收费情况                               |
| ---------------------------------------- | -------------------------------------------------------------- | -------------------------------------- |
| `POST /v1.0/robot/messageFiles/download` | `downloadCode` 换取临时 OSS 下载 URL（入站，下载用户发的图片） | 免费，属于 Stream 模式机器人的基础功能 |
| `POST oapi.dingtalk.com/media/upload`    | 上传本地文件，获取 `media_id`（出站，上传模型生成的图片）      | 免费，属于消息推送基础 API             |
| `down.dingtalk.com/media/{cleanMediaId}` | 通过 media_id 构造的图片展示 URL                               | 免费，下载链接由钉钉 OSS 提供          |

**本质上**：钉钉把媒体文件托管在自己的 OSS（阿里云对象存储）上，对企业内部机器人使用是免费的。这是钉钉作为企业协作平台的基础能力，不计费。

---

### 二、这些接口在 `deleteme/dingtalk` 中实现了吗？

**是的，都已完整实现**，而且实现得很完善：

#### 入站（用户图片 → 模型）✅ 已实现

- `downloadMediaByCode()` in `message-handler.ts`：用 `downloadCode` 换 URL，再下载到本地
- `downloadImageToFile()` in `message-handler.ts`：从 OSS URL 下载图片到 `{agentWorkspaceDir}/media/inbound/`
- `extractMessageContent()` 完整支持 `picture`、`richText`、`audio`、`video`、`file` 等所有消息类型

#### 出站（模型图片 → 用户）✅ 已实现

- `uploadMediaToDingTalk()` in `media.ts`：上传本地文件到 `oapi.dingtalk.com/media/upload`，获取 `media_id` + `downloadUrl`
- `processLocalImages()` in `media.ts`：扫描 LLM 输出中的本地图片路径，自动上传并把路径替换为钉钉下载 URL（`https://down.dingtalk.com/media/{cleanMediaId}`）
- `processVideoMarkers()` / `processAudioMarkers()` / `processFileMarkers()`：支持视频、音频、文件的完整上传发送流程（带 ffmpeg 封面生成）

#### 与 Go 版本的差距总结

Go 版本的 `messaging.go` 目前**只处理了 `text` 类型消息**，`picture`/`richText` 中的 `downloadCode` 被完全忽略。`client.go` 里也没有对应的 `DownloadMediaByCode()` 和 `UploadMedia()` 方法。

也就是说，**TS 版本的这套逻辑是完整的参考实现，可以直接对照移植到 Go**。
