# 设计文档：multi-agent-desktop

> 版本：0.5.0
> 最后更新：2026-11-25

这一份写的是**代码里已经落地的行为**。`docs/reviews/` 里的评审和方案是历史提案，不代表现在能用。

---

## 1. 产品定位

跨平台（macOS / Windows / Linux）的多 Agent 开发工具桌面程序。
N 个 AI agent 并行工作，各自独立会话。用户可以看舰队和对话、中途插话、停止、改这个会话的模型和工作目录。

**当前形态**：多 agent 会话 + 进程内工具循环（读写文件、shell、内置浏览器、会话之间派活）
+ 三档权限模式与写操作审批（应用内卡片 **和飞书卡片**）+ 命令审计
+ 多根工作区（主目录 + 附加目录）+ git worktree 隔离
+ 频道（共享 transcript + 投递记录）+ 知识库 + 技能 + 飞书多机器人 + Supervisor 自动拆任务。

**还没做**：检查器里的改动/任务数据、shell 流式输出和手动命令的超时、API Key 进钥匙串、历史改 SQLite、agent id 改 ULID、打包签名。

**待决策（不是已落地）**：产品定位是否收窄为「飞书里的值班排障 copilot」。
依据见 `docs/reviews/research-product-value-and-maturity.md`——抓取的 14 个同赛道竞品无一支持飞书，
而「多会话 + worktree」这一格最接近的开源项目 Crystal 已于 2026-02 废弃。**这条还没定，不要当结论用。**

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

### 2.4 运行时协议：统一事件 + 可替换执行体

MyGo IPC 解决前端 ↔ Go，`internal/llm` 解决 Go ↔ 模型，中间的「谁在执行这一轮」由
`internal/runtime` 统一：

- `Runtime` / `Session`：`Prompt` / `Cancel` / `RespondPermission` / `Events` / `Close`
- `Event`：`turn.started`、`text.delta`、`tool.*`、`permission.request`、`turn.finished` 等
- `EventSink`：唯一分配 `seq`、阻塞发送形成背压、关闭时等生产者退出

两个实现：

| 实现 | 路径 | 说明 |
|---|---|---|
| `native` | `internal/runtime/native.go` | 包装进程内 loop；`loop.Event` 经 `runtimeEventFromLoop` 翻译 |
| `acp` | `internal/runtime/acp` | JSON-RPC over stdio，接 Codex / Grok / DeepSeek Harness 等 |

**为什么要这一层**：换执行体时前端不用改；未知事件可降级为 notice；ACP 的
`sessionId` 不会污染本地会话标识。完整契约、映射表与接入步骤见
**[runtime-protocol.md](runtime-protocol.md)**。

当前状态：协议层与 ACP 客户端已实现并有单测；现有执行路径仍是 native，
切换是独立改动，避免一次动到在用的会话。

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

### 3.2b 字号刻度与 token 校验（已落地）

字号刻度：`--text-xs: 11px` / `sm: 12` / `base: 13` / `md: 14` / `lg: 16` / `xl: 20`。
**桌面正文不低于 13px，次要信息不低于 11px。** 旧刻度（10.5 / 11.5 / 12.5）在 macOS 上偏小，
是「界面看着廉价」的主因；CSS 里 20 处、TSX 里 35 处硬编码小字号已全部收到 token。

**token 必须校验存在性**。曾经有 7 个被引用的 token 从未定义，其中 `--accent` / `--accent-soft`
让用户气泡的 `color-mix` 整条声明静默失效——气泡一直是全透明的，而 e2e 把这个 bug 断言成了「设计规范」。
现在 `--accent` / `--accent-soft` 是 `--accent-primary` / `--accent-dim` 的别名。

> 教训：**CSS 引用不存在的变量不会报错，只会静默变成透明或默认值。**
> 加/改 token 后跑一遍「已定义 vs 被引用」的差集检查。

亮色氛围层必须克制：网格 `0.016`、光斑 `0.16`、不用暗角。
三样叠加会把白底染成蓝紫渐变，这是「发灰、发脏」的来源。

