# 架构评审复核：三份旧报告对当前代码还成立吗

> 复核日期：2026-11-25
> 对象：`docs/reviews/` 里三份「架构评审 · X (multi-agent-desktop)」
> 方法：**逐条拿当前代码核对**，不看报告自述状态。证据给到文件与函数名。
> 结论口径：✅ 已落地｜🟡 部分｜❌ 仍成立｜⚫ 对象已不存在

---

## 0. 一句话结论

**三份报告合计 17 条候选，9 条已落地、1 条部分、4 条仍成立、3 条对象已消失。**

`docs/reviews/README.md` 里三行状态全写着「提案，待决策」——**这个索引已经过期了**。
最严重的三个 bug（群聊恒 false、投递失败被吞、知识库 scope 永不生效）都已经修完并有测试。
继续把这份索引当决策依据，会重复讨论已经做完的事。

---

## 1. 群聊与单聊（`chat-architecture-review.html`，2025-11-12）

| # | 候选 | 原判级 | 现状 | 证据 |
|---|---|---|---|---|
| 1 | fan-out 裸在 Sidecar，`isRelevantToRole` 恒 false | Strong | ✅ **已落地** | `internal/channel/channel.go:619 DecideReplies` 是「谁该回」的唯一实现；`main.go:817` 只调它 |
| 2 | agent 看不见彼此，群上下文缺失 | Strong | ✅ **已落地** | `channel.go:495 BuildTranscript` + `:517 Render`；`main.go:799` 按 2000 字符预算裁剪，`:854 Context: transcript.Render(agentId)` |
| 3 | 投递失败被 `.catch` 吞掉 | Strong | ✅ **已落地** | `Delivery{Status,Reason}` + `SetDeliveries` 回填 + 重发同 ID 事件；前端 `ChannelView.tsx:530 DeliveryRow` |
| 4 | channel/agent 两个 manager 逐字重复 | Worth exploring | 🟡 **一半** | `channel.Delete` 已补（`channel.go:291`）；`Collection[T]` 未抽，`goSave`/`writeFileAtomic` 仍各一份 |
| 5 | 两个输入框、三份 @ 解析、切换丢草稿 | Worth exploring | ❌ **仍成立** | `ChannelView.tsx` 与 `AgentView.tsx` 各一个 textarea，无 composer module |
| 6 | 删除 agent 后引用悬空 | Speculative | 🟡 **一半** | `RemoveMember` 有；前端 `agentDeleted` 只清了 `streaming/channelStreaming/outbox` |

### 值得单独说的两件事

**① 实现比提案更完整。** 提案的 `Dispatch(msg, members) []Delivery` 只到「投递计划」；
落地版本把 `Decision{Reply, Reason}` 和 `Delivery{Status, Reason}` 分成两个类型——
判定与投递结果解耦，`skipped` 也能带原因。这比提案的形状好，**不要按提案回退**。

**② 有一处误导性残留必须清。** `main.go:879` 留着一行孤立注释：

```go
// isRelevantToRole 判断消息是否与角色相关。
// CreateSupervisor 新建一个自动拆任务的会话，并把目标立刻派进去。
func (s *API) CreateSupervisor(...)
```

函数体早已删除，注释却挂在了另一个函数头上。读代码的人会以为角色相关性判断还在 main 层。
**这是本次复核唯一发现的「报告没提、但真实存在」的问题。**

### #6 的确切缺口（可直接修）

`useBackend.ts:285` 的 `agentDeleted` 分支没清这些 per-agent map：

```
errors · shellByAgent · reasoning · pace · tokenUsage · stops
```

**后果分两种，不要混为一谈**（我第一版写成「界面串味」，核实后修正）：

- `errors / reasoning / pace / tokenUsage / stops / shellByAgent`：只按**当前选中会话的 id** 读取
  （`App.tsx` 先 `s.agents.find(...)` 再取值），删掉的 id 不会再被读到。
  所以这几个的实际后果是**内存里留着永不回收的条目**，不是可见的串味。
- `tools`：**这一个真的可见**。`App.tsx:241` 传给检查器的是 `s.tools`（全部会话），
  `Inspector.ToolList` 逐行渲染且不按会话过滤 —— 删掉会话后它的工具行永远留在检查器里，还计入页签条数。

已修，并补了 `e2e/agent-delete-cleanup.spec.ts`。**该测试经过反向验证**：
回退 `tools` 那一行清理后测试失败，恢复后通过——不是假通过的测试。

---

## 2. 知识库（`knowledge-architecture-review.html`，2025-11-12）

