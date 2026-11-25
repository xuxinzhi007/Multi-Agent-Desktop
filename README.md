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
- 多个 agent 会话并存，各自独立历史、状态、颜色。会话 id 是 `agent-<时间戳>`，同一秒撞车再加序号。**用户自建不设上限**；模型自建的子会话上限 6，且不占用户名额
- Queue + Worker 串行执行，队列长度 32，满了会返回错误。这一轮还在跑时，接着发来的本会话消息会在下一步模型调用前插进去；频道消息留到这一轮结束。停止会取消当前执行并清空队列
- token 级流式渲染，运行中可中断。进程内循环里，文字和推理按增量推到界面；工具调用等这一轮流结束、参数拼成完整 JSON 后再执行
- **进程内 agent loop** 是默认执行路径。空 backend，以及读进来的旧 `claude` / `codex`，都按 loop 跑。`mock` 仍是本地假回复。一轮有迭代、工具次数和墙钟上限，检查点在用户数据目录的 `loop/runs`，同一会话可以接着上次没跑完的预算
- **会话执行体可选 native / 外部 ACP**：每个会话可以选 `native`（默认，进程内 loop）或外部 ACP Agent（grok / codex-acp / dsh 等）。新建或编辑会话时可以选运行时，输入栏的运行时 chip 可随时切换；ACP 的命令、参数、环境变量与展示名可配置并落盘；未填命令时给中文可读错误，不会崩。两条路径共用 `internal/runtime` 的 `Session` 与事件协议：native 每一轮都经 `rt.Native` 的 Prompt → Events → `turn.finished` → Close，ACP 的正式文本、思考、工具开始/结束映射回现有流式 UI，审批走同一张卡片。ACP 每轮起一个子进程，停止或删除会话会取消并杀掉进程组，轮末状态回到 `idle`。会话行、舰队卡片与头部显示 `原生` / `ACP · 名称` 与连接状态（starting / running / idle / error、未配置），状态来自后端真实字段
- 模型 provider：Mock / 本地 llama.cpp / Ollama / OpenAI 兼容 / DeepSeek / Claude（走 OpenAI 兼容网关）/ Qoder / 豆包（方舟 API）/ 豆包 CLI。Qoder 不是兼容接口，走它自己的对话协议；可以浏览器授权，也可以填 `pt-` 开头的 Personal Access Token 或访问令牌。豆包 CLI 直接复用本机已登录的 `doubao` 命令，不填 Key
- **会话级配置**：每个 agent 可独立设置模型、推理强度与工作目录，未设置则回落全局。输入栏里的模型列表用已经保存的连接，不在这里重填 API Key
- **Agent 间协作**：`Route` 可以把一条消息派给另一个会话。loop 里模型还能调用 `list_agents`、`ask_agent`、`create_agent`、`delete_agent`。`create_agent` 记下 `ParentID`，侧栏把子会话嵌在父会话下面。模型或用户都能显式建这层关系；批量拆任务走下面的 Supervisor
- **派活（delegate 工具）**：模型自己判断任务能不能切开，能就调 `delegate`，把 1-3 块活派给新建的子会话，各自在独立工作树里并行做，跑完汇总回当前会话。子会话看不到父会话的对话，只拿到一段写全的任务说明。名额不够时整个派活直接失败，不会悄悄少派
- **Supervisor 自动拆任务**：侧栏「自动拆任务」输入一句目标，模型先拆一次，之后走和 `delegate` 同一套执行内核。最多 3 个子任务，受 6 个子会话名额约束（用户自建的不占）；子会话 10 分钟、父等待 20 分钟，父超时只停止等待不取消子会话。群聊里不能派子任务
- **git worktree 隔离**：会话可以在独立工作树里工作（分支 `mad/<名字>`），改动只落在自己的检出里。Supervisor 的子会话默认开启，普通会话在输入栏的文件夹弹层里手动开关。工作树放在仓库旁边的 `.mad-worktrees/<仓库名>/`，不写脏仓库；删除会话时回收，分支保留
- **群聊频道**：共享 transcript、@提及、三种回合策略（discuss / direct / roundrobin）。discuss 里没人被 @ 时，只由当前接话人回复；接话人可以改，从下一条生效
- **知识库**：手写笔记（约定、术语、偏好），标签筛选 + 全文检索。列表按「被谁引用」排序，能挂到会话或摘下来。挂上的笔记在下一轮 loop 里进提示词；新建会话时不会把全局笔记塞进首条 system。一轮结束时，模型可能再写一条值得留下的笔记
- **学习摘要**：历史超过最近 8 条之后，用一次单独的模型调用把更早的对话收成摘要，不挡住用户正在看的回复。有空闲子会话时用它的名字和模型；还没有子会话、子会话数没到 6 时，先建一个「…的学习」（飞书协助和学习助手自身不再建）。已经有子会话但都不空闲，或人数满了，就用本会话自己的模型。摘要写在下一轮上下文开头，里面的路径必须能在原文里对上
- **文件与 shell**：`read_file` / `write_file` / `edit_file` / `apply_patch` 限制在该会话的**主目录 + 附加目录**内。`grep` 输出 `路径:行号: 文本`，支持 include/exclude 与正则开关；`glob` 支持 `**` 跨层匹配；两者只读、纯 Go、跨平台，不依赖 `rg`。`apply_patch` 一次新增/更新/删除多个文件，更新必须带上下文锚点且唯一匹配，锚点陈旧或歧义时整批拒绝、一个字节都不写。`bash` 把整段 stdout/stderr 交回来，默认 60 秒，到点杀整个进程组，输出有上限。界面上另有一条手动执行命令的路径，同样是跑完再显示，没有自己的超时
- **写操作审批与审计**：`write_file` / `edit_file` / `apply_patch` / `bash` 执行前弹卡片，带标题、命令或结构化 diff；可选择拒绝、允许一次、本会话允许（按会话 + 工具记忆）。外部 ACP Agent 的 `session/request_permission` 也进同一张卡：允许回传 `allow_*` 选项，拒绝回传 `reject_*`，超时、停止或关闭一律按拒绝（cancelled）回传，不会自动放行。最多等 30 分钟；用户停止或会话 ctx 取消会立即按拒绝结束，不会等到超时。每次副作用调用追加到 `<数据目录>/audit.jsonl`，记会话、命令或文件、状态、退出码、耗时。危险命令（根目录递归删除、格式化、写块设备、关机重启、fork bomb）在执行前拦截
- **内置浏览器**：单独的 WebView，登录留在名为 workbench 的会话里，不进用户的 Chrome。模型用 `web` 工具按控件编号或页面上的文字操作，一次最多 12 步，只把最后一页交回。上次打开的地址记在 `browser-page.json`
- **飞书多机器人**：每个机器人一条长连接，会话记录共用 `feishu-rooms.json`。群可以设成只在被 @ 或被回复时回答。设置里能核对发消息、表情、历史、单聊、群消息和群列表这些权限，并打开开通页。主会话忙时，会认领它名下空闲的助手；没有空闲助手就新建，最多 3 个，沿用主会话的模型和工作目录。飞书房间视图只看记录，从下一条起可以改由另一个本机会话回答。设置页可查看 owner open_id（首次身份授权后自动写入，只读），并手动维护审批白名单（每行一个 open_id，空行自动去掉）
- **三档权限模式**：输入栏底栏切换 `逐次审批 / 自动 / 只读`。自动＝工作目录内的写与命令直接执行；只读＝写操作直接拒，适合生产环境排查。**刻意不做「完整权限」**：工作目录是工具层的硬边界，审批模式不该能放开它；危险命令的拦截不受档位影响。切档位时还挂着的卡立刻有结果，不会继续等到超时
- **飞书审批卡**：写操作要批准时，卡片直接发在飞书那条对话里（拒绝 / 允许一次 / 本会话允许），点完就地改写成结果；应用内先处理过时飞书上的按钮会收起。飞书侧另有 `/help` `/stop` `/status` `/use 名字` 四条指令
- **多根工作区**：会话可以设主目录 + 附加目录，文件工具能读写全部根，**命令仍在主目录跑**。用来「改这个仓库、同时读隔壁那个」，不必换会话或搬目录。附加目录同样不能是主目录/桌面/文稿/下载，且不从那边加载技能或知识
- **技能（skill）**：`<数据目录>/skills/*.md`，带 name / description。系统提示里只放名字和一句话描述，正文用 `read_skill` 按需取；会话走通一套流程后能用 `save_skill` 自己沉淀。和知识库的分工是「note 存这个项目的事实，skill 存跨项目复用的步骤」
- **⌘K 快速跳转**：会话 / 频道 / 飞书群在同一个列表里，输入名字直达。侧栏按「工作区 / 助手」分组，可折叠、带条数，删除按钮悬停才出现。顶部状态条常驻显示 运行中 / 待审批 / 队列，待审批可点开队列；网关页已接真实数据（进程/wired/绑定/Agent/待审批/SSE），记忆与计划两页后端未接入的部分明确禁用并写明「未接入」，不会伪造在线或延迟
- **检查器**：工具、知识、**能力**三页。工具行来自真实的 `agent.tool` 事件；能力页列出这个会话此刻的工具与技能，不可用的直接写明原因（没文件夹 / 没模型 / 浏览器没开）。没有「改动 / 任务」页
- 主窗口只在当前会话里放最近一页消息（默认 40 条，最多要 80 条）。切走的会话在界面上只留名单和最后一句，磁盘上的 `agents.json` 仍是完整历史
- 开发时如果 `.go` 源码比正在跑的进程新，诊断面板会标 `StaleBuild`。`mygo dev` 只热重载前端，Go 改动要重启进程
- 会话 / 频道 / 知识库 / 飞书记录 / 图片 / loop 检查点持久化到用户数据目录
- 一条命令交叉编译 macOS / Windows / Linux

