# 设计文档：multi-agent-desktop

> 版本：0.3.0（MyGo 迁移后）
> 最后更新：2026-11-12

---

## 1. 产品定位

跨平台（macOS / Windows）的多 Agent 开发工具桌面程序。
N 个 AI agent 并行工作，各自独立会话，用户可实时观察、干预、审批。

**当前形态**：多 agent 聊天 + shell 执行 + 检查器面板（演示数据）
**目标形态**：agent 能读写代码、自动拆解任务、git worktree 隔离

---

## 2. 架构决策

### 2.1 壳：MyGo（不是 Tauri / Electron）

| 候选 | 结论 | 原因 |
|---|---|---|
| Tauri 2 | ❌ 放弃 | 无法跨平台交叉打包（出 Windows 包必须在 Windows 上构建） |
| Electron | ❌ 未选 | 344MB 体积（Grok Bot 实测），UI 效果与壳无关 |
| **MyGo** | ✅ **选定** | 纯 Go 无 cgo，一条命令出 macOS + Windows + Linux |

**关键认知**：UI 效果取决于前端代码（CSS 变量数量、组件设计），不取决于壳。
Grok Bot（Electron）好看是因为 1553 个设计变量，不是 Chromium 的功劳。

### 2.2 进程模型：单 Go 进程

```
MyGo 主进程（Go）
├── agent.Manager     —— N 个 agent 会话，每个一个 goroutine
├── llm.Client        —— mock / ollama / openai 兼容
├── pty.Runner        —— shell 执行
└── mygo.Bind()       —— 暴露 API 给前端

React 19 前端（Vite 构建）
├── mygo-runtime      —— typed IPC client（自动生成）
└── events.*.on()     —— 订阅 Go 端事件
```

**优势**：
- 无跨进程通信（之前 Tauri 要 WebSocket + 端口）
- 无序列化开销（typed IPC 直接调 Go 方法）
- 协程天然适合多 agent 并行

### 2.3 通信协议：MyGo typed IPC

**不用 WebSocket / REST**，用 MyGo 的 `Bind` + `Event`：

```go
// Go 端
mygo.Bind(Sidecar{})  // 暴露 Sidecar 的方法给前端

var AgentCreated = mygo.NewEvent[Agent]("agent.created")
AgentCreated.Emit(win, agent)  // 推给前端
```

```ts
// 前端（自动生成 src/mygo.ts）
await Sidecar.createAgent({ name: "前端", role: "..." })
events.agentCreated.on((agent) => { ... })
```

**为什么不用 WebSocket**：
- 类型不安全（JSON 字符串，运行时才发现错）
- 要维护连接状态、重连逻辑
- MyGo IPC 是编译期类型安全的

---

## 3. 设计系统

### 3.1 分层策略

参考 Cursor / Grok Bot 的命名规律：

```
--bg-{base|chrome|surface|elevated|inset}     背景层级（越靠上层越亮）
--border-{subtle|default|strong|focus}        边框
--text-{primary|secondary|tertiary|disabled}  文字层级
--{success|warning|danger|info}-{,dim,border} 语义色三件套
--diff-{add,del}-{bg,text}                    diff 专用
--agent-1..6                                  agent 身份色
```

### 3.2 主题机制

**CSS 变量 + `data-theme` 属性切换**：

```css
:root[data-theme="dark"] {
  --bg-base: #0a0c10;
  --text-primary: #e8ebf1;
  /* ... */
}

:root[data-theme="light"] {
  --bg-base: #f5f6f8;
  --text-primary: #1a1d23;
  /* ... */
}
```

**切换方式**：
1. 系统跟随（`prefers-color-scheme` 媒体查询）
2. 手动切换（设置里选 暗色 / 亮色 / 跟随系统）

**MyGo 集成**：
- MyGo 检测系统主题变化，通过 `mygo:theme` 事件推给前端
- 前端监听并更新 `data-theme`

### 3.3 组件规范

- **前缀**：`c-`（component），如 `c-btn`、`c-badge`
- **禁止硬编码色值**：一律 `var(--token)`
- **内联样式**：仅用于动态值（如 `agent.color`），静态样式走 CSS 类

---

## 4. 数据流

### 4.1 单一状态源

`useSidecar.ts` 是唯一状态管理：
- 订阅 MyGo events → 更新 React state
- 暴露 `send(action, data)` → 调 Mygo 方法

```
Go Event → useSidecar reducer → React state → 组件渲染
                ↑
         用户操作 → send() → Go 方法
```

### 4.2 事件命名

