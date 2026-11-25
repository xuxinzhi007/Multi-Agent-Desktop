# 内部运行时协议 `mad.runtime/1`

本文是桌面端与 Agent 运行时之间的内部契约，以及 native / ACP 两条接入路径的实现说明。

代码位置：

- `internal/runtime/protocol.go` —— 事件、会话、错误、校验
- `internal/runtime/sink.go` —— Seq 分配、阻塞发送、关闭时序
- `internal/runtime/native.go` —— 进程内执行体适配器
- `internal/runtime/acp/` —— ACP（JSON-RPC over stdio）客户端
- `internal/agent/loop_runtime.go` —— 现有 loop 事件到协议事件的映射
- `cmd/acp-probe` —— 接新 Agent 时的最小验证工具

---

## 1. 为什么需要这一层

现在有三套东西混在一起：

| 层 | 现状 | 问题 |
|---|---|---|
| 前端 ↔ Go | MyGo typed IPC（`src/mygo.ts`，105 个 API） | 只能进程内用，外部编辑器接不进来 |
| Go ↔ 模型 | `internal/llm` 各 provider | 只管一次模型调用，不管任务生命周期 |
| Go ↔ 执行体 | `internal/loop` + `loopEmitter` 直接发 `agent.*` 事件 | 事件形状与 loop 绑死，换 ACP 就要改前端 |

`mad.runtime/1` 只解决第三层：把「谁在执行」抽象成可替换的 `Runtime`，
把输出统一成一套 `Event`。它不改前端 IPC，也不替代 ACP。

非目标：

- 不做插件内核（Cordis 那套收益/成本比不划算）
- 不做多机传输（以后要多机，在 Transport 后面换 gRPC 即可，接口不变）
- 不接管模型协议（Chat Completions / Responses / Anthropic 仍在 `internal/llm`）

---

## 2. 版本与兼容规则

```go
const Version = "mad.runtime/1"
```

规则：

1. **只增不改**：新增事件类型或字段可以，改名/删字段不行。
2. **未知字段忽略**：`json.Unmarshal` 默认行为，不做严格校验。
3. **未知事件降级**：`NormalizeKind` 把不认识的类型转成 `notice`，并带上原因，
   不允许因为对端升级就断线。
4. **ACP 版本独立**：ACP 的 `protocolVersion` 是整数 1，与本协议版本号无关。
   客户端只接受 `protocolVersion == 1`，其他值启动即失败。
5. **发出前校验**：`Event.Validate()` 检查必需字段。适配器发无效事件应视为 bug。

`SessionID` 始终是**本地会话 ID**（通常是 agent id），不是 ACP 的 `sessionId`。
ACP 的 sessionId 只存在于适配器内部，避免前端被对端标识污染。

---

## 3. 事件模型

### 3.1 公共字段

| 字段 | 类型 | 说明 |
|---|---|---|
| `v` | string | 固定 `mad.runtime/1` |
| `seq` | int64 | 会话内单调递增，由 `EventSink` 唯一分配 |
| `sessionId` | string | 本地会话 ID |
| `turnId` | string | 一轮的标识；同一轮的 delta / 工具 / 审批共享 |
| `kind` | string | 事件类型 |
| `at` | int64 | 毫秒时间戳 |
| `meta` | map | 扩展信息，前端可忽略 |

`seq` 的作用是去重与乱序保护：前端按 `seq` 丢弃重复帧即可，不需要自己数 token。

### 3.2 事件类型

| kind | 必需字段 | 语义 |
|---|---|---|
| `turn.started` | — | 一轮开始；UI 进入 running |
| `text.delta` | `text` | 正式回复的增量文本 |
| `thought.delta` | `text` | 推理/思考增量，不进入最终答案 |
| `tool.started` | `tool.id`、`tool.status` | 工具开始；`status` 一般是 `running` |
| `tool.updated` | `tool.id` | 工具中间状态（如 ACP 的 `in_progress`） |
| `tool.finished` | `tool.id` | 工具结束；`status` 为 `completed/failed/cancelled` |
| `plan.updated` | `plan[]` | 计划项变化；用于 plan 模式 UI |
| `permission.request` | `permission.id`、`permission.options[]` | 审批请求，见 §5 |
| `notice` | `notice.title` | 给用户看的提示，不进历史 |
| `usage` | `usage.used`、`usage.size` | 上下文/成本快照 |
| `turn.finished` | `stop.reason` | 一轮结束；`stop.finalText` 是本轮完整文本 |
| `error` | `error.message` | 可展示错误；通常后面还会跟 `turn.finished(error)` |

