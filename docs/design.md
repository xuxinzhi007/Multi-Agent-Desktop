# 设计文档：multi-agent-desktop

> 版本：0.4.2
> 最后更新：2026-11-23

这一份写的是**代码里已经落地的行为**。`docs/reviews/` 里的评审和方案是历史提案，不代表现在能用。

---

## 1. 产品定位

跨平台（macOS / Windows / Linux）的多 Agent 开发工具桌面程序。
N 个 AI agent 并行工作，各自独立会话。用户可以看舰队和对话、中途插话、停止、改这个会话的模型和工作目录。

**当前形态**：多 agent 会话 + 进程内工具循环（读写文件、shell、内置浏览器、会话之间派活）+ 写操作审批与命令审计 + 频道 + 知识库 + 飞书多机器人 + git worktree 隔离 + Supervisor 自动拆任务。

**还没做**：检查器里的改动/任务数据、shell 流式输出和手动命令的超时、API Key 进钥匙串、历史改 SQLite、agent id 改 ULID。

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
├── agent.Manager      —— N 个会话，每个一个 worker
├── loop.Runner        —— 进程内循环（停止、预算、检查点）
├── tool.Registry      —— 文件、bash、web、会话与笔记工具
├── llm.Client         —— mock / llama.cpp / ollama / openai 兼容 / qoder
├── channel.Manager    —— 频道与回合
├── feishu.Hub         —— 每个机器人一条长连接
├── browser.Desk       —— 内置浏览器，单独窗口
├── pty.Runner         —— 界面上手动执行的 shell
└── mygo.Bind()        —— 暴露 API 给前端

React 19 前端（Vite 构建）
├── mygo-runtime       —— typed IPC client（自动生成）
└── events.*.on()      —— 订阅 Go 端事件
```

主窗口和内置浏览器各是一块 WebView。浏览器页面不可信，不并进主 WebView。本地模型继续在进程外（`llama-server` 或 Ollama），不拉进应用进程。

**优势**：
- 无跨进程通信（之前 Tauri 要 WebSocket + 端口）
- 无序列化开销（typed IPC 直接调 Go 方法）
- 协程天然适合多 agent 并行

### 2.3 通信协议：MyGo typed IPC

**不用 WebSocket / REST**，用 MyGo 的 `Bind` + `Event`：

```go
// Go 端
mygo.Bind(API{})  // 暴露 API 的方法给前端

var AgentCreated = mygo.NewEvent[Agent]("agent.created")
AgentCreated.Emit(win, agent)  // 推给前端
```

```ts
// 前端（自动生成 src/mygo.ts）
await API.createAgent({ name: "前端", role: "..." })
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

界面细则、亮色陷阱和已有护栏见 `docs/skills/ui-design.md`。

---

## 4. 数据流

### 4.1 单一状态源

`useBackend.ts` 是主窗口的状态管理：
- 订阅 MyGo events → 更新 React state
- 暴露 `send(action, data)` → 调 Mygo 方法

```
Go Event → useBackend reducer → React state → 组件渲染
                ↑
         用户操作 → send() → Go 方法
```

非当前会话在 React state 里只留 id、名字、状态、最后一句。点进某个会话，再向 Go 要最近一页（默认 40 条，上限 80）。`agents.json` 仍是完整记录。每个 delta 不写盘；检查点在 loop 里，跟发起模型请求的节奏走，不按 token 落盘。

### 4.2 事件命名

