# 运行时 API 契约（native / ACP）

本文件冻结了「会话选择运行时」这件事的 Go API、持久化字段、事件映射与错误语义。
前端、后端、外部接入方都按这里实现；改契约必须先改这份文件。

- 协议层文档：`docs/runtime-protocol.md`（事件与 Session 状态机）
- 本文只写「外部可观测」的部分：API、JSON 字段、事件、错误、前端待接事项

---

## 1. 会话运行时的取值

| 值 | 含义 | 执行路径 |
|---|---|---|
| `native`（默认，字段缺失也是它） | 进程内 loop | `rt.Native` 驱动现有 `internal/loop` |
| `acp` | 外部 ACP Agent（grok / codex-acp / dsh 等） | `internal/runtime/acp` 拉起子进程 |

`Runtime` 与旧的 `Backend` 是两件事：

- `Backend` 仍是 `loop` / `mock` / `supervisor` / `doubao`，决定「用哪种模型或哪套内核」。
- `Runtime == "acp"` 时**覆盖** backend，走外部 Agent；切回 `native` 后 backend 语义照旧。
- 老 `agents.json` 没有 `runtime` 字段，读入后按 `native`，不需要迁移脚本。

## 2. 持久化字段（`agents.json`）

```json
{
  "runtime": "acp",
  "acp": {
    "name": "grok",
    "command": "grok",
    "args": ["acp"],
    "env": ["KEY=VALUE"],
    "trace": true
  }
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `runtime` | string | `native` / `acp`；缺省按 `native` |
| `acp.name` | string | 展示名，空则用 `command` 的 basename |
| `acp.command` | string | 可执行文件。Windows 上允许 `.cmd` / `.bat`（内部经 `cmd.exe /c` 启动） |
| `acp.args` | string[] | 启动参数，原样传给子进程 |
| `acp.env` | string[] | `KEY=VALUE`，追加到子进程环境；不会清空继承环境 |
| `acp.trace` | bool | 为真时把 ACP 原始帧写到 `<数据目录>/acp/<agentId>.jsonl` |

规则：

- 切到 `native` 时**保留** `acp` 配置，用户切回来不用重填。
- `args` / `env` 去空白、去空项后保存。
- 明文落盘与 `agents.json` 其余内容一致（自用项目，不做钥匙串）。

## 3. 前端可见视图（`AgentView`）

```go
type AgentView struct {
    // …现有字段不变…
    Runtime string     `json:"runtime"`       // 恒为 "native" 或 "acp"
    ACP     *ACPConfig `json:"acp,omitempty"` // 原样回传，供编辑表单回填
    // 运行时连接状态（只读、不落盘）。取值：
    //   ""        没有运行过（首次启动前）
    //   "starting" ACP 进程正在拉起 / 握手
    //   "running"  本轮正在执行
    //   "idle"     上一次启动成功、当前空闲
    //   "error"    上一次启动或运行失败，原因见 runtimeError
    RuntimeState string `json:"runtimeState,omitempty"`
    RuntimeError string `json:"runtimeError,omitempty"`
}
```

- `runtime` **恒非空**：底层空值在出视图时归一成 `native`。
- `acp` 只在配置过时出现；未配置过 ACP 的会话不返回该字段。
- `runtimeState` / `runtimeError` 是运行期状态，**不写进 `agents.json`**；进程重启后回到 `""`。
- ACP 过程中「授权 / 登录」失败由 `runtimeError` 原文表达（前端可据关键词提示「需要登录」，但不要凭空断言）。
- 所有 `AgentView` 出口都要带这些字段：`ListAgents`、`CreateAgent`、`GetAgentConfig` 系列、`agent.config` 事件、`agent.profile` 事件。

## 4. API

### 4.1 请求结构

```go
type AgentRuntimeRequest struct {
    AgentID string   `json:"agentId"`
    Runtime string   `json:"runtime"`           // native | acp
    Name    string   `json:"name,omitempty"`
    Command string   `json:"command,omitempty"`
    Args    []string `json:"args,omitempty"`
    Env     []string `json:"env,omitempty"`
    Trace   bool     `json:"trace,omitempty"`
}
```

### 4.2 方法

```go
// SetAgentRuntime 设置会话运行时；成功后返回完整 AgentView 并广播 agent.config。
func (s *API) SetAgentRuntime(req AgentRuntimeRequest) (AgentView, error)
```

生成后的 TS 调用形如：

```ts
await API.setAgentRuntime({
  agentId: agent.id,
  runtime: 'acp',
  name: 'grok',
  command: 'grok',
  args: ['acp'],
})
```

### 4.3 错误语义（全部是中文可读文案，不允许 panic / 500 / 静默成功）

| 情况 | 行为 |
|---|---|
| `runtime` 不是 `native`/`acp`（API 要求显式二选一，空串也报错） | 返回错误：`运行时只能是 native 或 acp` |
| `runtime == "acp"` 且 `command` 去空白为空 | 返回错误：`这个会话选了外部 ACP Agent，但还没填写启动命令` |
| 会话 id 不存在 | 返回错误：`agent <id> not found` |
| 成功 | 返回最新 `AgentView`，并发 `agent.config`（`catalogView`，不含消息） |
| 持久化失败 | 返回错误，但内存状态已改；前端应提示「已生效但未保存」不可依赖 |

### 4.4 运行期的 ACP 错误（走对话流，不是 API 错误）

ACP 是「启动时才报错」的能力，因此下列情况以 `agent.error` + `turn.finished(error)` 呈现，不阻塞输入：

| 情况 | 文案 |
|---|---|
| 选了 acp 但没有命令 | `这个会话选了外部 ACP Agent，但还没填写启动命令。在会话设置里填写后再试。` |
| 命令不存在 / 没有执行权限 / 握手失败 | `无法启动 ACP Agent（命令：<cmd>）：<原始错误>` |
| 没有工作目录 | `这个会话还没有文件夹`（与 native 一致） |
| ACP 进程中途退出 | `Agent 连接断开：<原因>`，随后本轮以 `error` 结束 |

## 5. 执行与生命周期

| 事项 | native | acp |
|---|---|---|
| 会话粒度 | 每轮一个 `rt.Native` 会话 | 每轮一个子进程（`acp.Runtime.Start`），轮末 `Close` |
| 提交 | `Session.Prompt` 提交即返回 | 同左 |
| 停止 | `runCtx` 取消 → `Session.Cancel("")` → loop 在边界收尾 | `Session.Cancel` 发 `session/cancel`，`Close` 杀进程组 |
| 删除会话 | 取消 + 关闭会话 | 取消 + 关闭会话（进程组回收） |
| 轮末状态 | `idle`（队列非空则 `queued`） | 同左 |
| 附件 / 图片 | 沿用现有 blocks | **本轮不支持**，只发送文本；不要假装支持 |
| 计划（plan）事件 | loop 不产出 | ACP `plan` 事件存在，但前端不显示，忽略 |
| 用量（usage） | 前端按消息自算 | ACP `usage_update` 忽略 |

进程回收不依赖 Agent 上的长期字段：每轮 `defer Session.Close()`，停止与删除都由取消 ctx 触发同一条路径。

### 5.1 `runtimeState` 的写点

| 时机 | 值 | 说明 |
|---|---|---|
| `acp.Runtime.Start` 前 | `starting` | 让界面能显示「正在连接」 |
| 握手成功、开始 `Prompt` | `running` | |
| 收到 `turn.finished`（任意 reason） | `idle` | `runtimeError` 清空 |
| 启动失败 / 事件流报错 / 进程异常退出 | `error` | 同时写 `runtimeError`（原始错误或中文说明） |
| 用户停止 | 先保持 `running` 到 `turn.finished` | 收尾后回 `idle` |

每次状态切换发一次 `agent.config`（`catalogView`），不要每个 delta 都发。
native 会话可以留空，也可以按同样规则 `running/idle`，但前端不应依赖 native 的这一位。

## 6. 事件映射（协议 → 现有前端事件）

前端 `src/lib/useBackend.ts` 只认旧事件；ACP 与 native 都必须映射到同一套，**不新增事件类型**。

| 运行时事件 | 前端事件 | 备注 |
|---|---|---|
| `turn.started` | — | `execute` 已发 `agent.status running` |
| `text.delta` | `agent.delta {id, delta, channelId}` | 同时累加 `Chars` |
| `thought.delta` | `agent.reasoning {id, reasoning}` | |
| `tool.started` | `agent.tool {id, tool, phase:"start", content}` | `content` 取标题，缺省用工具名；同时更新 `LastTool` |
| `tool.updated` | — | 前端只认 start/end |
| `tool.finished` | `agent.tool {id, tool, phase:"end", status, content}` | status：native 保留 `ok/error/timeout/denied/truncated`；ACP `completed→ok`、`failed→error`、`cancelled→denied`；`content` 截 400 字 |
| `plan.updated` | — | |
| `permission.request` | `tool.approval` / `tool.approval.done`（由 `approval.Center` 发） | 见 §7 |
| `usage` | — | |
| `notice` | `agent.error`（仅 `severity=error`） | 其余忽略 |
| `error` | 暂存，随 `agent.error` 落一次 | 避免同一条错误出现两次 |
| `turn.finished` | `agent.message` / `agent.stopped` / `agent.error` + 状态回写 | 见 §5 |

实现细节：

- `runtimeEventFromLoop` 在 `tool.started/finished` 的 `meta` 里带原始 `loopToolStatus`，否则 `rt.ToolStatus` 会丢掉 `denied/truncated/timeout` 的区别。
- 工具 `id` 在事件里是 agent id（与现有 `agent.tool` 一致）；工具调用本身的 id 只用于内部配对。

## 7. ACP 审批桥

`permission.request` 一律经 `approval.Center`，**不允许**在 Center 之外自动放行：

```go
allow, reason := appr.Approve(turnCtx, a.ID, a.Name, tool.Call{
    Name: "acp",
    Preview: tool.Preview{
        Title:  perm.Title,       // 空则「外部 Agent 请求权限」
        Detail: perm.Detail,      // 原始 rawInput JSON
    },
})
```

选项选择：

| 用户决定 | 回传给 Agent 的 optionId |
|---|---|
| 允许 | 优先 `allow_once` → `allow_always` → 任一 `allow_*` |
| 拒绝 | 优先 `reject_once` → `reject_always` → 任一 `reject_*` |
| 超时 / 停止 / 会话关闭 / 无可用选项 | `""`（协议按 `cancelled` 回传） |

`auto` 模式仍由 `approval.Center` 的既有语义决定（显式配置才自动）；`readonly` 一律拒绝。审批卡片复用现有 `ApprovalModal`，不新建 UI。

## 8. 前端待接清单（本契约只负责契约，UI 由前端线实现）

1. 新建会话弹窗：运行时二选一（原生 / 外部 ACP），ACP 下填 名称 / 命令 / 参数 / 环境变量；命令为空禁止提交并就地提示。
2. 会话编辑：读取 `AgentView.acp` 回填，保存调用 `API.setAgentRuntime`，失败时展示后端原文。
3. 会话展示：`原生` / `ACP · <name|basename>` 徽标；未配置命令也如实显示，方便用户补。
4. 流式文本、思考、工具开始/结束、审批卡：**复用**现有组件与事件，不新建重复实现。
5. 颜色只用 `src/index.css` 的 token。

## 9. 验证

```powershell
go build ./...
go test ./internal/runtime/... ./internal/tool/... ./internal/agent/... -count=1
npx mygo generate   # 改 Go 结构体后必须重新生成 src/mygo.ts
npm run typecheck
```

`go test -race` 在 Windows 需要 cgo；本地用普通 `go test`，CI 上再跑 race。
