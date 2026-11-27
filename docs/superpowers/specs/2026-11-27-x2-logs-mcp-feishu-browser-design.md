# ACK 服务日志 MCP、内置浏览器下线与飞书凭证归口设计方案

**日期**: 2026-11-27  
**范围**: `cmd/x2-logs-mcp` + `internal/x2logs` + `cli_tui.go` + `main.go` + 下线 `internal/browser` & `internal/tool/web` + 飞书 OAuth 凭证单一事实来源归口

---

## 1. 背景与目标

当前系统在日常研发排障与多端协同中存在三处架构痛点：

1. **生产/测试 ACK 服务日志查询缺乏工具化入口**：每次排查 `data-service` 等服务日志都需要打开 `https://devops.x2robot.com/logs` 手工选下拉框或复制粘贴日志，CLI、桌面端与飞书机器人都无法直接按 `traceId / keyword / SLS` 拉取真实日志辅助定位。
2. **内置 WebKit 浏览器（`internal/browser` + `internal/tool/web`）定位过时且占内存**：独立 WebKit 窗口与用户日常 Chrome 不共享登录态，且额外拉起一整块渲染进程；在已有无头 `web_fetch`、`feishu-mcp` 与内部平台专用 MCP 后，维护内置浏览器的收益已低于成本。
3. **桌面端飞书凭证与 `feishu-mcp` 双写割裂**：主动工具调用（读写飞书文档、知识库、发消息）走 `feishu-mcp` 更自然，但用户 OAuth 凭证分散在 `config.json` 与 `~/.feishu-mcp/credentials.json` 两处，需明确“主动工具走 MCP、被动长连接保留在 `internal/feishu`”的清晰边界并收口凭证源。

**核心目标**：
- **一处实现、四端复用**：构建纯 Go 编译版 `x2-logs-mcp` 标准 MCP Server，同时供 **CLI (`-headless`)**、**桌面端对话**、**飞书长连接机器人**、以及 **Cursor IDE** 直接调用，并在 CLI 提供 `/logs` 快捷指令。
- **平滑下线内置浏览器**：移除 `internal/browser`、`internal/tool/web` 及桌面侧边栏入口，释放多窗口 WebView 复杂度与内存风险。
- **飞书架构清晰分层**：保留 `internal/feishu` 入站 WebSocket 机器人与审批卡片回调，将用户态凭证单一事实来源统一归口至 `~/.feishu-mcp/credentials.json`，确保 CLI / 桌面 / 飞书工具链行为完全一致。

---

## 2. 实测验证的 `devops.x2robot.com` 接口契约

经对 `https://devops.x2robot.com` 前端产物与线上只读接口实测核验，真实 HTTP 协议如下（顶层直接返回 JSON，无 `{code, data}` 外壳）：

### 2.1 鉴权机制
- 请求头：`Authorization: Bearer <JWT>`
- Token 结构：`devops.x2robot.com` 通过飞书 SSO 签发的 HS256 JWT（含 `open_id`、`union_id`、`name`、`iat`、`exp`，有效期 `86400s = 24h`），浏览器端保存在 `localStorage["devops_admin_token"]`。
- 续期与获取路径：
  1. 环境变量 `X2_DEVOPS_TOKEN`（最高优先级，便于 CI/容器注入）；
  2. 本地凭据文件 `~/.x2-devops/credentials.json`（权限 `0600`，保存 `baseUrl`、`token`、`expiresAt`）；
  3. **零感自动发现（macOS）**：当本地缓存 JWT 缺失或已过期（`now >= exp - 60`）时，自动扫描本机 Chrome / Edge / Arc 的 `Local Storage/leveldb`（支持 Snappy 解压读取 `devops_admin_token`），若用户在浏览器登录过 `devops.x2robot.com`，无需手动复制即可自动续上最新有效 JWT；
  4. **交互式兜底**：支持 MCP 工具 `x2_logs_auth`（查看状态、写入新 Token、或调 `GET /api/v1/auth/login-url` 生成飞书单点登录链接）及 CLI `/logs auth <token>` 一键更新。

### 2.2 核心接口清单

