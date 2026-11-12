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

### 5.1 Goroutine per Agent

每个 `Agent` 持独立：
- `context.CancelFunc` —— 用户点「停止」时取消
- `[]llm.Message` —— 对话历史（mutex 保护）
- `status` —— 当前状态

```go
func (m *Manager) Prompt(ctx context.Context, req PromptRequest) error {
    a := m.Get(req.AgentID)
    runCtx, cancel := context.WithCancel(ctx)
    a.cancel = cancel
    
    go func() {
        stream, _ := client.Stream(runCtx, history)
        for chunk := range stream {
            m.emit("agent.delta", ...)
        }
        m.emit("agent.message", ...)
    }()
}
```

**N 个 agent 并发 = N 个 goroutine**，无锁竞争（每个 agent 独立 mutex）。

### 5.2 事件广播

`agent.Manager` 持 `Emitter` 函数，把事件转成 MyGo `Event.Emit`：
- 解耦：agent 包不依赖 MyGo
- 可测试：单测时换成 channel 收集事件

---

## 6. 已知限制 & 下一步

| 限制 | 计划 |
|---|---|
| 检查器面板是演示数据 | W3：接 CLI agent（claude -p / codex exec）产生真实数据 |
| agent 不能读写代码 | W3：加 tools 包（read/write/grep/run_test） |
| 无 git worktree 隔离 | W3：worktree 包 |
| 会话不持久化 | W4：落盘或 localStorage |
| 未接小鲸鱼桌宠 | W4：PetWidget 组件 |

---

## 7. 参考

- MyGo: https://github.com/egoist/mygo
- 调研: docs/research.md
- 交接: HANDOVER.md