| 事件 | 方向 | 含义 |
|---|---|---|
| `agent.created` / `agent.deleted` | Go → 前端 | 新建 / 删除会话 |
| `agent.profile` | Go → 前端 | 名字、简介、学习摘要进度 |
| `agent.status` | Go → 前端 | `idle` / `queued` / `running` / `error` / `blocked` |
| `agent.delta` | Go → 主窗口 | 流式正文 |
| `agent.reasoning` | Go → 主窗口 | 流式推理 |
| `agent.message` | Go → 主窗口 | 完整消息落定 |
| `agent.error` | Go → 前端 | 错误 |
| `agent.tool` | Go → 主窗口 | 工具开始 / 结束 |
| `agent.loop` | Go → 主窗口 | 循环阶段、预算、上下文占用 |
| `agent.stopped` | Go → 主窗口 | 停止原因 |
| `agent.config` | Go → 前端 | 会话级模型或目录变更，带完整 `AgentView` |
| `tool.start` | Go → 主窗口 | 旧的工具开始事件 |
| `shell.start` / `shell.result` | Go → 主窗口 | 手动执行的命令，整段结果 |
| `llm.config` / `workdir` | Go → 前端 | 全局配置 |
| `channel.created` / `channel.deleted` / `channel.message` | Go → 前端 | 频道 |
| `knowledge.changed` / `knowledge.deleted` | Go → 前端 | 笔记 |

`agent.Manager` 持 `Emitter`，不直接依赖 MyGo。`emitToWindow` 把字符串事件转成强类型 payload。channel / knowledge 也可以在 API 层直接 `Event.Emit`。

---

## 5. 多 Agent 并发模型

### 5.1 实现：Queue + Worker（已落地）

每个 agent 一个带缓冲的队列（32）和一个 worker goroutine。队列满时 `Prompt` 返回错误。

**状态**：
```
idle --入队--> queued --worker 取到--> running --结束--> idle
                                              |
                                              +--> error
running / queued --BlockAgent--> blocked
```

一轮正常结束调用 `setIdleOrQueued`：队列空则 `idle`，还有消息则 `queued`。`done` 留在类型里，当前收尾路径不把它当成停留状态。

**用户操作**：
- **发送**：空闲则马上跑；正在跑时，本会话的新消息在下一步模型调用前插进当前轮（`foldIntoTurn`）。频道消息留到这轮结束再入队
- **停止**：取消当前执行，并清空还没跑的队列。飞书侧若这条会话正在答，一并停掉
- 界面把排队中的句子标出来。没有「跳过当前、执行下一条」，也没有把排队项提前或删掉的操作

### 5.2 进程内循环（已落地）

默认 backend 是 `loop`。`executeLoop` 在工作目录里装配工具，交给 `loop.Runner`：

| 工具 | 作用 |
|---|---|
| `read_file` / `write_file` / `edit_file` | 只碰工作目录里面的文件 |
| `bash` | 不读登录脚本。默认 60 秒，超时杀进程组，输出有上限，整段返回 |
| `web` | 操作内置浏览器。先看控件，再用编号或页面上的文字。`steps` 最多 12 步，中间步骤不把页面交回 |
| `list_agents` / `ask_agent` / `create_agent` / `delete_agent` | 看会话、等对方答完、建子会话、删自己或自己建的子会话 |
| `list_notes` / `save_note` | 读知识库、写一条并挂到当前会话 |
| `set_profile` | 改自己的名字、简介或工作目录 |

`mock` backend 仍走 `executeMock`，不调用这些工具。loop 配上 mock 或空模型，会直接报「这个会话还没有模型」。没有工作目录，或目录是主目录、盘符根、桌面、文稿、下载、图片、影片、音乐本身，这一轮报错。

预算默认 24 轮、64 次工具、10 分钟墙钟。上下文超过窗口时先压缩，保留系统提示和最近大约 8000 token。同一会话的检查点在 `<dataDir>/loop/runs`，本会话消息能接上时可以续跑；频道里的这一轮不续跑。

写操作在 `Registry.Invoke` 里分两步：先调工具的 `Preview`（标题、命令、diff），策略拦截的直接以 `denied` 结束；再由审批中心挂起，等界面上的卡片决定。允许一次只放行这一次，本会话允许按「会话 + 工具」记忆，删会话即失效。等待上限 5 分钟，ctx 取消（按停止）也会收起卡片。