| 用途 | 方法与路径 | 请求参数 | 响应结构 |
| --- | --- | --- | --- |
| 数据源列表 | `GET /api/v1/observability/logs/sources` | 无 | `{"sources": [{"id": "default", "project": "...", "logstore": "k8s-stdout", "displayName": "k8s-stdout", "enabled": true}, ...]}` |
| Project 别名 | `GET /api/v1/observability/logs/project-aliases` | 无 | `{"aliases": [{"project": "...", "alias": "云平台集群日志"}]}` |
| 环境（命名空间） | `POST /api/v1/observability/logs/environments` | `{"source": "default"}` | `{"environments": ["ns-cloud-prod", ...]}` |
| 服务列表 | `POST /api/v1/observability/logs/services` | `{"source": "default", "environment": "ns-cloud-prod"}` | `{"services": ["data-service", ...]}` |
| 实例（Pod）列表 | `POST /api/v1/observability/logs/instances` | `{"source": "default", "environment": "ns-cloud-prod", "imageVersion": ""}` | `{"instances": ["data-service-xxx", ...]}` |
| **日志查询** | `POST /api/v1/observability/logs/query` | 见下方 2.3 | `{"logs": [{"time": "...", "level": "...", "service": "...", "instance": "...", "message": "..."}], "total": 123}` |

### 2.3 `POST /api/v1/observability/logs/query` 请求体
```json
{
  "source": "default",
  "environment": "ns-cloud-prod",
  "service": "data-service",
  "imageVersion": "",
  "instance": "",
  "keyword": "eps-ddkxhrn5zo7qk5lb",
  "queryMode": "keyword",
  "timeRange": "15m",
  "startTime": "1795760000",
  "endTime": "1795763600",
  "page": 1,
  "pageSize": 50
}
```
- `source`: 默认为 `"default"`（即 `云平台集群日志 / k8s-stdout`），也支持传 `"bv103-log"` 等显示名自动匹配 `id`。
- `queryMode`: `"keyword"`（默认，关键词检索；**注意**：若用户传入末尾带英文逗号如 `eps-xxx,`，工具支持原样查询及提示去掉尾逗号重试）或 `"sls"`（阿里云 SLS 查询语法，如 `_container_name_: data-service and ERROR`）。
- `timeRange`: `"realtime"` | `"15m"` | `"1h"` | `"6h"` | `"24h"` | `"7d"` | `"custom"`（`custom` 时传 `startTime` / `endTime`，不传 `timeRange`）。
- `pageSize`: 默认 `50`，最大限制 `200`，并对单条超长堆栈做合理截断以防撑爆大模型上下文窗口。

---

## 3. 分阶段模块设计

### 阶段一：`x2-logs-mcp` 服务与全端挂载

1. **核心包 `internal/x2logs`**：
   - `Client`：封装 `ListSources`、`ListEnvironments`、`ListServices`、`ListInstances`、`QueryLogs`，内置超时控制（15s）与人类可读格式化输出（时间戳、级别、服务、`[Pod]`、日志正文）。
   - `CredStore`：读写 `~/.x2-devops/credentials.json`（`0600`），解析 JWT `exp`、`name`、`open_id`，并在过期时尝试本机 Chromium LevelDB（纯 Go Snappy 解块）自动提取最新 `devops_admin_token`。
2. **独立二进制 `cmd/x2-logs-mcp`**：
   - 实现标准 MCP `2024-11-05` JSON-RPC 2.0 over stdio 服务端（零第三方重型依赖）。
   - 暴露 3 个聚焦工具：
     - `x2_query_logs`：按环境（默认 `ns-cloud-prod`）、服务（如 `data-service`）、关键词/SLS、时间范围（默认 `15m` 或 `realtime`）、Pod 实例查询 ACK 日志。
     - `x2_list_log_targets`：列出可用的数据源（Project/Logstore）、环境（Namespace）、服务名或 Pod 实例列表，防止模型瞎猜服务名或环境名。
     - `x2_logs_auth`：检查当前 JWT 有效性与剩余时长、手动写入新 Bearer Token、或获取飞书 SSO 登录 URL。
