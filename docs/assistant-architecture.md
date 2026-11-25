# 助手化架构与流程

本文回答两个问题：

1. 现在的桌面项目要变成「OpenClaw 那种随时能找到的个人助手」，架构应该怎么分？
2. 前端、后端、渠道、记忆、调度的流程怎么走，才不会边做边返工？

配套文档：`docs/runtime-protocol.md`（Agent 运行时契约）、`docs/design.md`（现有桌面架构）。

---

## 1. 定位：不是二选一，是两个形态

| 形态 | 面向 | 入口 | 核心动作 |
|---|---|---|---|
| **驾驶舱**（现有桌面） | 你坐在电脑前 | 窗口 | 多 Agent 并行、看 diff、审批、检查器、worktree |
| **网关·内嵌**（当前，2026-11-26） | 你不在电脑前，但桌面开着 | 网页/本地 HTTP + SSE | 收消息、派任务、查进度；飞书仍走现有 Hub |
| **网关·常驻**（后续） | 你不在电脑前，桌面可能关着 | 飞书 / Telegram / 网页 | 同上，独立进程 + systemd |

两者共用同一个内核：`internal/agent`（会话、Queue/Worker）、`internal/runtime`（native / ACP）、
`internal/tool`、`internal/approval`、`internal/audit`、`internal/skills`。
`internal/gateway` 是一个库：内嵌形态由桌面 `main.go` 装配，常驻形态才由
`cmd/mad-gateway` 装配；**本轮只启用内嵌形态**。

**明确的非目标**：

- 不做第二个 OpenClaw。渠道只要 2–3 个真正在用的，不做全平台适配。
- 不把桌面 UI 塞进手机。网关用文本 + 卡片交互，复杂改动回桌面看。
- 不为「插件生态」重构内核。技能继续用 `SKILL.md`，按需加载。

---

## 2. 总体架构

```
 网页 / 本地 (HTTP+SSE) ─▶ internal/gateway（嵌入式）
                          │ 仅接管 web：属性过滤 / 去重 / 绑定
                          │ host：Manager / approval.Center / FileBinder / SSE Sender
 飞书 ──────────────────▶ feishu.Hub（现有，main.go 装配，不经过网关）
                          │
                          ▼
                    ┌──────────────────────────────┐
                    │ internal/agent (Manager)     │
                    │  Queue / Worker / 父子 / 摘要 │
                    └───────┬──────────────┬───────┘
                            │              │
              ┌─────────────▼───┐   ┌──────▼────────────┐
              │ internal/runtime│   │ internal/tool      │
              │ native / acp    │   │ 文件 / grep / patch│
              └─────────────────┘   └──────┬────────────┘
                                           │ 副作用
                              ┌────────────▼────────────┐
                              │ approval.Center + audit │
                              └────────────┬────────────┘
                                           │
                    ┌──────────────────────▼──────────────┐
                    │ internal/memory  internal/scheduler │
                    │ 笔记 / 检索 / cron / heartbeat      │
                    └─────────────────────────────────────┘
```

关键点：

- `agent.Manager` 的构造签名是 `NewManager(cfg, emit Emitter)`，**事件出口是注入的**。
  桌面注入 MyGo 事件；网关在同一个 `emit` 里旁路一份到 `host.OnAgentEvent`，供 SSE 与 web 回复。
- `internal/gateway` 不 import `internal/agent`；真实装配在 `internal/gateway/host`
  （`SessionPort→agent.Manager`、`ApprovalPort→approval.Center`、`Binder→桌面数据目录`）。
- **飞书不迁入网关**：`main.go` 的 `feishu.NewHub` 继续持有唯一一条飞书长连接。
  网关默认 `channels=["web"]`，对 feishu 入站返回 409 `CHANNEL_NOT_OWNED`；
  飞书绑定的 Agent 回复由 Hub 发出，网关跳过（`gateway.reply.skipped`），避免重复回复。
- `internal/runtime` 已经是统一事件层，native 与 ACP 对上层同形。
- 审批、审计、工作目录校验都在工具层，渠道层不重复实现。

---

## 3. 端到端流程

### 3.1 入站消息（被动）

```
飞书回调 ─▶ feishu.Hub（现有）──┐
网页/本地 ─▶ gateway.Service ───┤  渠道归属：网关只接管 web；feishu 不投网关
   ├─ 渠道归属（非接管 → 409 CHANNEL_NOT_OWNED）
   ├─ 消息去重（外部 message id）
   ├─ 绑定解析：外部 chat id → 本地 agent 会话
   ├─ 策略（mentionOnly / allowAll）
   └─ 即时回执（仅接管渠道）          │
                                     ▼
                    Manager.Prompt(agentID, text)
                          → Worker / native 或 ACP
                          → 工具调用 → approval.Center（需要时阻塞）
                          → 事件 → Emitter
                                ├─ 飞书：Hub 发卡片/回复
                                ├─ web：网关 Sender + SSE
                                ├─ 桌面：MyGo 事件
                                └─ audit：副作用留痕
```

