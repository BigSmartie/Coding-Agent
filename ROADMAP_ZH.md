# MyCode 路线图

MyCode 当前定位为早期 Alpha。P0 实现基线已经完成，具体测试证据和仍需原生
环境验证的项目记录在 [P0_DEVELOPMENT.md](P0_DEVELOPMENT.md)，当前信任边界
以 [SECURITY.md](SECURITY.md) 为准。

## P0：安全与可靠性基线

P0 包含可构建的终端交互、系统凭据引用、凭据 origin 绑定、项目与 MCP 显式
信任、文件修改 review、默认关闭的 Docker 命令执行、有限流式传输、重试与
取消、多工具连续性，以及可恢复的会话快照。

P0 的命令与 MCP 工作区保持临时、断网。真实 provider 和系统凭据库测试需要
用户凭据或对应平台服务，因此采用显式开启的验收方式。

## P1：长任务 Agent 运行时

P1 按依赖顺序开发。每个里程碑必须包含 RFC 与威胁模型、版本化数据协议、
迁移和回滚、确定性夹具、race/故障/对抗测试、平台集成和文档更新。

### P1.1 事件日志、检查点与模型能力

第一阶段事件日志已实现（参见 [RFC](docs/rfc/p1-1-session-journal.md)）：
持久执行事件、可重放的原子检查点、中断轮次的保守处理、schema 1 到 2 的迁移，
以及跨进程会话锁。**P1.1 尚未完成**；模型能力元数据、更完整的中断轮次
恢复和日志压缩仍待开发。

- 为回合、审批、模型调用、工具调用和任务建立 append-only 类型化事件
- 原子 checkpoint，崩溃恢复时不重复执行工具
- Session schema 迁移和跨进程锁
- 描述 context window、工具、reasoning、usage、cache 的 provider capability

### P1.2 上下文预算、压缩与分层记忆

- 请求前 token 预算和 provider usage 记账
- 保持完整 tool-call/result 组的确定性压缩
- 保护 Responses opaque state 和 Anthropic 签名
- 受信任的全局、项目、嵌套与 include memory，限制循环和体积

### P1.3 持久任务、PTY 与后台 Job

- 压缩和重启后仍一致的结构化任务状态
- 有界的 PTY/job start、attach、read、write、poll、cancel 与进程树清理
- 将选定沙箱产物经 review 导回宿主工作区

### P1.4 受控网络、Provider 一致性与远程 MCP

- 默认拒绝、按精确 origin 授权并写审计事件的出口代理
- SSRF、重定向、DNS rebinding、凭据 scope、流量与响应限额
- Anthropic、OpenAI、网关和兼容 provider 共用一套 conformance suite
- MCP capability 协商、分页、通知、取消和 Streamable HTTP

### P1.5 子代理、评测与发布工程

- 工具、路径、网络、并发和 token 预算更窄的受限子代理
- 脱敏本地 trace 与可复现真实仓库任务评测
- fuzz/故障测试、SBOM、校验和、可复现归档与签名发布

## P2：可选产品能力

- Notebook 编辑
- MCP 与受控网络之外的内置 Web Search/Fetch
- 更丰富的 IDE 集成
- 高级 prompt caching 与成本优化
- Docker 不可用时的原生 sandbox backend

## 贡献门槛

优先提交聚焦且可验证的改动。任何改变文件、进程、凭据、网络、持久状态或
模型可见信任边界的功能，都必须在同一改动中说明失败行为并加入安全测试。
