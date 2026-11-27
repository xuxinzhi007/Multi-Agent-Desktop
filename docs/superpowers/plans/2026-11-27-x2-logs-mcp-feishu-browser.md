# ACK 服务日志 MCP、内置浏览器下线与飞书凭证归口 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans or subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建跨 CLI / 桌面端 / 飞书机器人 / Cursor 四端共用的 `x2-logs-mcp` 日志查询能力，干净下线内置 WebKit 浏览器，并将飞书用户态 OAuth 凭证统一归口到 `~/.feishu-mcp/credentials.json`。

**Architecture:** 新增 `internal/x2logs` 与独立二进制 `cmd/x2-logs-mcp`（标准 MCP 2024-11-05 stdio 协议），在 `main.go` 启动阶段统一挂载插件与 `x2-logs-mcp` 到共享 `agent.Manager`；移除 `internal/browser`、`internal/tool/web` 及前端 Workbench 入口；抽离 `internal/feishu/mcpstore.go` 统一读写 `~/.feishu-mcp/credentials.json`。

**Tech Stack:** Go 1.24, MCP JSON-RPC 2.0 (stdio), React 19 + TypeScript (Vite)

---

### Task 1: 实现 `internal/x2logs` 客户端与凭证存储（TDD）
- **Files:**
  - Create: `internal/x2logs/creds.go`
  - Create: `internal/x2logs/client.go`
  - Create: `internal/x2logs/client_test.go`
- **Steps:**
  - [ ] 编写失败单测覆盖 JWT 解析、凭据持久化、`QueryLogs` 请求序列化、尾随逗号重试、401 自动刷新重试与日志格式化截断。
  - [ ] 实现 `CredStore`（支持 `X2_DEVOPS_TOKEN`、`~/.x2-devops/credentials.json` 与 Chromium LocalStorage LevelDB Snappy 提取）与 `Client`。
  - [ ] 运行 `go test ./internal/x2logs -v` 确认全部通过。

### Task 2: 实现 `cmd/x2-logs-mcp` 服务端并接入 CLI / 桌面 / 飞书 / Cursor（TDD）
- **Files:**
  - Create: `cmd/x2-logs-mcp/main.go`
  - Create: `cmd/x2-logs-mcp/main_test.go`
  - Modify: `main.go`
  - Modify: `cli_tui.go`
- **Steps:**
  - [ ] 编写 `cmd/x2-logs-mcp/main_test.go`，通过 `internal/mcp.Start` 端到端测试 `x2_query_logs`、`x2_list_log_targets`、`x2_logs_auth`。
  - [ ] 实现 `cmd/x2-logs-mcp/main.go` 并编译到 `~/.multi-agent-desktop/bin/x2-logs-mcp`。
  - [ ] 在 `main.go` 补齐共享 `plugin.Manager` 注入与 `x2-logs-mcp` 自动注册，在 `cli_tui.go` 增加 `/logs` 快捷指令，同步注册到 `~/.cursor/mcp.json`。

### Task 3: 下线内置浏览器 `internal/browser` 与 `internal/tool/web`
- **Files:**
  - Delete: `internal/browser/*`, `internal/tool/web/*`
  - Modify: `main.go`, `api_skill.go`, `internal/agent/loop_backend.go`, `internal/agent/delegate.go`, `src/App.tsx`, `src/components/Sidebar.tsx`, `src/mygo.ts`, `.github/workflows/ci.yml`
- **Steps:**
  - [ ] 清理所有 Go 后端与 React 前端对 `browser.Desk` / `OpenWorkbench` / `web.New()` 的引用并删除对应目录。
  - [ ] 运行 `go test ./internal/agent/...` 与前端类型检查确认零残留。

### Task 4: 飞书用户态 OAuth 凭证归口至 `~/.feishu-mcp/credentials.json`（TDD）
- **Files:**
  - Create: `internal/feishu/mcpstore.go`
  - Create: `internal/feishu/mcpstore_test.go`
  - Modify: `api_feishu.go`
- **Steps:**
  - [ ] 编写 `internal/feishu/mcpstore_test.go` 验证多账号读写、过期比较与原子落盘。
  - [ ] 实现 `internal/feishu/mcpstore.go` 并重构 `api_feishu.go` 复用统一凭证读写层。
  - [ ] 全量运行 `go test`、前端构建检查与线上只读日志查询实测验证。