| 事件 | 方向 | 含义 |
|---|---|---|
| `agent.created` | Go → 前端 | 新会话 |
| `agent.deleted` | Go → 前端 | 删除会话 |
| `agent.status` | Go → 前端 | 状态变化（idle/running/done/error） |
| `agent.delta` | Go → 前端 | 流式片段 |
| `agent.message` | Go → 前端 | 完整消息落定 |
| `agent.error` | Go → 前端 | 错误 |
| `shell.start` / `shell.result` | Go → 前端 | 命令执行 |
| `llm.config` / `workdir` | Go → 前端 | 配置变更 |

---

## 5. 多 Agent 并发模型

### 5.1 实现：Queue + Worker（已落地）


**每个 agent 一个消息队列**：

```go
type Agent struct {
    // ... 现有字段 ...
    queue   chan PromptRequest  // 待处理消息
    worker  *Worker             // 消费 goroutine
    status  Status              // idle / running / queued
}

type Worker struct {
    agent   *Agent
    current context.CancelFunc  // 当前任务的取消函数
}

func (w *Worker) Run() {
    for req := range w.agent.queue {
        w.execute(req)  // 阻塞执行，完成后取下一个
    }
}
```

**状态机**：
```
idle --Prompt--> running --完成--> idle
  ↑                                  |
  |-------- Prompt（排队）--------> queued
```

**用户操作**：
- **发送**：入队，如果 idle 立即执行，否则 queued
- **停止**：取消当前 running，清空 queue
- **跳过**：取消当前，取 queue 下一个

### 5.2 Agent 间协作（已落地）

**现状**：`Manager.Route(from, to, text)` 已实现，消息带 `[from X]` / `[to X]` 前缀，UI 里显示为路由气泡。

**能力矩阵**：

| 能力 | 说明 | 实现 |
|---|---|---|
| **发现** | 知道有哪些 agent | `Manager.List()` 注入 |
| **寻址** | @某个 agent | `Message.toAgentId` |
| **发送** | 主动推消息 | `Manager.Route(from, to, msg)` |
| **触发** | 让另一个 agent 工作 | `Manager.Prompt(toId, text)` |

**架构**：
```go
// Agent 增加协作能力
type Agent struct {
    // ... 现有字段 ...
    manager *Manager  // 反向引用，用于寻址其他 agent
}

// Manager 增加路由
func (m *Manager) Route(fromId, toId, text string) error {
    msg := Message{
        From: fromId,
        To:   toId,
        Text: text,
    }
    // 推给目标 agent 的 queue
    return m.Prompt(toId, PromptRequest{Text: text})
}
```

**使用场景**：
```
用户: "@Supervisor 实现登录功能"
Supervisor: "拆解任务：@前端 写UI，@后端 写API"
  → Manager.Route("supervisor", "frontend", "写登录页面")
  → Manager.Route("supervisor", "backend", "写登录接口")
前端: "完成，代码在 login.tsx"
  → Manager.Route("frontend", "supervisor", "已完成")
```

### 5.3 群聊 vs 私聊（已落地）

**两层并存**：

```
频道（Channel）          私聊（DM）
  #general               @前端 ↔ 用户
  #frontend              @前端 ↔ @后端
```

**Channel 实现**（`internal/channel/channel.go`）：
```go
type Channel struct {
    ID       string    `json:"id"`
    Name     string    `json:"name"`
    Members  []string  `json:"members"`  // agent IDs
    Messages []Message `json:"messages"` // 共享历史
}

// 发消息到频道：返回创建的消息 + 除发送者外的成员 ID
// 即使 0 个成员也会记录消息（用户能看到自己发的）
func (m *Manager) Broadcast(channelId, fromId, fromName, text string) (*Message, []string, error)
```
持久化：`<dataDir>/channels.json`；`DeleteAgent` 会调用 `RemoveMember` 清理残留成员。

**前端 UI**：
- 侧栏加「频道」分组（类似 Slack）
- 频道视图显示所有成员消息
- @提及高亮，点击可跳转私聊

---

### 5.4 事件广播

`agent.Manager` 持 `Emitter` 函数，把事件转成 MyGo `Event.Emit`：
- 解耦：agent 包不依赖 MyGo
- 可测试：单测时换成 channel 收集事件
- `emitToWindow` 只做「字符串事件 → 强类型 payload → Emit」的转发，每个 `toXxx(data)` 是纯函数，`win == nil` 时可单测
- **新子系统不走 `map[string]any` 协议**：channel / knowledge 事件直接在 Sidecar 层用 typed `Event.Emit`

### 5.5 会话级配置（单一事实源）

配置的作用域是**会话**，不是全局：