阅读宽度：单聊、频道、飞书三个视图统一 `.c-transcript` 的 820px。

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
| `tool.approval` | Go → 前端 **和飞书** | 一张待批卡；两侧同时出，任一侧决定后另一侧收起 |
| `tool.approval.done` | Go → 前端 **和飞书** | 这张卡已有结果，收起按钮 |
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
| `read_file` / `write_file` / `edit_file` | 只碰主目录 + 附加目录里面的文件（见 5.5b） |
| `bash` | 不读登录脚本。默认 60 秒，超时杀进程组，输出有上限，整段返回 |
| `web` | 操作内置浏览器。先看控件，再用编号或页面上的文字。`steps` 最多 12 步，中间步骤不把页面交回 |
| `list_agents` / `ask_agent` / `create_agent` / `delete_agent` | 看会话、等对方答完、建子会话、删自己或自己建的子会话 |
| `delegate` | 把当前任务切成 1-3 块并行做，各建子会话在独立工作树里，汇总回父会话 |
| `list_notes` / `save_note` | 读知识库、写一条并挂到当前会话 |
| `read_skill` / `save_skill` | 取技能全文、把走通的流程沉淀成技能（见 5.9c） |
| `set_profile` | 改自己的名字、简介或工作目录 |

工具表按会话实际情况裁剪：到 `maxDelegateDepth` 时 `delegate` / `create_agent` **不出现在表里**，
而不是调用时报错——省掉一轮白跑的模型调用。没有技能库时 `read_skill` / `save_skill` 同样不装。

`mock` backend 仍走 `executeMock`，不调用这些工具。loop 配上 mock 或空模型，会直接报「这个会话还没有模型」。没有工作目录，或目录是主目录、盘符根、桌面、文稿、下载、图片、影片、音乐本身，这一轮报错。

防打转分两种：**参数不合 schema** 的失败标 `Meta.BadArgs`，单独计数、额度翻倍（写法问题，改一下就能继续）；
真正的执行失败连续 3 轮无进展才终止。终止理由带上最后一次真实错误，不再是一句无信息量的「all failed」。

预算默认 24 轮、64 次工具、10 分钟墙钟。上下文超过窗口时先压缩，保留系统提示和最近大约 8000 token。同一会话的检查点在 `<dataDir>/loop/runs`，本会话消息能接上时可以续跑；频道里的这一轮不续跑。

**一套页面，两个宿主（2026-11-26 落地）**：前端只认 `globalThis.mygo` 的 `call/on/once`。
桌面窗口里由原生宿主注入；浏览器里由 `src/lib/mygo-http.ts` 装一个同签名对象，
`call` 转 `POST /api/rpc`、`on` 转 `GET /api/events`（SSE，与桌面共用同一个 Broker）。
`/api/rpc` 不另写端点，而是反射到 `*API` 的同一份方法表（`internal/gateway/rpc.go`），
因此桌面与浏览器不会出现「一边有、一边没有」。`main.tsx` 的判定顺序是
宿主注入 → 网关可达 → 预览替身，预览只在前两者都不成立时启用，且写操作必须显式说明未执行。

**系统提示缓存边界**：系统提示在装配时拆成两段——静态段（身份、行为规则、工具使用规则、边界防御声明）与动态段（工作区根、渠道提示、技能清单/学习摘要/领域板/知识命中等会话级注入块）。静态段拼在最前、动态段固定尾部，两段在构造时冻结，循环每轮原样重申；静态段在同一运行内逐字节不变，为 OpenAI 兼容 / DeepSeek 等服务端自动前缀缓存提供稳定前缀。当前所有 provider 都走 OpenAI 兼容网关，请求体**未打** Anthropic `cache_control` 标记；待接 Anthropic 原生客户端后，在静态段末尾补 `cache_control`（OpenAI 兼容形状不挂）。

写操作在 `Registry.Invoke` 里分两步：先调工具的 `Preview`（标题、命令、diff），策略拦截的直接以 `denied` 结束；再由审批中心挂起，等卡片决定。**权限模式先于逐次审批**（见 5.9b）。允许一次只放行这一次，本会话允许按「会话 + 工具」记忆，删会话即失效。等待上限 5 分钟，ctx 取消（按停止）也会收起卡片。

卡片同时出现在应用内和飞书那条对话里，任一侧决定后另一侧收起。