一轮的典型顺序：

```
turn.started
  thought.delta*
  text.delta*
  tool.started -> tool.updated* -> tool.finished
  permission.request (可选，阻塞到用户选择)
  plan.updated (可选)
  usage (可选)
turn.finished
```

`stop.reason` 取值：

| 值 | 来源 |
|---|---|
| `end_turn` | 正常说完 |
| `max_tokens` / `max_turn_requests` / `refusal` | 与 ACP 对齐 |
| `cancelled` | 用户停止 |
| `error` | 执行失败 |
| `budget_exhausted` | 进程内 loop 的轮次/工具/墙钟预算用尽（本层扩展） |

### 3.3 工具对象

```json
{
  "id": "call-1",
  "name": "bash",
  "title": "运行 go test",
  "status": "completed",
  "input": {"command": "go test ./..."},
  "output": "ok ...",
  "exitCode": 0,
  "durationMs": 1200,
  "locations": [{"path": "internal/runtime/acp/client.go", "line": 12}]
}
```

`id` 是配对的唯一依据。同一 `id` 的 `tool.started` / `tool.updated` / `tool.finished`
必须落在同一个 `turnId`。

---

## 4. 会话状态机

```
Start ──> idle ──Prompt──> running ──turn.finished──> idle
             │                 │
             │                 ├─Cancel──> cancelling ──turn.finished(cancelled)──> idle
             │                 └─permission.request──> waiting ──RespondPermission──> running
             │
             └──Close──> closed（events channel 关闭）

running 时再次 Prompt ——> ErrBusy（不排队）
```

约定：

- `Prompt` **提交成功即返回**，不等这一轮结束。
  这样 UI 在模型输出时仍然可以 `Cancel` / `RespondPermission`。
- 一轮没结束时再 `Prompt` 返回 `ErrBusy`，由上层排队（现有 Queue/Worker 已经这么做）。
- `Close` 之后 `Prompt` 返回 `ErrClosed`，`Events()` 最终关闭。
- `Cancel(turnID)` 传空表示「停当前这轮」；传具体 ID 且不匹配则无操作。

---

## 5. 审批（权限）

审批请求带的是**选项列表**，不是布尔值：

```json
{
  "kind": "permission.request",
  "permission": {
    "id": "c7",
    "toolCallId": "call-9",
    "title": "运行命令",
    "detail": "{\"command\":\"rm -rf build\"}",
    "options": [
      {"id": "allow-once", "label": "允许一次", "kind": "allow_once"},
      {"id": "reject", "label": "拒绝", "kind": "reject_once"}
    ]
  }
}
```

`RespondPermission(requestID, optionID)` 回传的是 **option 的 id**，不是 kind。
原因：对端可能给出多个同 kind 但语义不同的选项（例如「允许并记住此规则」）。

规则：

1. **默认拒绝**。超时（ACP 默认 5 分钟）、会话关闭、用户取消，一律回 cancelled。
2. `PermissionMode` 是上限，不是下限。适配器不得把 `ask` 自行放宽成 `auto`。
3. `auto` 模式应由上层显式配置策略，并在事件流里仍然留下 `permission.request` 供审计。
4. `native` 的 `Turn.Ask` 走同一套：先发事件，再等 `RespondPermission`；
   如果有 `Native.Approver`，则由它直接决策（用于自动模式）。

---

## 6. 文件能力与安全

ACP 的 `fs/read_text_file` / `fs/write_text_file` 是 Agent 反向请求客户端的能力。