### 没做

- ❌ **检查器的「改动 / 任务」** —— 这两个页已经不在界面上，后端也没有产出 diff 或任务清单。写操作的 diff 目前只在审批卡里展示
- ❌ **shell 流式输出，以及手动执行命令的超时** —— 结果整段返回。模型用的 `bash` 有 60 秒上限；界面上手动执行的命令没有单独超时，取消才会停
- ❌ **API Key 迁到钥匙串** —— 全局配置在 `config.json`（权限 600），会话级模型配置和飞书 Secret 也是明文落盘
- ❌ **历史改 SQLite** —— 会话历史仍是整份 `agents.json`
- ❌ **agent id 改 ULID** —— 仍是 `agent-<时间戳>`
- 🔴 **未签名** —— 分发给别人会被 Gatekeeper / SmartScreen 拦。开发版 ad-hoc 签名还会让 macOS 每次构建后重问钥匙串，`bash tools/dev-sign.sh` 可建一个固定身份，之后用 `npm run dev:signed`
- ❌ **composer 未抽成模块** —— 单聊/群聊各一个输入框，切换会话丢草稿
- ❌ **失败排查还没做进界面** —— 目前靠 `python3 tools/runs.py` 看运行记录与失败的工具调用

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

本地 llama.cpp 不在应用进程里。需要时另起 `llama-server`（默认 `http://127.0.0.1:8080`），设置里选「本地 llama.cpp」。

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
┌──────────────────────────────────────────────────────────┐
│ Go 主进程                                                │
│  main.go              mygo.Bind + 主窗口 + 事件声明       │
│  internal/agent       会话、Queue/Worker、父子、学习摘要   │
│  internal/loop        进程内循环：停止、预算、检查点       │
│  internal/runtime     统一运行时协议；native / ACP 适配器   │
│  internal/tool        工具注册；builtin 文件/bash，web 浏览器 │
│  internal/channel     频道、回合判定、领域板               │
│  internal/knowledge   知识库（JSONL）                     │
│  internal/llm         mock / llama.cpp / ollama / 兼容网关 / Qoder │
│  internal/feishu      多机器人长连接、权限、房间记录        │
│  internal/browser     内置浏览器窗口                      │
│  internal/pty         手动执行的 shell                    │
│  internal/gateway     网关端口抽象、HTTP/SSE（嵌入式，随桌面启动）  │
│  internal/memory      记忆：SQLite FTS5（未装配）          │
│  internal/scheduler   心跳调度、静默、限频（未装配）        │
├──────────────────────────────────────────────────────────┤
│ React 19 前端（Vite）                                     │
│  src/mygo.ts          生成的 typed client                 │
│  src/lib/useBackend   状态管理 + 事件订阅                  │
│  src/components/*     主窗口 UI                           │
└──────────────────────────────────────────────────────────┘
```

内置浏览器是单独的一块 WebView。浏览器页面不可信，不并进主窗口。

### 分层职责

| 层 | 位置 | 职责 |
|---|---|---|
| 绑定层 | `main.go` 的 `API` | 参数校验、错误转换、事件发射，不做业务判断 |
| 业务层 | `internal/agent`、`internal/loop`、`internal/channel`、`internal/knowledge`、`internal/feishu` | 会话、循环、回合、知识、飞书 |
| 运行时层 | `internal/runtime`、`internal/runtime/acp` | 统一事件协议；native loop 与外部 ACP Agent 共用同一个 `Session` |
| 边界层 | `internal/gateway` + `internal/gateway/host` | 嵌入式 HTTP/SSE；默认只接管 `web`，接 Manager / approval.Center / 桌面数据目录 Binder；飞书仍走 `feishu.Hub`，不重复回复 |
| 辅助包 | `internal/memory`、`internal/scheduler` | SQLite FTS5 记忆检索；心跳调度、静默时段、限频与退避（均未进程装配） |
| 基础设施 | `internal/llm`、`internal/tool`、`internal/browser`、`internal/pty` | 外部依赖 |

运行时协议单独成文：**[docs/runtime-protocol.md](docs/runtime-protocol.md)**。

**「谁该回」的判定**统一在 `channel.DecideReplies` 一个纯函数里，
绑定层只负责投递与回填，不重复实现规则。

### 并发模型

每个 agent 一个 goroutine 消费自己的队列：

```
Prompt → queue(chan, 32) → workerLoop → executeNativeRuntime(native) | executeACP | executeMock → idle/queued
```

- native 的每一轮都经 `rt.Native`：`Prompt` 提交即返回，事件走 `EventSink`，`turn.finished` 收尾，`Close` 结束；loop 只是它的执行体，不再直接向界面发事件
- 选了 ACP 的会话每轮起一个子进程，`session/cancel` + 杀进程组回收；`runtimeState`（starting / running / idle / error）只存在进程内，重启回到未启动

- 队列满时 `Prompt` 返回错误，不静默丢弃
- `StopAgent` 取消当前执行并清空队列
- 状态机：`idle → queued → running → idle | error`，另有 `blocked` 表示在等人。一轮正常结束回到 `idle`（队列里还有消息则为 `queued`），不会停在 `done`
- 正在跑的这一轮，用户新发的本会话消息由 `foldIntoTurn` 插进下一步；群消息先进 `later`，这轮结束再入队

### 数据存储

都在用户数据目录（`~/Library/Application Support/multi-agent-desktop` /
`%APPDATA%\multi-agent-desktop` / `$XDG_DATA_HOME/multi-agent-desktop`）：

| 文件 | 内容 | 写入方式 |
|---|---|---|
| `config.json` | 全局模型配置、已保存的 provider、默认目录、飞书机器人（含 API Key 与 App Secret） | 全量，tmp + rename，权限 600 |
| `agents.json` | 会话、完整历史、会话级配置（含会话级 API Key）、`ParentID`、学习摘要 | 全量，tmp + rename 原子替换 |
| `channels.json` | 频道、消息、回合策略、接话人 | 全量，tmp + rename |
| `knowledge.jsonl` | 知识笔记 | 追加写，删除时整体重写 |
| `feishu-rooms.json` | 飞书群和私聊记录、绑定的本机会话 | 飞书包自己落盘 |
| `browser-page.json` | 内置浏览器上次的地址和页面状态 | 关窗或重开时读回 |
| `images/` | 对话和飞书里的图片字节 | 按 id 存文件 |
| `loop/runs/` | 进程内循环的检查点 | 按运行 id 读写 |
| `acp/<agentId>.jsonl` | 外部 ACP Agent 的原始收发帧（会话开了 trace 才写） | 追加写，含仓库内容与命令，只留本地 |
| 窗口位置尺寸 | 主窗口 | MyGo 管理 |

---

## 模型配置

默认 provider 是 `mock`：回复由本地规则生成，**不是真实分析**，用来验证
多 agent 并行 / 流式渲染 / 会话隔离 / 中断这几件事。选了 loop 又配成 mock，
或模型名为空，这一轮会直接报「这个会话还没有模型」。

### 云端 API

设置里选 OpenAI / DeepSeek / Claude，填 Base URL + API Key。Claude 走兼容网关，不是 Anthropic 原生接口。Qoder 可以点「授权登录」，也可以填写 `pt-` 开头的 Personal Access Token 或访问令牌。

### 环境变量

环境变量会覆盖配置文件里的同名字段（未设置的不受影响）：

```bash
MAD_LLM_PROVIDER=qoder
MAD_LLM_BASE_URL=https://api3.qoder.sh
MAD_LLM_MODEL=DeepSeek-V4-Pro
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

> 完整的界面规范见 **[docs/skills/ui-design.md](docs/skills/ui-design.md)** ——
> token 命名、亮色/暗色陷阱、动效、空状态、无障碍，以及本项目踩过的具体坑。
> 改 `.tsx` / `.css` 前建议先读它。


`src/index.css` 定义设计 token，`src/styles/components.css` 提供 `c-*` 组件类。
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
├── main.go                 ← 入口：mygo.Bind、窗口、事件声明、飞书/浏览器接线
├── mygo.config.ts          ← 应用名 / 版本 / 打包配置
├── internal/
│   ├── agent/              ← 会话、Queue/Worker、父子、学习摘要、loop 接线
│   ├── loop/               ← 进程内循环、预算、检查点
│   ├── runtime/            ← 统一事件协议；native 适配器；acp/ 外部接入
│   ├── tool/               ← 工具协议；builtin/ 文件、bash、grep/glob/apply_patch，web/ 内置浏览器
│   ├── channel/            ← 频道、回合判定、transcript、领域板
│   ├── knowledge/          ← 知识库存储与注入
│   ├── llm/                ← mock / llama.cpp / ollama / 兼容网关 / Qoder
│   ├── feishu/             ← 多机器人、权限、房间记录
│   ├── gateway/            ← 端口抽象、HTTP/SSE、绑定与回执；host/ 嵌入桌面装配
│   ├── gateway/host/       ← Session/Approval/Sender/Binder 真实接线（嵌入式）
│   ├── memory/             ← SQLite FTS5：events / notes / facts
│   ├── scheduler/          ← 心跳调度、静默时段、限频、失败退避
│   ├── browser/            ← 内置浏览器窗口
│   ├── config/             ← config.json
│   ├── pty/                ← 手动执行的 shell
│   ├── proc/               ← 进程组与 shell 选择
│   ├── workdir/            ← 工作目录是否过宽
│   └── privacy/            ← macOS 隐私权限查询
├── cmd/
│   └── mad-gateway/        ← 独立常驻入口（后续形态；当前由桌面 main.go 内嵌启动）
├── src/
│   ├── mygo.ts             ← 生成的 typed client（勿手改）
│   ├── App.tsx             ← 主窗口入口
│   ├── lib/useBackend.ts   ← 状态管理 + 事件订阅
│   ├── components/         ← UI（含舰队、检查器、飞书房间、模型选择）
│   └── index.css           ← 设计 token
├── e2e/                    ← Playwright 冒烟测试
├── docs/
│   ├── design.md           ← 架构、并发模型、交互设计
│   ├── runtime-protocol.md ← 内部运行时协议与 ACP 接入
│   ├── runtime-api-contract.md ← native/ACP 的字段、API、事件与错误契约
│   └── reviews/            ← 架构评审历史存档（不是当前行为）
└── specs/                  ← 特性决策记录
```

---

## 已知限制

- **Mock 回复是占位的**，别当真实分析看
- **检查器没有「改动 / 任务」页**。写操作的 diff 只在审批卡里出现，检查器只显示工具调用和知识库
- **shell 不流式**。模型的 `bash` 默认 60 秒后杀进程组；界面上手动执行的命令没有超时，取消请求才会停
- **shell 不读登录脚本**。macOS / Linux 用 `zsh -dfc`（没有 zsh 时用 `sh -c`）；Windows 用 `powershell.exe -NoProfile -NonInteractive`，模型应写 PowerShell，不要写 bash。登录脚本会去碰桌面、文稿、下载，macOS 就按文件夹反复弹权限。工作目录不能是主目录、盘符根（如 `C:\`）、`/`，也不能是桌面、文稿、下载、图片、影片、音乐这些文件夹本身；放在其中的项目子目录可以
- **危险命令拦截是最小规则集**，不是沙箱。变量拼接、脚本内部绕过的写法覆盖不到；审批卡是最后一道人工闸门
- **网关已内嵌进桌面（仅接管 `web`）**。应用启动即起 HTTP/SSE（默认 `127.0.0.1:8780`，`MAD_GATEWAY_ADDR` / `MAD_GATEWAY_TOKEN` 可改），退出即关；Session/Approval/Sender/Binder 四项已接真实实现，端口占用只记日志不阻断桌面。飞书聊天与审批仍走现有 `feishu.Hub`，网关对 feishu 入站返回 409、对飞书绑定回复跳过，避免重复。
- **记忆 / 调度目前只有包，没有进程装配**。记忆与调度还没被对话和界面调用，记忆 / 计划两页仍显示「未接入」，要等主进程接线后才算可用；`cmd/mad-gateway` 独立常驻形态也留到网关验收之后。
- **审计只落文件**，界面还没有只读的审计页。日志在用户数据目录的 `audit.jsonl`
- **worktree 只在 git 仓库里生效**。不是仓库的文件夹开不了隔离，Supervisor 会自动退回共用目录，并在汇总里写明
- **豆包 CLI 一次只能跑一件任务**。同一个账号已经有任务在跑时（比如正在用豆包办公本身），新的 `doubao -p` 会排队或等不到结果；停止按钮会杀掉整组进程。装 CLI 用 `npm i -g @bytedance/doubao-cli`，找不到原生命令时可用 `MAD_DOUBAO_BIN` 指定路径
- **ACP 只传文本**。本轮不会把图片/附件随 prompt 发给外部 Agent；ACP 的 plan / usage 事件不展示，上下文用量仍按消息粗估
- **ACP 每轮一个进程**。换轮要重新握手，首字延迟比 native 高；空闲时不保留后台进程，也因此不存在「进程还活着但界面不知道」的悬挂状态
- **ACP 运行状态不落盘**。`runtimeState` / `runtimeError` 只在进程内，重启后显示未启动；失败原因随 `agent.error` 留在对话里
- **API Key 与飞书 Secret 明文落盘**（`config.json` 权限 600，会话级 Key 在 `agents.json`）—— 计划迁移到系统钥匙串
- **会话历史是整份 JSON**，不是数据库。界面只拿当前会话的最近一页

---

## 开发约定

- 改 Go 结构体后跑 `npx mygo generate` 重新生成 `src/mygo.ts`（需要 Bun 或 Node.js ≥ 22.6；Node 20 会直接报错）
- `src/mygo.ts` 是生成文件，**不要手改**
- 跨 goroutine 的返回值必须是副本（`channel` 包的 `clone()`），不要泄漏内部指针
- 新增事件必须同时有前端订阅，否则 `TestEveryBackendEventIsSubscribed` 会失败
- 提交前跑 `npm run verify`（含 `go test -race`）

---

## License

TBD —— 参考的 Tempest / Berd / Goose 均为 Apache 2.0。
小鲸鱼素材来自 `dsh-deepseek-pet`，授权见其 `ASSET_LICENSE.md`。