3. **三端 + Cursor 自动接入**：
   - `main.go`：桌面与 CLI 启动时同时挂载 `plugin.Manager` 到共享 `agent.Manager`（补齐此前仅 `cli_tui.go` 挂载插件工具的缺口），并确保当本机存在 `x2-logs-mcp` 二进制时自动注册到 `config.json` 的 MCP 列表，使 **CLI、桌面端、飞书机器人**立即可用。
   - CLI 快捷指令 `/logs`（`cli_tui.go`）：
     - `/logs status`：查看当前日志鉴权状态与有效时长；
     - `/logs auth <BearerToken>`：一键更新保存 Token；
     - `/logs <关键词或自然语言条件>`：直接触发日志查询或交由主对话结合 `x2_query_logs` 排查分析。

### 阶段二：下线内置浏览器（`internal/browser` & `internal/tool/web`）

1. **后端清理**：
   - 删除 `internal/browser/` 与 `internal/tool/web/` 包；
   - 从 `main.go` 移除 `api.desk`、`OpenWorkbench` 及 `browser-page.json` 初始化；
   - 从 `internal/agent/loop_backend.go`、`internal/agent/delegate.go`、`api_skill.go` 移除 `web` 工具注册与状态项（保留无头纯 HTTP `web_fetch` 工具）。
2. **前端与构建清理**：
   - 从 `src/App.tsx`、`src/components/Sidebar.tsx`、`src/mygo.ts` 移除“打开内置浏览器（Workbench）”按钮与 RPC 绑定；
   - 从 `.github/workflows/ci.yml` 测试矩阵中移除 `./internal/browser` 与 `./internal/tool/web`。

### 阶段三：飞书能力边界收敛与凭证归口

1. **职责边界固化**：
   - **主动工具层（100% 走 `feishu-mcp`）**：读写飞书云文档、知识库、表格、主动发飞书消息全部统一由 `feishu-mcp` 提供，不在 Go 内核重复造飞书业务 Tool。
   - **被动通道层（保留 `internal/feishu`）**：保留 WebSocket 长连接收消息、会话路由绑定、写操作审批卡片回调与图片附件下载（MCP 属于 Pull 模型，无法替代常驻入站事件流）。
2. **凭证单一事实来源归口（`~/.feishu-mcp/credentials.json`）**：
   - 抽离 `internal/feishu/mcpstore.go`，将 `~/.feishu-mcp/credentials.json` 作为用户态 OAuth Token（`access_token` / `refresh_token` / `expires_at` / `open_id`）的主存储；
   - 桌面端完成设备授权（`FinishFeishuUserAuth`）或自动刷新 Token 时直接原子写入 `~/.feishu-mcp/credentials.json`，桌面端读取用户授权状态时也直接从该主存储水合，彻底消除 CLI 与桌面端 Token 不一致或过期不同步问题。

---

## 4. 错误处理与测试策略

- **错误处理**：
  - HTTP `401/403` 或 JWT 过期：先尝试自动从浏览器 LocalStorage 刷新一次并重试；仍失败则返回清晰的中文指引（附 `/logs auth <token>` 命令说明）。
  - 查询结果为空且 `keyword` 末尾带英文逗号（如 `eps-ddkxhrn5zo7qk5lb,`）：自动提示或回退去掉尾随标点再查一次，避免用户从页面复制带逗号 ID 导致漏查。
  - 日志单次返回过大：总字节数控制在安全上限内（默认 16KB），保留最新关键行并提示总命中条数 `total` 与缩小 `timeRange`/`service` 的建议。
- **测试覆盖**：
  - `internal/x2logs/client_test.go`：使用 `httptest.Server` 完整测试 `sources`、`environments`、`services`、`query` 请求序列化、尾逗号容错、401 自动刷新重试与输出截断。
  - `cmd/x2-logs-mcp/main_test.go`：端到端拉起 `x2-logs-mcp` 子进程，通过 `internal/mcp.Start` 跑通 `initialize` -> `tools/list` -> `tools/call` 全链路握手。
  - 回归验证：移除内置浏览器后全仓 `go test`、`npm run build`（或前端类型检查 `tsc --noEmit`）与 `go build` 零报错通过。