每次有副作用的调用（`bash` / `write_file` / `edit_file`）追加一行到 `<dataDir>/audit.jsonl`，字段包括会话、命令或文件、状态、退出码、耗时。前端只读地拿最近记录，不提供修改入口。危险命令（根目录/系统目录递归删除、格式化、写块设备、关机重启、fork bomb）由 `shellguard` 在执行前拦下，bash 的 `Preview` 与 `Execute` 各检查一次，防绕过注册表直调。

### 5.3 Agent 间协作与父子（已落地）

两条路并存：

- `Manager.Route(from, to, text)`：显式把一条消息放进对方队列。界面上是路由气泡
- 模型工具：`ask_agent` 按名字把任务交给已有会话并等待回复；`create_agent` 新建一个闲置助手；
  `delegate` 把当前任务切成 1-3 块，各建一个子会话在独立工作树里并行做，最后汇总回父会话

`ParentID` 记在 `Agent` 上，并进 `Save` / `Load` / `snapshot` / `viewMap`。侧栏按它嵌套。三种子会话都会带上父 id：

- 模型调用 `create_agent`
- 飞书主会话忙时认领或新建的「…的协助」，同一父会话最多 3 个
- 学习摘要在还没有子会话、人数也没到 8 时建的「…的学习」。摘要本身是一次单独的模型调用，不进那个子会话的队列。有空闲子会话就用它的名字和模型；已经有子会话但都不空闲，或人数满了，就用本会话的模型

**名额分池**：`maxChildAgents = 6` 只算模型自建的子会话，用户自己建的会话不占名额、也不设这个上限。
旧实现用一个总数上限 8 算所有会话，用户建满之后 `len(List()) >= 8` 永远为真，
`create_agent` / `delegate` / 学习子会话**全部永久失败**（真实运行记录里 create_agent 的 7 次失败全是这一条）。
名额满且有空闲子会话时**复用**（改名后派活），都在忙才拒绝并提示改用 `ask_agent`。

`delete_agent` 只能删自己，或自己创建的子会话。互相 `ask_agent` 等待会被拦住。

#### 派活（`internal/agent/delegate.go`）

Supervisor 和 `delegate` 工具共用同一套执行内核 `runSpawn`，区别只在谁给出子任务列表：
Supervisor 让模型先拆一次（`planWithModel`），`delegate` 由模型在工具调用里直接给出（不再拆第二次，避免双重推理）。

上限与语义，逐条对照行业一手规则（依据见 `docs/reviews/research-multi-agent-orchestration.md`）：

| 项 | 取值 | why |
|---|---|---|
| 单次子任务数 | 3 | Anthropic research 是 3-5；本仓库会话上限 8，父占 1，留飞书助手余量 |
| 嵌套层数 | 2 | Claude Code 是 3 层但 20 并发；8 个名额下 3 层会撞满 |
| 墙钟 | 子 10 分钟 / 父 20 分钟 | 父超时只停止等待，**不取消**子会话 |
| 审批 | 不审批 | OpenAI `as_tool` 默认不审批；子会话内的写操作照旧走 `Registry` |

三条刻意的语义：

- **名额不足时整个派活失败**，不静默少派。悄悄少派两个会让父会话以为活干全了
- **部分产出回传**并标 `partial`，不因为超时把已有文字整块丢掉
- **父超时只断等待**，子会话用独立 ctx 继续跑完，结果留在它自己的会话里，用户仍能进去看

`Depth` 记在 `Agent` 上（用户自己建的为 0），进 `Save` / `Load` / `Snapshot` / `viewMap`。
到 `maxDelegateDepth` 时 `delegate` 和 `create_agent` **不出现在工具表里**，而不是调用时报错——
省掉一轮白跑的模型调用（Claude Code 的做法）。`ask_agent` 不占名额，任何层都能用。

Supervisor（`backend=supervisor`）：侧栏「自动拆任务」填一句目标，`executeSupervisor` 让模型拆一次，
之后走 `runSpawn`。拆解提示词收窄到官方点名的三种适合场景（大量独立调查 / 信息量超过一个上下文 / 需对接多种工具），
并显式禁止「并行改同一批文件」。模型没给出可用方案时退化成一个子任务，不报错。群聊里不能派子任务。