| 字段 | 作用域 | 回落 |
|---|---|---|
| `Agent.WorkDir` | 单会话的 CLI / shell 目录 | 空则用 `Manager.workDir`（全局默认） |
| `Agent.LLMCfg` | 单会话的模型提供方 / 模型 | `nil` 则用 `Manager.llmCfg`（设置里的默认） |

前端在 AgentView 头部用两个 chip（📁 / ✦）就地编辑，走 `agent.workDir` / `agent.llm` 协议，
后端回 `agent.config` 事件（携带完整 `AgentView`）。设置弹窗只保留**外观**与**全局默认提供方**。

> **持久化白名单**：`Agent` 新增字段必须同步 4 处 —— `Save` / `Load` / `snapshot` / `viewMap`，
> 否则会静默丢字段。`internal/agent/manager_test.go` 的 round-trip 测试守住这个不变量。

### 5.6 知识库

`internal/knowledge` 提供人类编写的短笔记（约定 / 术语 / 偏好）：

- 存储：`<dataDir>/knowledge.jsonl`（追加 O(1) 且崩溃安全，删除时整体重写）
- 注入：`Manager.SetKnowledge(KnowledgeProvider)` —— **接口**注入，`agent` 包零新增依赖
- 时机：`Create` 时把 top-8 全局笔记前置到 system prompt
- 运行时：`AttachNoteToAgent` 把单条笔记追加为 system message

---

---

## 6. 交互体验设计

### 6.1 流式输出

**原则**：不打断用户阅读，不强制滚动。

**实现**：
- 跟踪用户是否在底部（`pinnedToBottom`）
- 在底部：新内容自动跟随滚动
- 不在底部：保持当前位置，不强制跳转
- 用户回到底部：恢复自动跟随

**代码**：
```tsx
const pinnedToBottom = useRef(true)

const handleScroll = () => {
  const el = scrollRef.current
  if (!el) return
  const threshold = 40
  pinnedToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < threshold
}

useEffect(() => {
  if (pinnedToBottom.current) {
    el.scrollTop = el.scrollHeight
  }
}, [streaming])
```

### 6.2 连续对话

**状态展示**：
```
[运行中] 帮我看看 A...        ← 当前执行
[排队中] 还有 B               ← 等待
[排队中] 一起总结一下          ← 等待
```

**操作**：
- **点击排队项**：可删除或提前
- **停止按钮**：取消当前 + 清空队列
- **跳过按钮**：取消当前，执行下一个

### 6.3 Agent 状态可视化

**侧栏状态点**：
- 灰色：空闲
- 橙色闪烁：执行中
- 蓝色：有排队消息
- 绿色：完成
- 红色：出错

**检查器联动**：
- 点击 agent → 右侧显示该 agent 的改动/任务/工具
- 多个 agent 同时运行 → 检查器可切换查看

### 6.4 打断与恢复

**用户打断**：
- 点击「停止」→ 取消当前 stream，agent 状态回 idle
- 已生成的内容保留（作为 assistant 消息入历史）
- 未开始的消息仍在队列

**Agent 被打断**（被其他 agent @）：
- 如果正在执行，新消息排队
- 如果空闲，立即执行
- 优先级：用户直接消息 > 其他 agent 消息

---

## 7. 已知限制 & 下一步

| 限制 | 状态 | 位置 |
|---|---|---|
| 连续对话会乱序（无队列） | ✅ 完成 | `internal/agent/manager.go` `workerLoop` |
| agent 不能互相通信 | ✅ 完成 | `Manager.Route` |
| 会话不持久化 | ✅ 完成 | `Manager.SetStore` / `agents.json` |
| 无群聊/频道 | ✅ 完成 | `internal/channel` + `ChannelView.tsx` |
| 配置只能全局设置 | ✅ 完成 | `Agent.WorkDir` / `Agent.LLMCfg` + 会话头部 chip |
| 无知识库 | ✅ 完成 | `internal/knowledge` + `KnowledgePanel.tsx` |
| 检查器面板是演示数据 | 🟡 中 | 接 CLI agent 产生真实数据 |
| agent 不能读写代码 | 🟡 中 | 加 tools 包 |
| 无 git worktree 隔离 | 🟡 中 | worktree 包 |
| LLMCfg 的 API Key 明文落盘 | 🟡 中 | 迁移到 `mygo.Secrets`（`// TODO(secrets)`） |
| 频道是广播触发，非讨论收敛 | 🟡 中 | 语义保持为「广播」，不做多轮收敛编排 |

---

## 8. 参考

- MyGo: https://github.com/egoist/mygo
- 调研: docs/research.md
- 交接: HANDOVER.md