规则：

- **一个外部会话对应一个本地 Agent**，不是全局一个。群聊按「群 + 机器人」建会话。
- 渠道消息是**不可信输入**。进入模型前只做归一化，不做「把消息当系统指令」的拼接。
- 网关忙时：先回执「收到，排队中」，再异步跑；不能假装已读。
- **不重复回复**：`agent.message`(assistant) / `agent.channel.reply` 只会发往绑定渠道；
  飞书绑定由 Hub 负责，网关看到非接管渠道只记 `gateway.reply.skipped`。

### 3.2 主动任务

```
scheduler（cron / interval / 一次性）
  → 创建系统回合（system turn），带任务说明
  → Manager.Prompt（复用同一队列与审批）
  → 结果：发到绑定渠道 / 写记忆 / 只落审计
```

约束：

- 静默时段（默认 23:00–08:00）不发消息，只记录。
- 每个任务有「上次运行 / 下次运行 / 连续失败次数」，连续失败自动降频。
- 主动任务默认只读工具；要让 cron 改文件，必须显式配置 `allowWrite`。

### 3.3 审批

```
工具 Preview → approval.Center
  → 桌面：ApprovalModal
  → 网关：渠道卡片（允许一次 / 允许并记住 / 拒绝）
  → 无论哪个入口，写同一份 pending，先到先得
  → 决定后：approval.done + audit(批准人、来源渠道)
```

风险：

- 渠道卡片必须校验点击人身份（owner / 白名单）。这是当前 P0-1 的修复点。
- 网关「自动批准」只能是只读工具；写操作永远人工。

---

## 4. 数据模型

| 实体 | 关键字段 | 归属 |
|---|---|---|
| `Agent` | id、name、role、runtime(native/acp)、acp{command,args}、workDir、permissionMode | `agents.json` |
| `SessionBinding` | agentId、channel、externalChatId、policy(mentionOnly/allowAll)、createdAt | `gateway-bindings.json`（桌面数据目录） |
| `MemoryNote` | id、kind(daily/fact/decision)、text、source、confidence、createdAt、expiresAt | `memory/*.jsonl` |
| `Schedule` | id、cron/interval、agentId、prompt、channel、quietHours、lastRun、failCount | `schedules.json` |
| `Approval` | id、agentId、tool、input、options、source(desktop/channel)、requester | 内存 + `audit.jsonl` |
| `RuntimeEvent` | v、seq、sessionId、turnId、kind、payload | 事件流，不落盘（检查点在 loop/） |

**单写者原则**：`agents.json` / `gateway-bindings.json` / `schedules.json` 只允许一个进程写。
内嵌形态天然满足：网关与桌面同进程，绑定文件落在同一个桌面数据目录。
若后续启用独立常驻网关，必须先解决与桌面的互斥（锁文件或桌面改只读客户端），否则一定丢数据。

---

## 5. 进程与部署

| 模式 | 进程 | 数据 | 适用 |
|---|---|---|---|
| **内嵌**（当前） | 桌面 App 进程内的 gateway host | 桌面数据目录（`gateway-bindings.json`） | 坐在电脑前、桌面开着，网页/本地入口 |
| **独立常驻**（后续） | `cmd/mad-gateway`（无 GUI） | 独立数据目录（需先定互斥） | N100、云主机、Tailscale |
| **远程客户端**（后续） | 桌面 App 连网关 HTTP/SSE | 网关持有 | 想用桌面 UI 看远程任务 |

当前拓扑（内嵌）：

```
┌ 桌面 App（main.go）
│  ├ agent.Manager
│  ├ approval.Center
│  ├ feishu.Hub ── 唯一飞书长连接（保持现状）
│  └ gateway host ── 127.0.0.1:8780，仅 web，HTTP + SSE
│       └ gateway-bindings.json（桌面数据目录）
└ 窗口（MyGo IPC，含网关页）
```

- 启动顺序：Manager / 审批中心 / Hub 就绪 → 启动网关 → 创建窗口；退出时先关网关再 `Manager.Shutdown()`。
- 端口占用只记日志 + `GatewayStatus.listenError`，桌面照常启动。
- 网关不自己连飞书；`MAD_GATEWAY_TOKEN` 为空时仅允许回环访问。
- 后续独立常驻形态才用 `systemd`、journald、独立数据目录；本轮不做，也不新增第二条飞书连接。