### 5.4 群聊（已落地）

频道在 `internal/channel`。持久化 `<dataDir>/channels.json`。`DeleteAgent` 会 `RemoveMember`。

回合策略：

| 策略 | 谁回 |
|---|---|
| `discuss` | 被 @ 的回。没人被 @ 时，只由当前 speaker 接；speaker 空着或不是成员，就没人接 |
| `direct` | 成员都回 |
| `roundrobin` | 成员都回 |

`DecideReplies` 是唯一判定，返回 `Decision{Reply, Reason}`。绑定层只投递，不做判断。

**共享 transcript**：`BuildTranscript(prior, 2000)` 从频道消息构建群上下文，`Render(agentId)` 按成员裁剪后
作为 system 段进这一轮的 prompt。所以成员**能看见彼此说了什么**，不是各说各话。
`Agent.Messages` 只留自己的轮次，群上下文每轮现算。

**投递结果是数据不是异常**：每个成员一条 `Delivery{AgentID, Status, Reason}`，
状态 `delivered` / `skipped` / `failed`，`skipped` 也带原因。`SetDeliveries` 只回填这个字段并重发同 ID 事件，
前端 `DeliveryRow` 直接显示「谁收到了、谁没收到、为什么」。
早期版本把失败拼成字符串抛出、前端 `.catch` 吞掉，用户看到「发出去了」其实没人收到——那条已修。

领域结论（知识蒸馏命中某一块专业）会发到同名频道，下一轮的系统提示里带上别人在这块记下的几条。

飞书房间不是这里的频道。见 5.7。

### 5.5 事件广播

`agent.Manager` 持 `Emitter` 函数，把事件转成 MyGo `Event.Emit`：
- 解耦：agent 包不依赖 MyGo
- 可测试：单测时换成 channel 收集事件
- `emitToWindow` 只做「字符串事件 → 强类型 payload → Emit」的转发

### 5.5b 工作区是一组根（已落地）

`Agent.WorkDir` 是主目录，`Agent.ExtraDirs` 是附加目录，合起来由 `workdir.Roots` 校验和解析。

- **文件工具**（read/write/edit）能读写主目录 + 附加目录
- **bash 的 cwd 仍是主目录**：命令在哪跑必须是确定的，否则相对路径语义会漂
- 相对路径先在主目录解析，不存在时再逐个附加目录试；绝对路径校验落在任一根内
- 附加目录同样过 `TooBroad`（不能是主目录/桌面/文稿/下载），且必须真实存在
- **不从附加目录加载技能或知识**：放宽的是文件访问，不是配置

依据见 `docs/reviews/research-workspace-roots.md`（Grok Bot 的 `workspacePaths`、
Claude Code 的 `--add-dir` / `permissions.additionalDirectories` 一手证据）。
单根时行为与改造前完全一致，`roots_test.go` 守住这条。

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
- 权限检查对照六项，分必需和可选，见下表。设置页「检查权限」逐项列出：缺哪些、scope 名、缺了会出现什么现象，另有「已有 N 项」和「租户已授予的 N 个 scope」可以展开看。只有缺必需项时才弹开通页

  | 能力 | scope | 必需 | 免审 |
  |---|---|---|---|
  | 发消息 | `im:message:send_as_bot` | ✅ | 是 |
  | 接收单聊 | `im:message.p2p_msg:readonly` | ✅ | 是 |
  | 接收群里被 @ 的消息 | `im:message.group_at_msg:readonly` | ✅ | 是 |
  | 查看所在群 | `im:chat:readonly` | ✅ | 是 |
  | 标处理中表情 | `im:message.reactions:write_only` | ❌ 可选 | 是 |
  | 读取群里**全部**消息 | `im:message.group_msg` | ❌ 可选 | **否，要审核** |

  只被 @ 的权限是免审的，`im:message.group_msg` 不是。开着前四项就能在群里被 @ 后回答，
  缺 `im:message.group_msg` 只影响**主动补拉群历史**，不影响事件推送进来的消息
- 缺权限时**从飞书的报错正文里取 `need scope: xxx`** 显示（`missingScopeFromError`），
  不靠静态清单猜——`230027` 这类错误码只有正文里才写清缺的是哪一项