每次有副作用的调用（`bash` / `write_file` / `edit_file`）追加一行到 `<dataDir>/audit.jsonl`，字段包括会话、命令或文件、状态、退出码、耗时。前端只读地拿最近记录，不提供修改入口。危险命令（根目录/系统目录递归删除、格式化、写块设备、关机重启、fork bomb）由 `shellguard` 在执行前拦下，bash 的 `Preview` 与 `Execute` 各检查一次，防绕过注册表直调。

### 5.3 Agent 间协作与父子（已落地）

两条路并存：

- `Manager.Route(from, to, text)`：显式把一条消息放进对方队列。界面上是路由气泡
- 模型工具：`ask_agent` 按名字把任务交给已有会话并等待回复；`create_agent` 新建助手，带 `task` 时会等它做完

`ParentID` 记在 `Agent` 上，并进 `Save` / `Load` / `snapshot` / `viewMap`。侧栏按它嵌套。三种子会话都会带上父 id：

- 模型调用 `create_agent`
- 飞书主会话忙时认领或新建的「…的协助」，同一父会话最多 3 个
- 学习摘要在还没有子会话、人数也没到 8 时建的「…的学习」。摘要本身是一次单独的模型调用，不进那个子会话的队列。有空闲子会话就用它的名字和模型；已经有子会话但都不空闲，或人数满了，就用本会话的模型

会话总数上限 8。`delete_agent` 只能删自己，或自己创建的子会话。互相 `ask_agent` 等待会被拦住。

Supervisor（`backend=supervisor`）：侧栏「自动拆任务」填一句目标，`executeSupervisor` 让模型拆成 2-4 个子任务，每个子任务建一个独立子会话（默认各自 worktree），并行跑完后把汇总写回父会话。最多 4 个子任务、总时限 20 分钟，仍受 8 会话上限约束；停止父会话会级联停止子会话。模型没给出可用的 JSON 方案时退化成一个子任务，不报错。

### 5.4 群聊（已落地）

频道在 `internal/channel`。持久化 `<dataDir>/channels.json`。`DeleteAgent` 会 `RemoveMember`。

回合策略：

| 策略 | 谁回 |
|---|---|
| `discuss` | 被 @ 的回。没人被 @ 时，只由当前 speaker 接；speaker 空着或不是成员，就没人接 |
| `direct` | 成员都回 |
| `roundrobin` | 成员都回 |

`DecideReplies` 是唯一判定。绑定层只投递。领域结论（知识蒸馏命中某一块专业）会发到同名频道，下一轮的系统提示里带上别人在这块记下的几条。

飞书房间不是这里的频道。见 5.7。

### 5.5 事件广播

`agent.Manager` 持 `Emitter` 函数，把事件转成 MyGo `Event.Emit`：
- 解耦：agent 包不依赖 MyGo
- 可测试：单测时换成 channel 收集事件
- `emitToWindow` 只做「字符串事件 → 强类型 payload → Emit」的转发

### 5.6 会话级配置（单一事实源）

| 字段 | 作用域 | 回落 |
|---|---|---|
| `Agent.WorkDir` | 单会话的工具与 shell 目录 | 空则用 `Manager.workDir` |
| `Agent.LLMCfg` | 单会话的提供方、模型、推理强度 | `nil` 则用 `Manager.llmCfg` |

前端在输入栏用 `ModelPicker` 选已经保存的连接，走 `SetAgentLLM`。能思考的模型，强度浮在那一行旁边。设置弹窗管外观、全局默认，以及各 provider 的凭据。凭据不在会话弹层里重填。

> **持久化白名单**：`Agent` 新增字段必须同步 `Save` / `Load` / `snapshot` / `viewMap`，
> 否则会静默丢字段。`internal/agent/manager_test.go` 的 round-trip 测试守住这个不变量。

### 5.7 飞书（已落地）

`internal/feishu.Hub` 让每个机器人各走一条长连接，房间记录共用一份。配置在 `config.json` 的 `feishuBots`（旧的单机器人字段读入后迁过来）。Secret 不回传给设置页。