- 路径必须经 `workdir.Roots.Resolve` 校验，只能落在 `WorkDir` 或 `ExtraDirs` 内。
- 文件不存在且只有主根时，`Resolve` 会把它当作主根下的新文件（供写操作创建）。
- 写入前创建父目录，权限 `0644`；不跟随符号链接到根外（`Resolve` 已处理）。
- `terminal/*` 目前返回 `-32601 method not found`：命令执行仍由 Agent 自己的工具负责，
  客户端不额外开一个 shell 口子。
- 原始帧 trace 可能含仓库内容与命令，只应写到本地临时目录，不要上传。

进程安全：

- ACP 子进程用 `internal/proc.Setup` 设置进程组；`Close` 取消 ctx 会杀整组，
  避免 CLI 派生的 node / git 孙进程继续持有 stdout。
- Windows 上 npm 的 `.cmd/.bat` 包装脚本经 `cmd.exe /c` 启动。

---

## 7. 顺序、背压与错误

### 7.1 单一写者与 Seq

`EventSink` 是唯一出口：

```go
sink := runtime.NewEventSink(sessionID, 512)
sink.Begin()          // 登记生产者
defer sink.End()
sink.Emit(event)      // 盖 v/seq/session/at 后发出
```

- `Seq` 在 `EventSink` 内部加锁分配，适配器不要自己编号。
- `Emit` 是**阻塞发送**：消费者慢时形成背压，外部 Agent 再快也不会把内存堆爆。
- `Close` 先关 `stop`，阻塞中的 `Emit` 立刻返回 `ErrClosed`，再等所有生产者 `End` 后关 channel。

### 7.2 缓冲大小

默认 512 帧。它只吸收抖动，不是队列。真正的背压来自「发送阻塞 → 子进程 stdout 管道阻塞」。
不要为了「不卡」把缓冲调到几万，那只是把内存问题推迟。

### 7.3 错误

| 哨兵 | 含义 |
|---|---|
| `ErrBusy` | 上一轮还没结束 |
| `ErrClosed` | 会话已关闭 |
| `ErrUnsupported` | 适配器不支持该操作 |

传输断开时：

1. 发一条 `notice(error)` 说明连接断了；
2. 如果有一轮在跑，补 `error` + `turn.finished(error)`；
3. 关闭事件流。

前端不需要区分「模型失败」和「进程崩了」——两者都会以 `turn.finished(error)` 收尾。

---

## 8. ACP 映射

### 8.1 客户端方法

| 时机 | 方法 | 说明 |
|---|---|---|
| Start | `initialize` | 声明 `protocolVersion: 1`，`fs.readTextFile/writeTextFile: true`，`auth.terminal: false` |
| Start | `session/new` | `cwd` 为主根；对端声明 `additionalDirectories` 时才带附加根 |
| Prompt | `session/prompt` | `prompt` 是 content block 数组；结果 `stopReason` |
| Cancel | `session/cancel` | 通知，不等回复 |
| Close | — | 直接杀进程；不假设对端实现 `session/close` |

### 8.2 `session/update` → 协议事件

| ACP `sessionUpdate` | 协议事件 |
|---|---|
| `agent_message_chunk` | `text.delta`（同时累加 `stop.finalText`） |
| `agent_thought_chunk` | `thought.delta` |
| `user_message_chunk` | 忽略（本地已有用户输入） |
| `tool_call` | `tool.started` |
| `tool_call_update`（非终态） | `tool.updated` |
| `tool_call_update`（`completed/failed/cancelled`） | `tool.finished` |
| `plan` | `plan.updated` |
| `usage_update` | `usage` |
| `notice` | `notice` |
| 其他（`available_commands_update` 等） | 忽略 |

工具状态映射：

| ACP | 协议 |
|---|---|
| `pending` | `pending` |
| `in_progress` | `running` |
| `completed` | `completed` |
| `failed` | `failed` |
| `cancelled` | `cancelled` |

### 8.3 Agent → Client 反向请求

| 方法 | 处理 |
|---|---|
| `session/request_permission` | 发 `permission.request`，阻塞等 `RespondPermission`，回 `{outcome:{outcome:"selected",optionId}}` 或 `cancelled` |
| `fs/read_text_file` | `Roots.Resolve` → 读文件；支持 `line` / `limit` |
| `fs/write_text_file` | `Roots.Resolve` → 建目录 → 写文件 |
| `terminal/*` | `-32601 method not found` |
| 其他 | `-32601` |