- `MentionOnly` 的群**照旧补拉历史并记进房间**，只是不主动接话。原来这里直接 return，
  导致群记录永远是空的
- 长连接进入 `connected` 时立刻 `ensureBot`，`spokenTo` 在 `botID` 为空时再同步补取一次。
  `botID` 是「群里被 @ 了吗」的判断依据，缺了会退化成「见到 @ 了某个 bot 就当在叫我」
#### 身份：应用 vs 用户

`internal/feishu/cred.go` 的 `Cred` 决定一次调用用谁的身份。

| | tenant_access_token | user_access_token |
|---|---|---|
| 权限来源 | 应用被授予的 scope | **这个人本人在群里的可见范围** |
| 读群全部消息 | 要 `im:message.group_msg`（**需审核**） | 只要这个人在群里 |
| 过期 | 由 `tenantToken` 自动续 | 约 2 小时，用 `refresh_token` 续 |

`Cred.token()` 的顺序是**先用户后应用**：有用户令牌且没过期就用它，过期就刷新；
刷新失败退回应用身份——应用身份读不了群消息，但单聊和发消息都正常，
不能因为没授权用户身份就整个不能用。

三个读接口各有 `xxxAs(ctx, Cred, ...)` 形式，旧签名转发到应用身份，测试不受影响：

- `HistoryAs` / `ListChatsAs` / `OpenStopsAs`

`Service.cred()` 从 `Config` 拼出 `Cred`，`replayChat` / `catchUp` / `closeHung` 都走它。
`Config` 因此带了函数字段 `OnUserRefresh`，`hub.sameConfig` 逐字段比较，跳过函数。

刷新结果通过 `OnUserRefresh` 写回 `config.json`（`main.go` 的 `botRefresher`）。
不写回去的话进程重启后又拿到过期令牌，只能重新授权。

授权完成后 `probeUserRead` 立刻用新令牌试读一次群消息，把结论（能读几个群、几条消息、
还是缺哪个 scope）直接显示在设置页，不用等用户自己猜。
- 主会话忙时 `Assist` 挑一个空闲助手；没有就新建，沿用主会话**真正在用的**模型和工作目录。
  取的是 `EffectiveLLM`，父会话用全局默认时回落全局——只看 `LLMCfg` 会让助手开出来就没模型，第一条消息必失败。
  权限模式也跟着父会话，否则助手在飞书里又变成「等一张看不见的卡」。助手满了返回 busy，等这一轮结束再派
- 飞书协助**不再建学习子会话**：它只答一条消息就交还，给它配「协助的学习」纯属占名额
- 模型已经说出的句子和工具行写回飞书卡片
- `FeishuRoomView` 只展示 `feishu-rooms.json` 里的记录。`SetFeishuRoomAgent` 从下一条起改由另一个本机会话回答

### 5.8 内置浏览器（已落地）

`browser.Desk` 用名为 workbench 的 `mygo.Session`，单独窗口。`OpenWorkbench` 和 `web` 工具都走它。地址只接受 `http` / `https` / `about`。登录 cookie 留在这个窗口，不使用用户的 Chrome。页面状态记在 `browser-page.json`。窗口关掉后不再留着，下次打开再创建。

**引擎是 WebKit（Safari），不是 Chrome**。只支持 Chrome 的页面特性这里可能没有，工具描述里写明了这一点，遇到要换路径。窗口有菜单：⌘R 刷新、⇧⌘R 强制刷新、⌘[ 后退、⌘] 前进、⇧⌘C 复制地址。

三个已修的卡点：

- **控件编号跟着节点走**，不再每次 snapshot 重排。页面重渲染一次就换掉编号，会让模型反复 snapshot 重试
- **弹层优先纳入**：下拉/对话框挂在 DOM 末尾，按页面顺序取前 N 个会把选项挤掉。现在 dialog/listbox/menu/popper 优先，上限 100
- **重定向不再干等**：停止加载且非空白页即算落定（2 秒宽限），把「已跳转到 X」写进返回。
  原来要求 URL 必须等于目标，站点跳登录页就永远不匹配，每次白等 20 秒