- 启用的机器人必须有 App ID、App Secret 和一个本机会话，否则状态是 error
- 群可以 `MentionOnly`：只有被 @ 或被回复才回答。私聊照常接
- 文本、富文本、图片、合并转发会进会话。其它类型回一句「目前只接收文本、富文本和图片」
- 同一条 `message_id` 重推会被丢掉
- 权限检查对照：发消息/读历史/标表情、接收单聊、接收群消息、查看所在群。缺了可以打开飞书开通页
- 用户授权后的令牌留在本机配置里
- 主会话忙时 `Assist` 挑一个空闲助手；没有就新建，沿用主会话的模型和工作目录。助手满了返回 busy，等这一轮结束再派
- 模型已经说出的句子和工具行写回飞书卡片
- `FeishuRoomView` 只展示 `feishu-rooms.json` 里的记录。`SetFeishuRoomAgent` 从下一条起改由另一个本机会话回答

### 5.8 内置浏览器（已落地）

`browser.Desk` 用名为 workbench 的 `mygo.Session`，单独窗口。`OpenWorkbench` 和 `web` 工具都走它。地址只接受 `http` / `https` / `about`。登录 cookie 留在这个窗口，不使用用户的 Chrome。页面状态记在 `browser-page.json`。窗口关掉后不再留着，下次打开再创建。

### 5.9 学习摘要（已落地）

一轮结束后 `studyHistory` 在后台跑，不挡住已经显示的回复。最近 8 条留在窗口里，更早的按最多 3 步、每步 20 条或大约 3000 token，用一次单独的模型调用收成摘要。有空闲子会话时记它的名字；否则在没有子会话且人数未满时先建「…的学习」。已经有子会话但都不空闲，或人数满了，就用本会话的模型。摘要里出现、原文里没有的路径会被整句丢掉。结果放在下一轮系统提示的开头，并标出覆盖了前多少条。`LearnBusy` 期间界面能看到还在写。

### 5.10 知识库（已落地）

`internal/knowledge` 用 `<dataDir>/knowledge.jsonl`（追加写，删除时整体重写）。

- 创建会话时**不**注入笔记。首条 system 只说明自己在这个应用里工作
- 挂到会话上的 id 存在 `Agent.Knowledge`。loop 开始时 `View` 按这些 id 拼进系统提示，预算大约 1600 字
- 界面按引用人数排序，能挂、能摘
- `save_note` 和一轮结束后的蒸馏都会写笔记。蒸馏只在模型判断值得长期记住时落盘
- 最近几轮明显集中在前端 / 后端 / 审查 / 测试时，简介会收成这块。名字里还写着另一块时会改名，并在对话里告诉用户

### 5.11 模型与诊断（已落地）

| Provider | 接入 |
|---|---|
| `mock` | 本地规则，假回复 |
| `llamacpp` | 本机 `llama-server`，默认 `http://127.0.0.1:8080`，不要 API Key |
| `ollama` | 本机，默认 `http://127.0.0.1:11434` |
| `openai` / `deepseek` / `claude` | OpenAI 兼容。Claude 需要网关，不是原生接口 |
| `qoder` | 自己的对话协议。浏览器授权，或 `pt-` Personal Access Token / 访问令牌。默认模型 `DeepSeek-V4-Pro` |

`ListAllModels` 把已保存的连接摊成一份列表。Qoder 用账号拉到的目录，没有目录时用内置表，能思考的模型带推理档位。

`StaleBuild` 只在当前目录像一份源码树（有 `go.mod`，且不是 `/` 或用户主目录）时，把非测试 `.go` 的修改时间和本进程的可执行文件比一下。诊断面板在源码更新、进程还是旧的时提示重启。打包后的进程不做这次扫描，避免碰到桌面和文稿。

---

## 6. 交互体验设计

### 6.1 流式输出

**原则**：不打断用户阅读，不强制滚动。

**实现**：
- 跟踪用户是否在底部（`pinnedToBottom`）
- 在底部：新内容自动跟随滚动
- 不在底部：保持当前位置，不强制跳转
- 用户回到底部：恢复自动跟随

主窗口用 `agent.delta` 做流式。

