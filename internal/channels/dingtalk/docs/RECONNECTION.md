# DingTalk Stream 重连与保活机制

## 1. 核心问题：静默断连
钉钉 WebSocket 连接在长时间无消息时，容易因为网络抖动、负载均衡策略或服务端 TTL 而断开。Go SDK 默认的心跳检测（120秒）在 TCP 半开（Half-open）状态下反应太慢，导致连接已经失效但客户端仍认为在线。

## 2. 保活策略
为了提高稳定性，我们在 SDK 之上增加了应用层的健康监控：

- **活跃时间刷新 (`lastActivityTime`)**：
  - 拦截所有通过 Stream 收到的 DataFrame（包含业务消息、事件、以及 SYSTEM 类型的系统消息）。
  - 只要收到任何数据包，即更新活跃时间。
- **健康检测轮询 (`healthMonitorLoop`)**：
  - 每 **30 秒** 进行一次心跳检查。
  - **90 秒超时**：如果连续 90 秒没有收到任何消息，认为连接已死亡。
  - 这种策略比 SDK 的 120s Ping 更激进，能更快感知到静默断连。

## 3. 重连流程
当检测到心跳超时或 SDK 抛出不可恢复错误时：

1. **重建 Client**：关闭旧的 `StreamClient`，彻底销毁旧的 WebSocket 资源。
2. **指数退避重试**：
   - 初始等待：3 秒。
   - 递增公式：$min(3s * 2^{attempt}, 30s)$。
   - 在重连期间，通过 `OnStatusChange` 回调通知频道进入 `Degraded`（降级）状态。
3. **状态恢复**：重连成功后，自动恢复为 `Healthy` 状态，并重置活跃时间计时器。

## 4. 状态反馈
连接状态与 GoClaw 健康看板集成：
- **Healthy**: 连接正常，显示 "Connected (Stream Mode)"。
- **Degraded**: 正在尝试重连，显示具体断连时长及重连尝试次数。
- **Failed**: 初始连接认证失败（AppKey/AppSecret 错误）。