### 8.4 一个进程一个会话

`acp.Runtime.Start` 每次起一个新进程。当前实现是**每轮一个进程**：一轮结束（含停止、失败）立即 `Close`，
理由：

- 崩溃隔离：一个 Agent 挂了不影响别的会话；
- 登录态隔离：不同 Agent 的凭据不混；
- 事件路由简单：不需要按 `sessionId` 多路复用。

代价是内存与启动时间。以后要复用进程，只需把 `Session` 的 `sessionID` 路由改成
「按对端 sessionId 分发到多个 EventSink」，接口不变。

---

## 9. native loop 已接入统一协议（2026-11-25）

现状：

- `internal/agent/loop_backend.go` 的 `prepareNativeLoop` 完成原有装配（模型/目录校验、工具注册表、系统提示、检查点、loop 配置），返回一个 `rt.ExecFunc`；
- `executeNativeRuntime` 用 `rt.Native` 启动会话：`Prompt` 提交即返回，事件经 `EventSink` 出来，`consumeRuntimeEvents` 翻译回现有 UI 事件，`turn.finished` 收尾，`Close` 结束；
- `executeLoop` 只保留为兼容入口，内部直接转到 `executeNativeRuntime`；`loopEmitter` 已移除，`runtimeEventFromLoop` 是唯一的 loop → 协议翻译层（`tool.finished` 额外带 `meta.loopToolStatus`，保留 `denied / truncated / timeout` 的区别）；
- 落库消息、检查点、审计、频道回复、学习摘要仍沿用原语义；`stop`/`budget` 仍分别以 `agent.stopped` 与 `agent.error` 呈现。

`Manager` 的选择顺序是 `EffectiveRuntime()`：`Runtime == "acp"` 覆盖 backend；其余按 backend 走 `executeMock / executeSupervisor / executeDoubao`。字段、API 与错误语义见 `docs/runtime-api-contract.md`。

下面是接入初期的形态，保留作为新增 Runtime 的参考。它覆盖：

| loop event | 协议事件 |
|---|---|
| `delta` | `text.delta` |
| `reasoning` | `thought.delta` |
| `tool.start` | `tool.started`（带 `Call.ID`，已补进 loop 事件） |
| `tool.end` | `tool.finished`（`StatusOK/Truncated → completed`，`Denied → cancelled`，其余 `failed`） |
| `iter.start` | `usage`（仅当有 `CtxLimit`） |
| `done` | `turn.finished(end_turn)` |
| `stopped` | `turn.finished(cancelled / budget_exhausted)` |
| `failed` | `turn.finished(error)` + `error` |

切换到统一协议的最小改法（不改 loop 本体）：
```go
exec := func(ctx context.Context, spec rt.Spec, turn rt.Turn, emit func(rt.Event) error) (rt.Stop, error) {
    r := &loop.Runner{ /* 与现在 executeLoop 相同的装配 */ }
    r.Emitter = func(ev loop.Event) {
        if e, ok := runtimeEventFromLoop(ev); ok {
            if e.TurnID == "" { e.TurnID = turn.ID }
            _ = emit(e)
        }
    }
    st := r.Run(ctx, state)
    return rt.Stop{Reason: stopReasonFromLoop(st), FinalText: st.FinalText}, nil
}

n := &rt.Native{Exec: exec, Approver: autoApprover}
sess, _ := n.Start(ctx, spec)
```

当前实现已按同一思路落地（见本节开头的现状）。新增 Runtime 时沿用同一套：
`Exec` 只负责装配与事件翻译，生命周期（Prompt / Cancel / Close / Seq）交给 `rt.Native`。

---

## 10. 接入 ACP Agent

桌面端不在代码里写死命令：会话的 `ACPConfig`（name / command / args / env / trace）
经 `SetAgentRuntime` 落盘到 `agents.json`，`executeACP` 每轮构造下面的 `acp.Runtime`。
`command` 为空时直接给中文错误，不启动进程。审批桥在 `runtime_backend.go` 的
`handleRuntimePermission`：允许取 `allow_once → allow_always → allow_*`，拒绝取
`reject_once → reject_always → reject_*`；没有可用选项、超时、用户停止、会话关闭
一律回 `""`（对端按 cancelled 处理），不会自动放行。原始帧 trace 写到
`<数据目录>/acp/<agentId>.jsonl`。