### 6.2 连续对话

正在跑的时候再发，本会话的句子会在下一步模型调用前插进当前轮，输入框会说明这一点。这条用户消息还没落进历史时，界面先标「排队中」。

- **停止**：取消当前，并清掉还没插进去、还在队列里的消息
- 没有单独的「跳过」按钮

### 6.3 Agent 状态可视化

**侧栏状态点**：
- 灰：空闲
- 橙、闪：执行中
- 强调色、闪：排队
- 红：出错
- 红、闪：`blocked`，在等人。文案用 `blockedReason`，没有就写「等你处理」

正常结束回到灰色空闲，不留一颗绿色的「完成」点。

**检查器**：工具、知识两个页。工具行来自 `agent.tool`。没有改动页，也没有任务页。

**舰队**：并排看状态、当前任务摘要、最近输出和产出量，不展开完整对话。

### 6.4 打断

- 点击「停止」取消当前 stream，已生成的内容留在历史里，队列被清空
- 飞书正在用这个会话写卡片时，停止也会停掉那一轮
- 用户消息和其它 agent 的消息走同一条队列，没有单独的优先级

---

## 7. 已知限制

| 限制 | 状态 | 说明 |
|---|---|---|
| 每个会话一条队列，连发不乱序 | ✅ 已落地 | `workerLoop`。跑的过程中本会话新消息会插进下一步 |
| agent 可以互相派活 | ✅ 已落地 | `Route`、`ask_agent`、`create_agent`；批量拆任务走 Supervisor |
| 会话持久化 | ✅ 已落地 | `agents.json`。不是 SQLite |
| 群聊 | ✅ 已落地 | `discuss` / `direct` / `roundrobin` |
| 会话级模型与目录 | ✅ 已落地 | 输入栏选择，凭据在设置里 |
| 知识库 | ✅ 已落地 | 挂上的笔记下一轮注入。创建时不塞全局 top-8 |
| 读写代码与 shell | ✅ 已落地 | 工作目录内。bash 有 60 秒上限，不流式 |
| 内置浏览器 | ✅ 已落地 | `web` 工具 + 单独窗口 |
| 飞书多机器人 | ✅ 已落地 | 长连接、权限、助手认领、房间只读 |
| 学习摘要 | ✅ 已落地 | 子会话或本会话自己写，不挡当前回复 |
| 父子会话 | ✅ 已落地 | `ParentID`，侧栏嵌套，总数 8 |
| Qoder、StaleBuild、模型选择 | ✅ 已落地 | 见 5.11 |
| 检查器的改动 / 任务 | ❌ 未做 | 页已经拿掉，没有结构化数据 |
| 工具审批 | ✅ 已落地 | 写前快照 + 结构化 diff；允许一次 / 本会话允许；5 分钟超时，停止即取消 |
| 命令审计 | ✅ 已落地 | `<dataDir>/audit.jsonl`，记会话、命令、状态、退出码、耗时；危险命令前置拦截 |
| git worktree | ✅ 已落地 | 会话级开关；工作树在 `.mad-worktrees/<仓库>/`，分支 `mad/<slug>`；仅 git 仓库生效 |
| Supervisor 自动拆任务 | ✅ 已落地 | `backend=supervisor`，侧栏入口；2-4 个子任务、20 分钟总时限、子会话默认各建 worktree |
| shell 流式输出；手动命令的超时 | ❌ 未做 | 手动执行只在取消时杀进程组 |
| API Key / 飞书 Secret 明文 | 🟡 未做迁移 | `config.json` 权限 600；会话 Key 在 `agents.json`。见 `TODO(secrets)` |
| 历史改 SQLite | ❌ 未做 | 仍是整份 JSON |
| agent id 改 ULID | ❌ 未做 | `agent-<时间戳>` |
| 未签名 | 🟡 | Gatekeeper / SmartScreen 会拦 |

---

## 8. 参考

- MyGo: https://github.com/egoist/mygo
- 架构评审历史：`docs/reviews/`（提案，不是当前行为）