| # | 候选 | 现状 | 证据 |
|---|---|---|---|
| 1 | `knowledge` 是 shallow，选笔记策略散两处 | ✅ **已落地** | `KnowledgeProvider` 是唯一 seam（`manager.go:107`），`View(agentID, role, ids, recentText, budget)` 收口 |
| 2 | attach 泄漏进 history，绕过 seam | ✅ **已落地** | `Agent.Knowledge []string`（`manager.go:50`），四处白名单齐（`:295 Save`、`:437 Load`、Snapshot、viewMap） |
| 3 | `Scope` 存了但没人实现 | ✅ **已落地** | `knowledge.go:358` 按 `ScopeAgent+agentID` 命中，`:366` 再收全局 |
| 4 | 固定 top-8 无排序 | ✅ **已消失** | 创建会话时**不再注入** top-8；改为挂上的 id 当轮注入（`design.md` §5.10） |
| 5 | CLI agent / Channel 拿不到知识库 | 🟡 **形态变了** | CLI backend 已被进程内 loop 取代；频道走 `contextText`，知识按会话挂载 |
| 6 | 引用不可见、无 Detach | ✅ **已落地** | `main.go:2658 DetachNoteFromAgent`；前端 KnowledgePanel 能挂能摘 |

**当时那条「先 #2 再 #1」的建议是对的，而且被照做了**——先定 `Agent.Knowledge` 的数据形状，
`View` 才有确定入参。这条经验值得保留在 ADR 里，而不是留在过期的评审索引里。

**当时那条「不做向量检索」的判断也仍然成立**：笔记规模是几十到几百条，
`internal/knowledge` 至今零外部依赖。不要动它。

---

## 3. 悬浮窗（`floating-window-review.html`，2025-11-12）

⚫ **评审对象已不存在。**

```
src/mini.tsx   → 不存在
mini.html      → 不存在
API.MiniStatus → 不存在
```

整份报告（拖动/点击冲突、`--app-region: drag` 吃掉点击、8 个色点静默丢弃、
`done` 计数误用 `idle`）讨论的是一个**已经删掉的形态**。

🔶 而且 `research-product-forms.md` 后来给出的一手证据正好否证了这个形态：
6 个成熟产品（Claude Code / Cursor / Devin / Warp / Raycast / Linear）
**没有一个**用常驻小浮窗承载并发状态。删掉它是对的。

**建议：把这份报告标为「已废弃 · 对象已移除」，而不是继续挂在「提案，待决策」。**

---

## 4. 现在真正该做的（按性价比）

| 优先级 | 事项 | 理由 | 成本 |
|---|---|---|---|
| ~~P0~~ ✅ 已修 | 清掉 `main.go:879` 的悬空注释 | 唯一会误导读码人的残留 | 1 行 |
| ~~P0~~ ✅ 已修 | `agentDeleted` 补清 6 个 map + `tools` 按 id 过滤 | `tools` 那条用户可见（检查器残留）；其余是内存不回收 | 小 |
| P1 | 更新 `docs/reviews/README.md` 的状态列 | 现在三行全写「待决策」，实际 9 条已落地 | 小 |
| P2 | composer module（评审 #5） | 草稿跨切换保留 + @ 语义收成一份；**但要先确认 @ 解析现在是否真的还有三份** | 中 |
| — | `Collection[T]`（评审 #4） | 纯结构收益、不改行为。报告自己也写了「得先被真实需求压出来，不能凭空抽」——**至今没有这个需求，不做** | — |
| — | 悬浮窗全部候选 | 对象已删除 | — |

---

## 5. 对这套评审机制本身的评估

**做得好的地方：**

- 判级（Strong / Worth exploring / Speculative）+ 「为什么不上一级」的理由，是有效的决策工具。
  #4 那条「泛型抽象一旦过度会变成新的浅 module，得先被真实需求压出来」——**今天仍然正确，而且救了一次过度设计**。
- 明确写「评审是提案，不是既定事实」，并要求落地后把结论移进 `design.md`。这条纪律是对的。

**失效的地方：**

- **纪律没有被执行**：9 条已落地，结论没有移进 `design.md`，索引也没更新。
  于是报告从「决策工具」退化成「历史噪音」——读它的人无法判断哪条还有效。
- 🔶 建议加一条硬约束：**评审报告必须带「复核日期」字段**；
  超过一个月未复核的，索引里自动标为「待复核」，不能继续当决策依据。
  这次复核花了约 20 次代码检索——如果每条候选当初就带上「验证命令」，复核成本能降一个量级。

---

## 附：本次复核用到的检索

```bash
grep -rn "isRelevantToRole\|DecideReplies" --include=*.go .
grep -rn "Deliveries\|Delivery" --include=*.go .
grep -n "BuildTranscript\|Render" internal/channel/channel.go
grep -n "Knowledge \[\]string\|ScopeAgent" internal/agent/manager.go internal/knowledge/knowledge.go
grep -n "agentDeleted" -A 14 src/lib/useBackend.ts
ls src/mini.tsx mini.html
```