参数兼容：`steps` 里补齐 `timeoutMs / urlContains / mods / dx / dy`；只给 `steps` 不给 `op` 不报缺字段；
`type` 只写 `text` 时当作输入内容；收下同义动词 `goto/fill/refresh/screenshot/hover/press`。

### 5.9 学习摘要（已落地）

一轮结束后 `studyHistory` 在后台跑，不挡住已经显示的回复。最近 8 条留在窗口里，更早的按最多 3 步、每步 20 条或大约 3000 token，用一次单独的模型调用收成摘要。有空闲子会话时记它的名字；否则在没有子会话且人数未满时先建「…的学习」。已经有子会话但都不空闲，或人数满了，就用本会话的模型。摘要里出现、原文里没有的路径会被整句丢掉。结果放在下一轮系统提示的开头，并标出覆盖了前多少条。`LearnBusy` 期间界面能看到还在写。

### 5.9b 权限模式与飞书审批卡（已落地）

`internal/approval` 给每个会话一个权限模式，输入栏底栏的 `PermissionPicker` 切换：

| 模式 | 语义 |
|---|---|
| `ask` | 每次写操作都问（默认） |
| `auto` | 工作目录内的写与命令直接执行，不再逐次问 |
| `readonly` | 只读，写操作**直接拒绝**，不挂卡也不等人 |

**刻意不做「完整权限」**：工作目录是工具层的硬边界（文件工具不出目录、bash 的 cwd 就是它），
审批模式不该能把这条边界放开。无论哪一档，`shellguard` 对危险命令的拦截都照旧生效。
不认识的模式一律回落 `ask`；模式是运行期状态，重启回到最保守的一档。

切模式时**还挂着的卡立刻有结果**（auto 放行 / readonly 拒绝），不让它们继续等到超时。

审批卡同时发到飞书：`Service.Ask` 把卡片发在正在回答的那条对话里，三个按钮
「拒绝 / 允许一次 / 本会话允许」，点完 `Hooks.Decide` 送回 `approval.Center`，卡片就地改写成结果。
桌面先处理过时 `Hub.CloseAsk` 收掉飞书上还挂着的按钮。
**没有这条链路时，用户在飞书里提问、审批卡弹在应用内，5 分钟后自动拒**——这是最难受的失败方式。

飞书侧同时支持 `/help`、`/stop`、`/status`、`/use 名字` 四条指令。

### 5.9c 技能（已落地）

`internal/skill` 存「怎么做某类事」的步骤，文件在 `<dataDir>/skills/*.md`，带 frontmatter（name / description）。

和知识库分工：**note 存这个项目的事实与结论，skill 存跨项目复用的步骤**。

加载按需：系统提示只放名字和一句话描述（`skill.Catalog`，最多 20 条），
模型判断用得上时用 `read_skill` 取全文。把所有正文塞进每一轮上下文既烧预算又稀释注意力。

- `read_skill(name)`：取全文，名字或 id 都能找
- `save_skill(name, description, body)`：走审批，让会话把走通的流程沉淀下来

检查器「能力」页列出这个会话此刻的工具与技能，**不可用的直接写明原因**（没文件夹 / 没模型 / 浏览器没开），
数据来自 `API.AgentCapabilities`，不是前端硬编码的清单。

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

`ListAllModels` 把已保存的连接摊成一份列表。Qoder 用账号拉到的目录，能思考的模型带推理档位。

**Qoder 的两条已修问题**（都属于「用假成功掩盖真失败」）：

1. **检查不能比执行更严**。`QoderAccountStatus` 曾在 `qoderIdentity` 之后再加一道 `apiKey == access` 严格相等。
   access token 会自动刷新，刷新后 config 里存的旧 key 永远对不上 → **对话能用，连接页却报红**。
   现在检查复用执行同一套判定；刷新后通过回调把新 Key 回写 `config.json`，根治状态不一致。
2. **目录拉取失败不再静默退回内置表**。`qoderResolve` 原来吞掉错误返回内置目录，
   于是「Key 失效」被伪装成「模型列表正常」。现在错误上抛。

界面上的版本号取自 `mygo.App.Version()`（`SystemInfo.Version`），不在前端写死字符串。

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