---

## 6. 前端信息架构（先改这块）

### 6.1 导航

现有侧栏是「Agent 列表」。助手化后需要把「会话」和「系统」分开：

```
┌ 顶部状态条：网关 在线/离线 · 运行中 N · 待审批 M · 队列 K
│
├ 工作区
│   ├ 会话（Agents）       现有 AgentView / FleetView
│   ├ 频道（Channels）     现有 ChannelView
│   └ 舰队（Fleet）        现有 FleetView
├ 助手
│   ├ 网关（Gateway）      渠道连接、绑定会话、白名单、最近入站
│   ├ 记忆（Memory）       每日笔记、事实、来源、删除/固化
│   └ 计划（Schedules）    提醒、cron、上次/下次运行、失败
└ 设置
```

### 6.2 每个页面的状态必须完整

| 页面 | 空态 | 加载 | 错误 | 禁用 |
|---|---|---|---|---|
| 会话 | 引导「新建会话 / 接入 ACP」 | 骨架行 | 显示原因 + 重试 | 运行中禁用删除 |
| 网关 | 「网关未启用」+ 启动说明 | 读取中 | 端口占用 / 装配失败原因 + 重试 | token 非空时只读 |
| 记忆 | 「还没有记忆」+ 导入入口 | 骨架 | 读取失败 | 删除需确认 |
| 计划 | 「还没有计划」+ 新建 | 骨架 | 调度器未启动 | 静默时段提示 |

原则：**不允许出现「看起来能点但没反应」的按钮**。没有后端就明确写「未接入」并禁用；
已经接上的页面（网关）必须显示真实状态，不能用「未接入」占位糊过去。

### 6.3 会话行要能看出「谁在执行」

每个 Agent 行/卡片增加：

- 运行时徽章：`native` / `ACP:grok` / `ACP:codex`
- 连接状态：ACP 进程 running / exited / auth required
- 本轮状态：idle / queued / running / waiting-approval / error
- 最近一次工具与耗时

### 6.4 流程清晰化的三个交互点

1. **审批集中**：顶部待审批数 + 一键打开队列；不再只靠弹窗。
2. **回合时间线**：一个会话里按「用户 → 思考 → 工具 → 审批 → 回答」折叠展示，而不是散落事件。
3. **失败可恢复**：断线、模型失败、预算耗尽，给「重试 / 换模型 / 打开日志」三个明确动作。

---

## 7. API 边界

| 边界 | 协议 | 说明 |
|---|---|---|
| 桌面前端 ↔ Go | MyGo typed IPC | 保持现状，不暴露到公网；网关页用 `GatewayStatus` / `GatewayBindings` / `GatewayDecideApproval` |
| 网页/本地 ↔ 网关 | HTTP + SSE（仅回环） | `/api/status`、`/api/agents`、`/api/bindings`、`/api/approvals`、`/api/events` |
| 飞书 ↔ Go | `feishu.NewHub`（现有） | 仍是唯一飞书长连接；不经过网关 |
| 网关 ↔ 渠道 | 各渠道 SDK / Bot API | 适配器接口统一：Start / Stop / Send / Inbound |
| Agent ↔ 外部运行时 | ACP over stdio | 已有，复用 |
| Agent ↔ 工具 | MCP（可选） | 以后接外部工具，不现在做 |

**不要**把 MyGo IPC 包一层 HTTP 当公网 API：它没有鉴权、没有速率限制、方法粒度是内部的。
网关 HTTP API 只暴露「会话、消息、审批、状态」四类；默认只接管 `web`，feishu 入站返回 409。

---

## 8. 里程碑

按「前端优先、契约先行」拆，每一步都能独立验收：

