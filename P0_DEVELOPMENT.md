# P0 Alpha 验收记录

2026-09-09：P0 安全与可靠性实现已经冻结为 `v0.1.0-alpha.1` 候选版本。
开发和验收过程没有调用、打印或迁移真实 API Key。

| 范围 | 已落地行为 | 验证方式 |
| --- | --- | --- |
| 构建与交互 | 修复 TUI 编译、真实流式事件、工具结果关联、快捷指令审批卡死、Ctrl+C 取消 | 全量并发回归、编译与 CLI 冒烟 |
| 凭据 | Windows Credential Manager / macOS Keychain / Linux Secret Service；隐藏输入；仅持久化引用；显式迁移旧配置 | fake store 登录、迁移、失败保留和冲突测试 |
| 配置边界 | HTTPS origin 绑定、禁止重定向；拒绝项目 provider/env/凭据覆盖；状态必须位于工作区外 | endpoint、软链接、硬链接及配置越权测试 |
| 项目与 MCP 信任 | 显式指纹授权；提示词和技能消费同一份已验证快照；配置失效可撤销；MCP 未授权不启动 | 指纹变化、替换竞态、伪造状态与 stdio 回归 |
| 文件与执行 | 安全文件句柄；只读 `/input`、512 MiB tmpfs `/workspace`；断网 Docker；无宿主执行回退 | Linux 链接测试与真实容器集成测试 |
| 审批与输出 | 默认拒绝；once/turn 正确失效；审批参数只存摘要；凭据脱敏、终端控制字符转义、输出和 diff 限额 | 权限生命周期、持久化、注入与边界测试 |
| 模型传输 | 三种协议 SSE、总超时、有限重试、取消、输出上限、多工具续传、不完整回复报错 | 本地 HTTP/SSE 服务与协议夹具 |
| 会话与 MCP 生命周期 | 原子私有写入、随机 session ID、schema、错误/取消历史保留；MCP 超时停服、子进程清理 | 持久化、并发关闭、取消和恢复回归 |

Responses 加密状态和 Anthropic 签名作为协议数据保留。如果签名正文或
续传字段必须脱敏，历史仍会保存，但会明确禁止恢复，避免发送失效签名。
已知运行时凭据即使被服务端放入 opaque 字段，也不会原样落盘。

## 已执行的验收

| 检查 | 本机结果 |
| --- | --- |
| `go test -race -count=1 -timeout=5m ./...` | 通过，Windows 上全部 20 个有测试的包通过 |
| `go vet ./...` | 通过 |
| `go mod verify` | 通过 |
| `gofmt -l cmd internal` / `git diff --check` | 无格式/空白错误 |
| Windows amd64 / Linux amd64 / macOS arm64 编译 | 全部通过 |
| Linux 容器内安全测试 | config、permissions、prompt、sandbox、skills、trust、workspace 七个包通过；补足本机 Windows 权限不足而跳过的软链接用例 |
| 真实 Docker 隔离与取消 | 使用本地 `python:3.12-slim` 通过；验证断网、无宿主凭据、非 root、只读输入/根目录、tmpfs、无源码回写、取消清理容器与子进程 |
| 构建产物 CLI 冒烟 | 隔离临时配置、mock 模式下 `/help` 和 `/exit` 通过 |
| Windows 原生凭据库 | 随机合成测试项的写入、读取、删除和删除后查验通过；测试项已清理，未枚举或读取现有凭据 |

本地验收生成了 `dist/mythoscode.exe`、`dist/mythoscode-linux-amd64`、
`dist/mythoscode-darwin-arm64`；`dist/` 不进入源码仓库。Go module 已统一为
`github.com/BigSmartie/Coding-Agent`。

`.github/workflows/ci.yml` 配置了三平台 Go 检查和 Linux Docker 集成检查，
使用只读仓库权限、固定 action commit 和固定 Go 工具链。每次 push/PR 的
远端结果以目标仓库 GitHub Actions 页面为准。

## 尚未执行的验收与使用限制

- 未调用真实模型 API；live smoke 默认跳过，需显式开启并提供用户凭据。
- Windows 原生凭据库已验证；macOS Keychain / Linux Secret Service 仍需原生验收。
  原生测试可显式设置 `MYTHOS_CODE_CREDENTIAL_INTEGRATION=1` 后运行 credentials 包，
  仅创建和清理随机合成测试项。macOS 此次仅交叉编译，没有运行原生测试。
- 现有项目内旧凭据未自动迁移。首次升级可使用
  `mythoscode auth migrate --from-project`，新配置使用 `mythoscode auth login`。
  迁移冲突会拒绝覆盖，具体步骤见 [README.md](README.md)。
- 命令与 MCP 需要已启动的 Docker 及本地可信 Linux 镜像，镜像须含 `/bin/sh`、`cp`。
  运行时不拉取镜像，不联网下载依赖；Git 元数据不进入沙箱。
- 容器修改仅存在临时内存工作区，持久修改须通过审批后的文件工具完成。
  会话内容在脱敏后仍是明文，安全边界详见 [SECURITY.md](SECURITY.md)。

下一阶段是上下文压缩、持久事件日志与检查点、PTY/后台任务、受控网络能力、
更完整的 MCP 能力和真实任务成功率评测。当前结果不代表已达到 Claude Code
或 Codex 的成熟度。
