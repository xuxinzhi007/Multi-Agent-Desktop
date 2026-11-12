# multi-agent-desktop

跨平台的**多 Agent 开发工具**桌面程序：开 N 个 AI agent，各自独立会话，同时干活。

```
Tauri 2 (Rust 壳)  +  React 19 (前端)  +  Go sidecar (后端)
```

灵感来自 [Tempest](https://github.com/tempestai-dev/tempest)（多 agent + worktree 并行）
和 [Berd](https://github.com/block/berd)（壳 / sidecar 分离）。

---

## 现在的真实状态

> 请先读这一段，避免期待错位。

**已完成（W1 + W2 + UI 骨架）**

- Tauri 壳自动拉起 / 关闭 sidecar，进程干净退出
- 前端 ↔ sidecar WebSocket 全双工通信
- 多个 agent 会话并存，各自独立历史、状态、颜色
- token 级流式渲染，运行中可中断
- shell 命令执行，右侧面板看 stdout / stderr / exit code
- 模型 provider 可切换：Mock / Ollama / OpenAI 兼容 / DeepSeek / Claude
- **三栏信息架构**：agent 舰队 / 会话流 / 检查器（改动·任务·工具）
- **diff 视图**：双列行号、红绿高亮、按文件折叠
- **工具调用卡片**：含「待批准 / 允许 / 拒绝」交互
- **Todo 进度追踪**：完成态删除线、进行中闪烁、百分比进度条
- **设计 token 体系**：约 120 个变量，组件零硬编码色值

**还没做（W3 + W4）**

- ❌ **agent 不能自己执行代码** —— 目前 shell 需要你手动输入，agent 干活靠字
- ⚠️ **检查器面板是演示数据** —— 改动/任务/工具由 mock 事件驱动，展示的是界面结构，
  不是真实 agent 行为。真数据要等 W3 把工具调用接进 agent loop
- ❌ **没有 git worktree 隔离** —— 多个 agent 改同一份代码会互相踩
- ❌ **没有 Supervisor** —— 任务要你手动分配给每个 agent，不能自动拆解
- ❌ **会话不持久化** —— sidecar 重启即清空
- ❌ 小鲸鱼桌宠素材在 `assets/pet/` 躺着，还没接进 UI
- 🟡 **打包不完整** —— macOS `.app` 已可产出并验证启动，DMG 因 Finder 自动化权限问题被跳过；
  Windows / Linux 包未验证

**一句话**：现在是「多个 AI 聊天窗口并排」，还不是「多个 AI 在你代码上并行干活」。
前者已经能用，后者是 W3 的核心目标。

---

## 快速开始

### 0. 一次性准备（只需做一次）

```bash
# 装依赖
npm install

# 编译 sidecar 并放到 Tauri 要求的目录
./tools/build-sidecar.sh
cp sidecar/bin/sidecar-darwin-arm64 src-tauri/binaries/sidecar-aarch64-apple-darwin
```

> **为什么需要第 2 步？** Tauri 的 `externalBin` 要求文件名带 target triple 后缀
> （`sidecar-aarch64-apple-darwin`），交叉编译脚本产出的是 `sidecar-darwin-arm64`。
> `src-tauri/binaries/` 被 gitignore（6MB 二进制不入库），**换机器或 `git clean` 后必须重做**，
> 否则启动报 `sidecar not found`。

Intel Mac 换成 `sidecar-x86_64-apple-darwin`。

### 1. 开发模式（改代码热更新）

```bash
npm run tauri dev
```

这一条命令会做四件事：

1. 起 Vite 开发服务器（`localhost:5173`）
2. 编译 Rust 壳（首次约 2 分钟，之后增量 10 秒内）
3. 拉起 Go sidecar（`127.0.0.1:17389`）
4. 打开桌面窗口

窗口里能做什么：

| 操作 | 位置 |
|---|---|
| 新建 agent（可建多个并行） | 左下「+ 新建 Agent」 |
| 发消息，看流式输出 | 中间输入框，Enter 发送 |
| 执行 shell 命令 | 输入框下方切「终端」 |
| 看改动 diff / 任务进度 / 工具调用 | 右侧检查器三个 tab |
| 切模型 provider、改工作目录 | 左上角 ⚙ |

### 2. 单独调试（可选）

只调后端，不启壳：

```bash
cd sidecar && go run . --port 17389 --workdir ..
curl http://127.0.0.1:17389/healthz
```

只调前端，连已经在跑的 sidecar：

```bash
npm run dev    # 浏览器打开 http://localhost:5173
```

## 打包

### macOS（已验证）

```bash
./tools/package-macos.sh
```

输出：`src-tauri/target/release/bundle/macos/multi-agent-desktop.app`（约 18MB）

安装：

```bash
open src-tauri/target/release/bundle/macos/multi-agent-desktop.app   # 直接运行
# 或拖到 /Applications
```

**为什么单独写了个脚本？**
`npm run tauri build` 默认同时产出 `.app` 和 `.dmg`，但 DMG 那一步用
`bundle_dmg.sh` 通过 AppleScript 驱动 Finder 排布窗口，在无 GUI 会话或没给
「自动化」权限时会卡死并报：

```
execution error: "Finder"遇到一个错误：AppleEvent已超时。 (-1712)
```

脚本用 `--bundles app` 跳过 DMG，只出 `.app`，稳定可重复。
确实需要 DMG 时，在**已登录的图形界面**下跑完整命令：

```bash
npm run tauri build
```

### 其他平台

| 目标 | 命令 | 说明 |
|---|---|---|
| Windows | `npm run tauri build` | 需在 Windows 上执行，产物 `.msi` / `.exe` |
| Linux | `npm run tauri build` | 需在 Linux 上执行，产物 `.deb` / `.AppImage` |

**Tauri 不支持跨平台交叉打包** —— 出 Windows 包必须在 Windows 机器（或 CI）上构建。
sidecar 可以交叉编译，壳不行。

### 发布前要处理

当前是**未签名**构建，别人下载会被 Gatekeeper 拦。正式分发需要：

- Apple Developer 账号（$99/年）+ notarization
- Windows 代码签名证书
- 或者告诉用户右键 → 打开 绕过

## 模型配置

默认 provider 是 `mock`：回复由本地规则生成，**不是真实分析**，
只用来验证「多 agent 并行 / 流式渲染 / 会话隔离 / 停止」这几件事。

### 接 Ollama（本地免费）

```bash
brew install ollama
ollama serve
ollama pull qwen2.5-coder:7b
```

设置里选 **Ollama**，Base URL 和模型名已预填。

### 接云端 API

设置里选 OpenAI / DeepSeek / Claude，填 Base URL + API Key。
Key 只存在 sidecar 进程内存，**不落盘**。

### 用环境变量预设（省得每次点设置）

```bash
MAD_LLM_PROVIDER=ollama \
MAD_LLM_BASE_URL=http://127.0.0.1:11434 \
MAD_LLM_MODEL=qwen2.5-coder:7b \
cd sidecar && go run .
```

---

## 设计系统

`src/index.css` 定义约 120 个 token，`src/styles/components.css` 提供 `c-*` 组件类。
组件内**禁止硬编码色值**，一律走变量。

命名规律：

```
--bg-{base|chrome|surface|elevated|inset}     背景层级（越靠上层越亮）
--border-{subtle|default|strong|focus}        边框
--text-{primary|secondary|tertiary|disabled}  文字层级
--{success|warning|danger|info}-{,dim,border} 语义色三件套
--diff-{add,del}-{bg,text}                    diff 专用
--agent-1..6                                  agent 身份色
--space-1..8 (4px 基准)  --text-xs..xl  --radius-sm..full
```

改主题只需改 `:root` 一处。加新组件用 `c-` 前缀，避免和内联样式打架。

## 环境要求

| 工具 | 版本 | 备注 |
|---|---|---|
| Go | 1.22+ | 本机 1.25.8，在 `~/sdk/go1.25.8/bin` |
| Node | 22+ | 本机 26.13，配 npm 11 |
| Rust | 1.90+ | `rustup default stable-aarch64-apple-darwin` |
| Xcode CLT | — | macOS 必需，验证 `xcrun --show-sdk-path` |

> **非交互 shell 注意**：脚本工具读不到 `~/.zshrc`，先手动加 PATH：
> ```bash
> export PATH="$HOME/sdk/go1.25.8/bin:$HOME/go/bin:$HOME/.cargo/bin:$PATH"
> ```

---

## 架构

### 进程模型

**单 Go 进程**（MyGo），无跨进程通信：

```
┌────────────────────────────────────────────────┐
│ MyGo 主进程（Go）                              │
│  ├── main.go           壳入口，mygo.Bind()     │
│  ├── internal/agent/   N 个会话，goroutine 并行 │
│  ├── internal/llm/     mock / ollama / openai  │
│  ├── internal/pty/     跨平台 shell 执行        │
│  └── internal/events/  事件总线（W3 规划）      │
├────────────────────────────────────────────────┤
│ React 19 前端（Vite 构建）                      │
│  ├── src/mygo.ts       typed IPC client（生成） │
│  ├── src/lib/useSidecar.ts  状态管理 + 事件订阅 │
│  ├── src/lib/useTheme.ts    主题切换            │
│  └── src/components/*.tsx  UI 组件             │
└────────────────────────────────────────────────┘
         ↓ mygo.Bind() + mygo.NewEvent()
    编译期类型安全，无 WebSocket
```

### 通信协议

**MyGo typed IPC**（不是 WebSocket）：

```go
// Go 端暴露 API
mygo.Bind(Sidecar{})
var AgentCreated = mygo.NewEvent[Agent]("agent.created")
AgentCreated.Emit(win, agent)
```

```ts
// 前端调用（自动生成 src/mygo.ts）
await Sidecar.createAgent({ name: "前端", role: "..." })
events.agentCreated.on((agent) => { ... })
```

**为什么不用 WebSocket**：
- 类型不安全（JSON 字符串，运行时才发现错）
- 要维护连接状态、重连逻辑
- MyGo IPC 编译期类型安全，零样板代码

### 多 Agent 并发

**Goroutine per Prompt**，当前无队列（会乱序，见设计文档 5.2）：

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

**目标**：Queue + Worker 模型（见 `docs/design.md` 5.2）

### 目录

```
multi-agent-desktop/
├── main.go                  ← MyGo 壳入口
├── mygo.config.ts           ← 打包配置
├── go.mod / go.sum          ← Go 依赖
├── internal/
│   ├── agent/               ← 会话管理（Manager + Agent）
│   ├── llm/                 ← mock / openai / 接口
│   ├── pty/                 ← shell 执行
│   ├── events/              ← 事件总线（W3 占位）
│   ├── supervisor/          ← 任务分发（W3 占位）
│   └── worktree/            ← git worktree（W3 占位）
├── src/
│   ├── mygo.ts              ← typed IPC client（自动生成）
│   ├── App.tsx              ← 根组件
│   ├── lib/
│   │   ├── useSidecar.ts    ← 状态管理
│   │   ├── useTheme.ts      ← 主题切换
│   │   └── presets.ts       ← 角色/provider 预设
│   ├── components/          ← UI 组件
│   └── index.css            ← 设计 token（暗色 + 亮色）
├── docs/
│   ├── design.md            ← 架构、并发模型、交互设计
│   ├── research.md          ← 技术选型调研
│   └── architecture.md      ← 跨平台坑清单
├── assets/pet/              ← 小鲸鱼素材（未接入）
└── tools/                   ← 构建脚本
```

---

## API 与事件

### 前端调用（typed IPC）

```ts
// 会话管理
await Sidecar.createAgent({ name: "前端", role: "React 专家" })
await Sidecar.deleteAgent(agentId)
await Sidecar.stopAgent(agentId)
await Sidecar.promptAgent({ agentId, text: "..." })
await Sidecar.listAgents()

// Shell
await Sidecar.runShell({ agentId, command: "ls -la" })

// 配置
await Sidecar.getLLMConfig() / setLLMConfig(cfg)
await Sidecar.getWorkDir() / setWorkDir(dir)

// 窗口
await Sidecar.setAlwaysOnTop(true)

// 系统信息
await Sidecar.info()
```

### Go → 前端事件

| 事件 | 载荷类型 | 含义 |
|---|---|---|
| `agent.created` | `Agent` | 新会话 |
| `agent.deleted` | `string` | 删除的 agent ID |
| `agent.status` | `StatusEvent` | `idle` / `running` / `done` / `error` |
| `agent.delta` | `DeltaEvent` | 流式片段（token 级） |
| `agent.message` | `MessageEvent` | 完整消息落定 |
| `agent.error` | `ErrorEvent` | 错误 |
| `shell.start` / `shell.result` | `ShellEvent` / `ShellResultEvent` | 命令执行 |
| `llm.config` | `Config` | 模型配置变更 |
| `workdir` | `WorkDirEvent` | 工作目录变更 |

### 环境变量

```bash
MAD_LLM_PROVIDER=ollama
MAD_LLM_BASE_URL=http://127.0.0.1:11434
MAD_LLM_MODEL=qwen2.5-coder:7b
MAD_LLM_API_KEY=sk-...
```

---

## 路线图

| 里程碑 | 目标 | 状态 |
|---|---|---|
| W1 | ~~Tauri 壳 + sidecar WS 通信~~ → **已迁移 MyGo** | ✅ 2026-11-11 |
| W2 | 多 agent 并行 + 流式对话 + shell | ✅ 2026-11-11 |
| W2.5 | **MyGo 迁移**：单 Go 进程 + typed IPC + 跨平台打包 | ✅ 2026-11-12 |
| W3 | 连续对话队列 + agent 互相通信 + CLI 编排 | ⬜ 下一步 |
| W4 | git worktree 隔离 + 小鲸鱼桌宠 + 自动更新 | ⬜ |

**W3 核心**（见 `docs/design.md` 第 5 节）：
1. **Queue + Worker**：连续对话不乱序，可排队/跳过/清空
2. **Manager.Route**：agent 可 @其他 agent，主动发消息、触发工作
3. **CLI 编排**：接 `claude -p` / `codex exec`，让 agent 真能改代码

---

## 已知问题

- **Mock 回复是占位的**，别当真实分析看
- **连续对话会乱序** —— 无队列机制，连发两条消息两个 goroutine 同时跑
- **agent 不能互相通信** —— 完全孤立，没有 @提及、任务分发
- **agent 不会自己用终端** —— `shell.run` 是前端发起的，跟 agent 推理链没打通
- **长命令会卡住** —— shell 是同步等结果，没有流式输出
- **检查器面板是演示数据** —— `demoTrail()` 造的，不是真实 agent 行为
- **DMG 打包会失败** —— `bundle_dmg.sh` 依赖 Finder AppleScript，无 GUI 会话时报 -1712，用 `tools/package-macos.sh` 绕过
- **未签名** —— 分发给别人会被 Gatekeeper / SmartScreen 拦

---

## 文档

- [HANDOVER.md](HANDOVER.md) —— 交接文档，换机器 / 换会话照它继续
- [docs/research.md](docs/research.md) —— 开源项目调研、技术选型对比
- [docs/architecture.md](docs/architecture.md) —— 详细架构、跨平台坑点清单

## License

待定。参考的 Tempest / Berd / Goose 均为 Apache 2.0。
小鲸鱼素材来自 `dsh-deepseek-pet`，授权见其 `ASSET_LICENSE.md`。