| 阶段 | 内容 | 验收 |
|---|---|---|
| **M0 契约**（1–2 天） | 写定 Agent 运行时字段、网关 API、事件映射、数据归属 | 文档评审通过；Go 类型先落地，不接 UI |
| **M1 前端 IA**（3–5 天） | 顶部状态条、导航分组、网关/记忆/计划三个页面的空态与只读态 | 无死按钮；`npm run build:web` 通过；e2e 覆盖空态 |
| **M2 运行时可见**（依赖当前三件套） | 会话行显示 native/ACP、连接状态、回合状态 | ACP 会话可创建，审批复用现有弹窗 |
| **M3 网关最小闭环**（已完成内嵌形态） | `internal/gateway` 作为库嵌入桌面：Manager/审批中心/FileBinder/SSE Sender 真装配，仅接管 web；飞书仍走 Hub | 启动桌面 `/api/status` wired 四项为 true；网关页显示真实状态并可审批；端口占用不阻断桌面 |
| **M3.5 独立常驻**（后续） | `cmd/mad-gateway` 独立进程 + 数据互斥 + 渠道迁入 | 桌面关闭后仍可派活；不与桌面抢写 `agents.json` |
| **M4 第二渠道**（3–5 天） | Telegram 长轮询 + 白名单 + 去重 | 非飞书渠道跑通同一套隔离与审批 |
| **M5 记忆**（1 周） | 每日笔记、`memory_search`、定时整理 | 跨会话能召回；有来源与删除 |
| **M6 计划**（1 周） | cron / 提醒 / 早报 / 静默时段 | 任务可停用；失败降频；不刷屏 |
| **M7 沙箱与分发**（按需） | Linux 沙箱、签名、自动更新 | 公网暴露前完成渠道白名单与注入测试 |

---

## 9. 与现有代码的映射

| 新能力 | 复用现有 | 新增 |
|---|---|---|
| 会话/队列/审批 | `internal/agent`、`approval`、`audit` | 无（只加字段） |
| 无头事件 | `Manager` 的 `Emitter` 注入 | `internal/gateway` 总线 + `internal/gateway/host` 把 `emit` 旁路进 SSE |
| 渠道 | `internal/feishu`（保持为唯一飞书连接） | `internal/gateway/channel/{telegram,web}`（web 为当前接管） |
| 记忆 | `internal/knowledge`、`LearnText` | `internal/memory`（笔记 + 检索 + 整理任务） |
| 计划 | 无 | `internal/scheduler` |
| 技能 | `internal/skill`（SKILL.md） | 安装/更新/权限 |
| 运行时 | `internal/runtime` | 无（当前三件套） |

**不动的部分**：`src/mygo.ts` 生成链路、设计 token、飞书 Hub 的既有行为。
`main.go` 只新增网关的启动/关闭与事件旁路，不改成「双模入口」；
独立 `cmd/mad-gateway` 留给 M3.5，不与桌面共用同一数据目录同时跑。

---

## 10. 已拍板的四件事（2026-11-26）

1. **形态**：网关采用「桌面内嵌，飞书保持现状」——同进程、单写者，绑定文件在桌面数据目录；
   独立常驻是后续形态，本轮不做。
2. **渠道**：网关当前只接管 `web`/本地渠道；飞书继续由 `feishu.Hub` 处理，不新增第二条长连接。
3. **审批身份**：仅 owner + 白名单可批（飞书侧已落盘 ownerOpenId / approverOpenIds）。
4. **自动批准边界**：默认 ask；写操作永远人工，只读工具才允许 auto/readonly 策略。

留给 M3.5 再定的：独立进程与桌面的数据互斥、把飞书迁进网关（需先拆 Hub）、公网暴露前的鉴权与审计。

---

## 11. 风险

| 风险 | 影响 | 对策 |
|---|---|---|
| 两个进程同时写 `agents.json`（仅 M3.5） | 会话丢失/覆盖 | 内嵌形态天然单进程；独立形态先做锁文件或桌面只读 |
| 渠道消息提示注入 | 泄露文件、执行命令 | 不可信输入、审批、白名单、审计带批准人 |
| 常驻进程权限过大 | 无人值守时被利用 | 默认 ask；写操作永远人工；只读自动 |
| 主动消息过多 | 用户关闭通知 | 静默时段、频率上限、失败降频 |
| ACP 进程泄漏 | 内存/端口占用 | 已有进程组清理；网关加定期巡检 |
| 网关端口被占用 | 页面看起来「没起来」 | 只记日志 + `listenError` 展示，桌面照常启动 |

---

## 12. 推荐执行顺序

**契约 → 前端 IA → 网关内嵌闭环 → 记忆 → 计划 → 独立常驻/第二渠道 → 沙箱**。

网关内嵌闭环已落地：桌面启动即起 HTTP/SSE，`Session/Approval/Sender/Binder` 四项真装配，
`web` 入站可派活、审批可在网关页操作；飞书链路保持原样。
记忆与调度**故意延后**：等网关验收（桌面实机 `/api/status` wired、网关页无「未接入」、飞书不重复回复）之后，
再单独开线，避免与网关改动抢 `main.go` / `useBackend` / `src/mygo.ts`。

每接一个后端能力，就替换掉一个「未接入」占位，不先写依赖假数据的交互逻辑。