**检查器**：工具、知识、**能力**三个页。工具行来自 `agent.tool`。没有改动页，也没有任务页。
能力页列出这个会话此刻的工具与技能，**不可用的直接写明原因**（没文件夹 / 没模型 / 浏览器没开），
数据来自 `API.AgentCapabilities`，不是前端硬编码清单。

**侧栏**：频道 / 飞书 / 会话三组可折叠，带条数，折叠状态记在本机。
删除按钮平时不出现，悬停或键盘聚焦到那一行才显形。
**⌘K 快速跳转**把三类目标放进同一个列表，输入名字直达——条目一多时这比滑动查找便宜得多。
三类行都是 `role="button"` + `tabIndex`，Enter / Space 可打开。

**删会话要带走它的全部临时状态**：`errors / shellByAgent / reasoning / pace / tokenUsage / stops` 按 id 清，
`tools` 按 id 过滤。其中 `tools` 是用户可见的——检查器拿的是全部会话的工具行，不清就永远留在那里。

**舰队**：并排看状态、当前任务摘要、最近输出和产出量，不展开完整对话。

### 6.4 打断

- 点击「停止」取消当前 stream，已生成的内容留在历史里，队列被清空
- 飞书正在用这个会话写卡片时，停止也会停掉那一轮
- 用户消息和其它 agent 的消息走同一条队列，没有单独的优先级

---

## 7. 已知限制

| 项 | 状态 | 说明 |
|---|---|---|
| 每个会话一条队列，连发不乱序 | ✅ 已落地 | `workerLoop`。跑的过程中本会话新消息会插进下一步 |
| agent 可以互相派活 | ✅ 已落地 | `Route`、`ask_agent`、`create_agent`、`delegate`；批量拆任务走 Supervisor |
| 子会话名额 | ✅ 已修 | `maxChildAgents = 6` **只算模型自建的**；用户自建不占名额也不设上限。旧的总数上限 8 会让派活永久失败 |
| 空闲子会话复用 | ✅ 已落地 | 名额满且有空闲子会话时改名复用，都在忙才拒绝 |
| 会话持久化 | ✅ 已落地 | `agents.json`。不是 SQLite |
| 群聊 | ✅ 已落地 | `discuss` / `direct` / `roundrobin`；共享 transcript + 每人一条投递记录 |
| 会话级模型与目录 | ✅ 已落地 | 输入栏选择，凭据在设置里 |
| **多根工作区** | ✅ 已落地 | 主目录 + 附加目录；文件工具可读写全部根，bash 的 cwd 仍是主目录。见 5.5b |
| 知识库 | ✅ 已落地 | 挂上的笔记下一轮注入。创建时不塞全局 top-8 |
| **技能** | ✅ 已落地 | `<dataDir>/skills/*.md`；清单进提示、正文按需 `read_skill`。见 5.9c |
| 读写代码与 shell | ✅ 已落地 | 边界内。bash 有 60 秒上限，不流式 |
| 内置浏览器 | ✅ 已落地 | `web` 工具 + 单独窗口 + 刷新菜单。**引擎是 WebKit，不是 Chrome** |
| 飞书多机器人 | ✅ 已落地 | 长连接、双身份、权限自检、助手认领、房间只读 |
| **飞书审批卡** | ✅ 已落地 | 卡片发在对话里，拒绝/允许一次/本会话允许；任一侧决定后另一侧收起。见 5.9b |
| **权限模式** | ✅ 已落地 | `ask` / `auto` / `readonly` 三档，输入栏切换。**刻意不做「完整权限」**：工作目录是工具层硬边界 |
| 学习摘要 | ✅ 已落地 | 子会话或本会话自己写，不挡当前回复。飞书协助与学习助手自身不再建学习子会话 |
| 父子会话 | ✅ 已落地 | `ParentID`，侧栏嵌套；深度上限 2 |
| Qoder、StaleBuild、模型选择 | ✅ 已落地 | 见 5.11 |
| 工具审批 | ✅ 已落地 | 写前快照 + 结构化 diff；允许一次 / 本会话允许；5 分钟超时，停止即取消 |
| 命令审计 | ✅ 已落地 | `<dataDir>/audit.jsonl`；危险命令前置拦截，不受权限模式影响 |
| git worktree | ✅ 已落地 | 会话级开关；`.mad-worktrees/<仓库>/`，分支 `mad/<slug>`。**解绑与物理删除分离**：删不掉也能关掉 |
| Supervisor 自动拆任务 | ✅ 已落地 | `backend=supervisor`；单次最多 3 个子任务、20 分钟总时限、子会话默认各建 worktree |
| 失败可解释 | 🟡 一半 | 终止理由带真实错误、参数错误单独计数；`tools/runs.py` 可自查。**还没做进界面** |
| 视觉与可读性 | 🟡 持续 | 字号刻度、失效 token、氛围层、阅读宽度已修；16 张界面截图巡检见 `e2e/visual-audit.spec.ts` |
| 检查器的改动 / 任务 | ❌ 未做 | 页已经拿掉，没有结构化数据 |
| composer 未抽成模块 | ❌ 未做 | 单聊/群聊各一个输入框，切换丢草稿。见 `docs/reviews/review-recheck-2026-11-25.md` #5 |
| channel / agent 落盘重复 | ❌ 未做 | `goSave` / 原子写各一份。**纯结构收益，等真实需求压出来再抽**，不凭空做泛型 |
| shell 流式输出；手动命令的超时 | ❌ 未做 | 手动执行只在取消时杀进程组 |
| API Key / 飞书 Secret 明文 | 🔴 未做 | `config.json` 权限 600；会话 Key 在 `agents.json`。**交付给别人前必须解决** |
| 历史改 SQLite | ❌ 未做 | 仍是整份 JSON |
| agent id 改 ULID | ❌ 未做 | `agent-<时间戳>` |
| 打包签名 / 分发 | 🔴 未做 | Gatekeeper / SmartScreen 会拦。开发版 ad-hoc 签名导致钥匙串反复弹窗，`tools/dev-sign.sh` 可建固定身份 |