```go
r := &acp.Runtime{
    Command: "grok",              // 或 codex-acp / dsh
    Args:    []string{"acp"},
    Options: acp.Options{Trace: traceFile},
}
sess, err := r.Start(ctx, rt.Spec{ID: "agent-1", WorkDir: "/repo"})
sess.Prompt(ctx, rt.Prompt{Text: "修掉这个测试"})
for ev := range sess.Events() {
    switch ev.Kind {
    case rt.KindPermission:
        sess.RespondPermission(ev.Permission.ID, "allow-once")
    case rt.KindTurnFinished:
        return
    }
}
```

命令行验证（不启动桌面应用）：

```bash
go run ./cmd/acp-probe -cmd grok -arg acp -cwd . -prompt "列出目录"
go run ./cmd/acp-probe -cmd codex-acp -cwd . -allow -trace /tmp/acp.jsonl
```

`-trace` 会写出每一帧收发记录，排错时先看这个：
先确认 `initialize` 的 `protocolVersion`，再看 `session/new` 的 `sessionId`，
最后看 `session/update` 是否符合 §8.2。

---

## 11. 新增一个 Runtime 的检查清单

1. `Name()` 可读，能区分具体后端。
2. `Start` 校验 `spec.ID`、`spec.WorkDir`，错误信息带原因。
3. 所有事件走 `EventSink`，不自己造 channel / 不自己编 `seq`。
4. `Prompt` 提交即返回，结束用 `turn.finished`。
5. `Cancel` 真的能停：取消 ctx + 杀进程组，不能只改一个标志位。
6. `Close` 幂等；关闭后 `Events()` 会关闭。
7. 审批默认拒绝；超时、断线、关闭都要回 cancelled。
8. 文件访问走 `workdir.Roots`，不直接 `filepath.Join`。
9. 对端未知事件要忽略或降级，不能断线。
10. 有 trace 开关，原始帧可回放。
11. 至少覆盖：正常一轮、审批、取消、进程异常退出、未知事件。

---

## 12. 决策记录（为什么不是自研外部协议）

| 方案 | 结论 | 原因 |
|---|---|---|
| 完全自研外部协议 | 否 | 规范/版本/适配器都要自己维护，且没有生态；协议不提升编码能力 |
| 直接全用 ACP，删掉 native | 否 | 飞书、频道、学习摘要等能力在 native 侧；ACP 不覆盖 |
| **内部统一契约 + ACP 边界** | 是 | 内部可控、外部可接；编码质量靠 harness 与工具面补齐 |

关键判断：

- 编码能力的瓶颈是 **模型与 harness 匹配度、工具面、验证闭环**，不是协议格式。
- 协议的收益在于「接一次，接进所有说 ACP 的 Agent」，所以外部边界必须复用标准。
- 内部可以自研，因为只有我们自己消费；但要窄、要版本化、要可回放。

---

## 13. 验证

```bash
go test ./internal/runtime/... ./internal/tool/... ./internal/agent/... -count=1
go build ./...
go run ./cmd/acp-probe -h
```

当前覆盖：

- `internal/runtime`：事件校验、未知 kind 降级、native 流式/忙/取消/审批/关闭
- `internal/runtime/acp`：握手、流式文本与工具、计划、用量、审批往返、取消、
  未知 update 容错、版本不匹配、`fs` 读写
- `internal/agent`：loop 事件映射；native 经协议跑通一轮并回到 idle；ACP 未配置命令报中文错误；
  ACP 事件映射与收尾；只读/超时按拒绝回传；老 `agents.json` 默认 native
- `internal/tool/builtin`：grep 输出与 include/exclude、glob `**` 稳定排序、
  apply_patch 多文件增/改/删与陈旧/歧义锚点整体拒绝

`go test -race` 在 Windows 需要 `CGO_ENABLED=1` 和 C 编译器；CI 的 macOS/Linux
环境会跑，Windows 本地可先只跑普通测试。
