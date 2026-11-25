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
| **网关**（新增无头模式） | 你不在电脑前 | 飞书 / Telegram / 网页 | 收消息、派任务、查进度、主动提醒 |

两者共用同一个内核：`internal/agent`（会话、Queue/Worker）、`internal/runtime`（native / ACP）、
`internal/tool`、`internal/approval`、`internal/audit`、`internal/skills`。

**明确的非目标**：

- 不做第二个 OpenClaw。渠道只要 2–3 个真正在用的，不做全平台适配。
- 不把桌面 UI 塞进手机。网关用文本 + 卡片交互，复杂改动回桌面看。
- 不为「插件生态」重构内核。技能继续用 `SKILL.md`，按需加载。

---

## 2. 总体架构

```
                    ┌──────────────────────────────┐
   飞书 / Telegram ─▶│ internal/gateway             │
   网页聊天 (SSE)  ◀─│  Router / Allowlist / 去重    │
                    │  SessionResolver / Outbox     │
                    └──────────────┬───────────────┘
                                   │ Enqueue / Emitter
                    ┌──────────────▼───────────────┐
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
  桌面注入 MyGo 事件，网关注入「渠道消息 + SSE + 日志」。这就是无头复用的基础。
- `internal/runtime` 已经是统一事件层，native 与 ACP 对上层同形。
- 审批、审计、工作目录校验都在工具层，渠道层不重复实现。

---

## 3. 端到端流程

### 3.1 入站消息（被动）

```
渠道回调 / 长轮询
  → gateway.Router
       ├─ 渠道白名单（联系人 / 群 / 租户）
       ├─ 消息去重（外部 message id）
       ├─ 频率限制（每会话每分钟 N 条）
       └─ 解析 @、引用、附件
  → SessionResolver：外部 chat id → 本地 agent 会话
  → Manager.Enqueue（已有 Queue/Worker，32 容量）
  → Worker.Prompt
       ├─ native loop 或 ACP runtime
       ├─ 工具调用 → approval.Center（需要时阻塞）
       └─ 事件 → Emitter
  → Emitter
       ├─ 渠道适配器：发「正在处理」→ 增量编辑/分段发送 → 最终卡片
       ├─ SSE：网页端实时看
       └─ audit：副作用留痕
