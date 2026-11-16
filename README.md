# multi-agent-desktop

跨平台的**多 Agent 开发工具**桌面程序：开 N 个 AI agent，各自独立会话，同时干活。

**单 Go 进程 + React 19 前端**，由 [MyGo](https://github.com/egoist/mygo) 提供原生窗口、typed IPC 与打包。

灵感来自 [Tempest](https://github.com/tempestai-dev/tempest)（多 agent + worktree 并行）与
[Berd](https://github.com/block/berd)（壳与后端分层）。

---

## 当前状态

> 这一节描述的是**代码里真实存在的行为**，不是路线图。没做的会明确标 ❌。

### 能用

- **舰队视图**：所有 agent 并排一列一个，同屏看到状态、当前任务、实时输出、耗时与产出量
- 多个 agent 会话并存，各自独立历史、状态、颜色
- Queue + Worker 串行执行，连发多条不会乱序
- token 级流式渲染，运行中可中断
- 模型 provider 可切换：Mock / Ollama / OpenAI 兼容 / DeepSeek / Claude
- **会话级配置**：每个 agent 可独立设置模型与工作目录，未设置则回落全局
- **Agent 间协作**：`Route` 显式派发消息给对方 agent
- **群聊频道**：共享 transcript、@提及路由、三种回合策略（discuss / direct / roundrobin）
- **知识库**：手写笔记（约定、术语、偏好），标签筛选 + 全文检索，显示引用情况并支持挂/摘
- **CLI backend**：接 `claude -p` / `codex exec`，让 agent 真正读写代码
- shell 命令执行：结果内联在对话流里，不另开面板（同步等待，见「已知限制」）
- 检查器面板：工具调用、活动流、知识库
- **知识库**按「被谁引用」排序与展示，能直接看到每条笔记正在被哪些 agent 使用
- 迷你悬浮窗（灵动岛）：常驻桌面，显示各 agent 状态与「在等你」提示
- 会话 / 频道 / 知识库持久化到用户数据目录
- 一条命令交叉编译 macOS / Windows / Linux

### 没做

- ❌ **git worktree 隔离** —— 多个 agent 改同一份代码会互相踩
- ❌ **Supervisor 自动拆解任务** —— 任务要你手动分配给每个 agent
- ❌ **工具审批流** —— 工具调用直接执行，没有「待批准 / 允许 / 拒绝」闸门
- ❌ **diff 视图的真实数据** —— 检查器的「改动 / 任务」两个 tab 目前始终为空，
  要等工具调用产出结构化结果后才有内容
- 🟡 **API Key 明文落盘** —— 存在 `agents.json`，计划迁移到 `mygo.Secrets`
- 🟡 **shell 同步阻塞** —— 长命令会卡住，没有流式输出
- 🟡 **未签名** —— 分发给别人会被 Gatekeeper / SmartScreen 拦

---

## 快速开始

```bash
npm install
npm run dev          # mygo dev：热重载开发
```

打包：

```bash
npm run build:app    # 跳过安装器，只出 .app（macOS）
npm run build        # 完整产物：macOS dmg / Windows exe / Linux deb+rpm+AppImage
```

MyGo 不需要 cgo，**任意平台都能从任意机器编译**；只有签名与磁盘镜像需要 macOS。

验证：

```bash
npm run verify       # tsc + go vet + go test -race + playwright
```

---

## 架构

### 进程模型

**单 Go 进程**。前端不通过 WebSocket 通信，而是 MyGo 的 typed IPC：
Go 侧 `mygo.Bind(api)` 暴露方法，`mygo.NewEvent[T](name)` 声明事件，
`mygo generate` 生成 `src/mygo.ts`——**编译期类型安全，零手写桥接**。

```
┌──────────────────────────────────────────────────┐
│ Go 主进程                                        │
│  main.go          mygo.Bind + 窗口 + 事件声明     │
│  internal/agent   会话管理、Queue/Worker、持久化  │
│  internal/channel 频道、回合判定、transcript      │
│  internal/knowledge 知识库（JSONL 存储）          │
│  internal/llm     mock / openai 兼容流式客户端    │
│  internal/cli     claude / codex 子进程编排       │
│  internal/pty     跨平台 shell 执行               │
├──────────────────────────────────────────────────┤
│ React 19 前端（Vite）                             │
│  src/mygo.ts         生成的 typed client          │
│  src/lib/useBackend  状态管理 + 事件订阅           │
│  src/components/*    UI                           │
└──────────────────────────────────────────────────┘
```

### 分层职责

| 层 | 位置 | 职责 |
|---|---|---|
| 绑定层 | `main.go` 的 `API` | 参数校验、错误转换、事件发射，不做业务判断 |
| 业务层 | `internal/agent`、`internal/channel`、`internal/knowledge` | 会话、回合、知识 |
| 基础设施 | `internal/llm`、`internal/cli`、`internal/pty` | 外部依赖 |

**「谁该回」的判定**统一在 `channel.DecideReplies` 一个纯函数里，
绑定层只负责投递与回填，不重复实现规则。

### 并发模型

每个 agent 一个 goroutine 消费自己的队列：

```
Prompt → queue(chan, 32) → workerLoop → execute → idle/queued
```

- 队列满时 `Prompt` 返回错误，不静默丢弃
- `StopAgent` 取消当前执行并清空队列
- 状态机：`idle → queued → running → idle | error`，另有 `blocked` 表示在等人

### 数据存储

都在用户数据目录（`~/Library/Application Support/multi-agent-desktop` /
`%APPDATA%\multi-agent-desktop` / `$XDG_DATA_HOME/multi-agent-desktop`）：

| 文件 | 内容 | 写入方式 |
|---|---|---|
| `config.json` | 全局模型配置、默认目录（含 API Key） | 全量，tmp + rename，权限 600 |
| `agents.json` | 会话、历史、会话级配置 | 全量，tmp + rename 原子替换 |
| `channels.json` | 频道、消息、回合策略 | 全量，tmp + rename |
| `knowledge.jsonl` | 知识笔记 | 追加写，删除时整体重写 |
| `window-state.json` | 窗口位置尺寸 | MyGo 管理 |

---

## 模型配置

默认 provider 是 `mock`：回复由本地规则生成，**不是真实分析**，用来验证
多 agent 并行 / 流式渲染 / 会话隔离 / 中断这几件事。

### 本地 Ollama

```bash
ollama serve
ollama pull qwen2.5-coder:7b
```

设置里选 **Ollama**，Base URL 与模型名已预填。

### 云端 API

设置里选 OpenAI / DeepSeek / Claude，填 Base URL + API Key。

### 环境变量

环境变量会覆盖配置文件里的同名字段（未设置的不受影响）：

```bash
MAD_LLM_PROVIDER=ollama
MAD_LLM_BASE_URL=http://127.0.0.1:11434
MAD_LLM_MODEL=qwen2.5-coder:7b
MAD_LLM_API_KEY=sk-...
MAD_WORK_DIR=/path/to/project
```

### 配置存在哪

```
<用户数据目录>/config.json     ← 全局模型配置与默认目录，权限 600
```

| 平台 | 路径 |
|---|---|
| macOS | `~/Library/Application Support/multi-agent-desktop/config.json` |
| Windows | `%APPDATA%\multi-agent-desktop\config.json` |
| Linux | `$XDG_DATA_HOME/multi-agent-desktop/config.json` |

设置面板底部会显示实际路径。文件是人可读的 JSON，可手动编辑；
损坏时会被备份为 `.corrupt` 并以默认值启动，不会导致应用起不来。

---

## 设计系统

`src/index.css` 定义约 120 个 token，`src/styles/components.css` 提供 `c-*` 组件类。
组件内**禁止硬编码色值**，一律走变量。

```
--bg-{base|chrome|surface|elevated|inset}     背景层级
--border-{subtle|default|strong|focus}        边框
--text-{primary|secondary|tertiary|disabled}  文字层级
--{success|warning|danger|info}-{,dim,border} 语义色三件套
--diff-{add,del}-{bg,text}                    diff 专用
--agent-1..6                                   agent 身份色
--space-1..8 (4px 基准)  --text-xs..xl  --radius-sm..full
```

改主题只需改 `:root` 一处。

---

## 目录

```
multi-agent-desktop/
├── main.go                 ← 入口：mygo.Bind、窗口、事件声明
├── mygo.config.ts          ← 应用名 / 版本 / 打包配置
├── internal/
│   ├── agent/              ← 会话管理、Queue/Worker、持久化
│   ├── channel/            ← 频道、回合判定、transcript
│   ├── knowledge/          ← 知识库存储与注入
│   ├── llm/                ← LLM 客户端（mock / openai 兼容）
│   ├── cli/                ← claude / codex 子进程
│   └── pty/                ← 跨平台 shell
├── src/
│   ├── mygo.ts             ← 生成的 typed client（勿手改）
│   ├── App.tsx / mini.tsx  ← 主窗 / 悬浮窗入口
│   ├── lib/useBackend.ts   ← 状态管理 + 事件订阅
│   ├── components/         ← UI 组件
│   └── index.css           ← 设计 token
├── e2e/                    ← Playwright 冒烟测试
├── docs/
│   ├── design.md           ← 架构、并发模型、交互设计
│   └── reviews/            ← 架构评审历史存档
└── specs/                  ← 特性决策记录
```

---

## 已知限制

- **Mock 回复是占位的**，别当真实分析看
- **检查器的「改动 / 任务」始终为空** —— 后端还没有产出这两类结构化数据的路径
- **shell 是同步等结果**，长命令会卡住，没有超时控制
- **工具调用没有审批闸门**，CLI agent 直接执行
- **无 worktree 隔离**，多 agent 并行改同一仓库会冲突
- **API Key 明文落盘**（`config.json`，权限 600）—— 计划迁移到系统钥匙串

---

## 开发约定

- 改 Go 结构体后跑 `npx mygo generate` 重新生成 `src/mygo.ts`
- `src/mygo.ts` 是生成文件，**不要手改**
- 跨 goroutine 的返回值必须是副本（`channel` 包的 `clone()`），不要泄漏内部指针
- 新增事件必须同时有前端订阅，否则 `TestEveryBackendEventIsSubscribed` 会失败
- 提交前跑 `npm run verify`（含 `go test -race`）

---

## License

TBD —— 参考的 Tempest / Berd / Goose 均为 Apache 2.0。
小鲸鱼素材来自 `dsh-deepseek-pet`，授权见其 `ASSET_LICENSE.md`。
