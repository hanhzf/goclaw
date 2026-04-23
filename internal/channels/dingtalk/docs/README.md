# DingTalk Channel 核心机制文档

本目录记录了钉钉频道（Stream Mode）的核心设计逻辑与实现细节。

## 目录索引

### 1. [重连与保活机制](./RECONNECTION.md)
详细说明了如何解决钉钉 WebSocket 静默断连问题，包括应用层 30s 健康监测、90s 超时判定以及指数退避重连策略。

### 2. [Thinking/Reasoning 处理](./STREAMING_AND_REASONING.md)
说明了如何通过“延迟创建（Lazy Creation）”模式处理 AI 的推理过程，实现“零空泡”输出，并配合情绪贴纸（Emotion Reply）优化用户等待体验。

### 3. [AI 卡片机制](./CARD_MECHANISM.md)
详细说明了 AI 交互卡片的生命周期、消息 ID 到卡片 TrackID 的映射管理、防重复投递机制以及内存泄漏（TTL 清理）防护。

---

## 核心原则
- **数据驱动展示**：后端仅下发业务逻辑，频道负责转换成卡片结构。
- **稳定性优先**：在 SDK 自动重连基础上增加应用层双重保险。
- **极致体验**：消除 AI 回复时的空白占位符，保持会话流简洁。