```

规则：

- **一个外部会话对应一个本地 Agent**，不是全局一个。群聊按「群 + 机器人」建会话。
- 渠道消息是**不可信输入**。进入模型前只做归一化，不做「把消息当系统指令」的拼接。
- 网关忙时：先回执「收到，排队中」，再异步跑；不能假装已读。

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
| `SessionBinding` | agentId、channel、externalChatId、policy(mentionOnly/allowAll)、createdAt | `gateway.json` |
| `MemoryNote` | id、kind(daily/fact/decision)、text、source、confidence、createdAt、expiresAt | `memory/*.jsonl` |
| `Schedule` | id、cron/interval、agentId、prompt、channel、quietHours、lastRun、failCount | `schedules.json` |
| `Approval` | id、agentId、tool、input、options、source(desktop/channel)、requester | 内存 + `audit.jsonl` |
| `RuntimeEvent` | v、seq、sessionId、turnId、kind、payload | 事件流，不落盘（检查点在 loop/） |

**单写者原则**：`agents.json` / `gateway.json` / `schedules.json` 只允许一个进程写。
桌面与网关同时启动时必须用锁文件互斥，或桌面改为「只读客户端」。这条不先定，后面一定丢数据。

---

## 5. 进程与部署

| 模式 | 进程 | 数据 | 适用 |
|---|---|---|---|
| 本地 | 桌面 App（MyGo） | 本机目录 | 坐在电脑前 |
| 网关 | `mad-gateway`（无 GUI） | 同一目录 / 服务器目录 | N100、云主机、Tailscale |
| 远程客户端 | 桌面 App 连网关 HTTP/SSE | 网关持有 | 想用桌面 UI 看远程任务 |

推荐拓扑（贴合现有硬件）：

```
Windows/Mac 桌面 ──Tailscale──> N100 (Debian, systemd)
                                   └─ mad-gateway
                                       ├─ 飞书长连接
                                       ├─ Telegram 长轮询
                                       └─ ~/.multi-agent-desktop
```

- 网关用 `systemd` 常驻，崩溃自动重启；日志走 journald + 文件双写。
- 密钥不进 `config.json` 明文：网关优先读环境变量 / systemd `EnvironmentFile`，后续迁钥匙串。
- 网关不需要公网 IP：飞书长连接、Telegram 长轮询都出站。

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
| 网关 | 「未配置渠道」+ 配置入口 | 连接中 | 渠道断开原因 | 无权限时只读 |
| 记忆 | 「还没有记忆」+ 导入入口 | 骨架 | 读取失败 | 删除需确认 |
| 计划 | 「还没有计划」+ 新建 | 骨架 | 调度器未启动 | 静默时段提示 |

原则：**不允许出现「看起来能点但没反应」的按钮**。没有后端就明确写「未接入」并禁用。

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
| 桌面前端 ↔ Go | MyGo typed IPC | 保持现状，不暴露到公网 |
| 桌面/网页 ↔ 网关 | HTTP + SSE | `/api/status`、`/api/agents`、`/api/channels`、`/api/approvals`、`/api/events` |
| 网关 ↔ 渠道 | 各渠道 SDK / Bot API | 适配器接口统一：Start / Stop / Send / Inbound |
| Agent ↔ 外部运行时 | ACP over stdio | 已有，复用 |
| Agent ↔ 工具 | MCP（可选） | 以后接外部工具，不现在做 |

**不要**把 MyGo IPC 包一层 HTTP 当公网 API：它没有鉴权、没有速率限制、方法粒度是内部的。
网关 API 只暴露「会话、消息、审批、状态」四类。

---

## 8. 里程碑

按「前端优先、契约先行」拆，每一步都能独立验收：

| 阶段 | 内容 | 验收 |
|---|---|---|
| **M0 契约**（1–2 天） | 写定 Agent 运行时字段、网关 API、事件映射、数据归属 | 文档评审通过；Go 类型先落地，不接 UI |
| **M1 前端 IA**（3–5 天） | 顶部状态条、导航分组、网关/记忆/计划三个页面的空态与只读态 | 无死按钮；`npm run build:web` 通过；e2e 覆盖空态 |
| **M2 运行时可见**（依赖当前三件套） | 会话行显示 native/ACP、连接状态、回合状态 | ACP 会话可创建，审批复用现有弹窗 |
| **M3 网关最小闭环**（1 周） | `mad-gateway` + 飞书渠道 + 会话绑定 + 出站回执 | 飞书私聊能派活；审批卡能点；重启不丢会话 |
| **M4 第二渠道**（3–5 天） | Telegram 长轮询 + 白名单 + 去重 | 非飞书渠道跑通同一套隔离与审批 |
| **M5 记忆**（1 周） | 每日笔记、`memory_search`、定时整理 | 跨会话能召回；有来源与删除 |
| **M6 计划**（1 周） | cron / 提醒 / 早报 / 静默时段 | 任务可停用；失败降频；不刷屏 |
| **M7 沙箱与分发**（按需） | Linux 沙箱、签名、自动更新 | 公网暴露前完成渠道白名单与注入测试 |

---

## 9. 与现有代码的映射

| 新能力 | 复用现有 | 新增 |
|---|---|---|
| 会话/队列/审批 | `internal/agent`、`approval`、`audit` | 无（只加字段） |
| 无头事件 | `Manager` 的 `Emitter` 注入 | `internal/gateway/emit`（渠道 + SSE） |
| 渠道 | `internal/feishu` | `internal/gateway/channel/{telegram,web}` |
| 记忆 | `internal/knowledge`、`LearnText` | `internal/memory`（笔记 + 检索 + 整理任务） |
| 计划 | 无 | `internal/scheduler` |
| 技能 | `internal/skill`（SKILL.md） | 安装/更新/权限 |
| 运行时 | `internal/runtime` | 无（当前三件套） |

**不动的部分**：`main.go` 的 MyGo 绑定、`src/mygo.ts` 生成链路、设计 token。
网关是新的 `cmd/mad-gateway`，不把桌面 `main.go` 改造成双模入口，避免一次动到在用的窗口。

---

## 10. 必须先拍板的四件事

1. **数据归属**：网关单写者 + 桌面只读客户端，还是两边互斥启动？（决定同步与锁）
2. **渠道优先级**：飞书 + Telegram，还是加企业微信？个人微信不建议 hook。
3. **审批身份**：仅 owner，还是 owner + 白名单？群场景是否允许「@ 机器人的人」？
4. **自动批准边界**：只读工具自动、写操作永远人工，是否接受？

这四条不定，M3 之后会反复返工。

---

## 11. 风险

| 风险 | 影响 | 对策 |
|---|---|---|
| 两个进程同时写 `agents.json` | 会话丢失/覆盖 | 单写者 + 锁文件；桌面远程模式 |
| 渠道消息提示注入 | 泄露文件、执行命令 | 不可信输入、审批、白名单、审计带批准人 |
| 常驻进程权限过大 | 无人值守时被利用 | 默认 ask；写操作永远人工；只读自动 |
| 主动消息过多 | 用户关闭通知 | 静默时段、频率上限、失败降频 |
| ACP 进程泄漏 | 内存/端口占用 | 已有进程组清理；网关加定期巡检 |
| 前端先行的占位页 | 上线后成死按钮 | 统一「未接入」态；e2e 检查禁用 |

---

## 12. 推荐执行顺序

**契约 → 前端 IA → 网关最小闭环 → 第二渠道 → 记忆 → 计划 → 沙箱**。

前端可以先动，但只动「信息架构与状态」，不要先写依赖后端数据的交互逻辑；
每接一个后端能力，替换掉一个「未接入」占位，这样不会返工。

当前三件套（native/ACP 运行时 + 前端选择 + 新工具）仍在进行中，
新线应在它合并、`npm run build:web` 绿了之后再改共享文件（`App.tsx`、`useBackend`、`src/mygo.ts`）。