### 成熟度判定

**不是 demo，是「作者本人可用的生产工具」。** 离「能交给别人用」差三件：**签名分发、密钥进钥匙串、失败可解释做进界面**。
依据与同赛道竞品数据见 `docs/reviews/research-product-value-and-maturity.md`。

---

## 7.x 写安全：多会话改同一个仓库（2026-11-26 落地）

**问题**：多个会话/子 Agent 同时改同一份检出。`edit_file` / `apply_patch` 只保证"锚点唯一"，
但锚点唯一是内容级判断，挡不住"读完之后别人改了同一处"——后写者静默覆盖先写者。

**三层**：

1. **读过就记版本**：`read_file` 记 `size + mtime(ns) + sha1前12`，并在输出末尾回给模型；
   写工具带 `ifVersion`，或"读过且版本已变"即 `STALE_WRITE`，零写入（patch 整批原子）。
2. **冲突提前可见**：写操作通过审批、落盘前发 `agent.file.intent`，桌面顶部条与 SSE 客户端都能看见
   "谁在改哪个文件"。
3. **默认隔离 + 可合回**：`config.worktree.mode`（`children` 默认 / `all` / `off`）；子会话自动落在
   `.mad-worktrees/<repo>/<slug>`、分支 `mad/<slug>`；`worktree_merge` 先试 ff，冲突 `merge --abort`
   干净退回，双端不干净就拒绝。

**不做什么**：不做三方合并、不做跨机器锁、不做 CRDT。冲突的语义只有写的人知道，工具只负责把冲突
变成一次明确的失败。

**编码能力同源的三个闭环**（详见 `docs/reviews/req-code-capability-2026-11-26.md` §9）：verify 带相关测试、
连续 3 次未过即停并交人、`repo_map` 导航、`changeset` 收尾自查。

## 8. 参考

- MyGo: https://github.com/egoist/mygo
- 架构评审历史：`docs/reviews/`（提案，不是当前行为）
- **评审复核**：`docs/reviews/review-recheck-2026-11-25.md`——三份旧评审逐条对当前代码的核对结果。
  报告超过一个月未复核的按「待复核」对待，不能当决策依据
- 产品价值与成熟度：`docs/reviews/research-product-value-and-maturity.md`
- 工作区边界与 Grok Bot 实测：`docs/reviews/research-workspace-roots.md`
- 产品形态一手调研：`docs/reviews/research-product-forms.md`
