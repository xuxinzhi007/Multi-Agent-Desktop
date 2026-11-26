# 架构评审

保存对当前代码库的架构评审报告。**评审是提案，不是既定事实** —— 报告里的候选一旦被采纳并落地，
应把结论移进 `docs/design.md`，这里是历史存档。

> **复核纪律**：报告必须带复核日期。超过一个月未复核的按「待复核」对待，不能继续当决策依据。
> 最近一次全量复核：**2026-11-25**，结论见 [`review-recheck-2026-11-25.md`](review-recheck-2026-11-25.md)。

| 报告 | 主题 | 状态（2026-11-25 复核） |
|---|---|---|
| [`knowledge-architecture-review.html`](knowledge-architecture-review.html) | 知识库存储与联动 | **6 条中 5 条已落地**，结论已进 `design.md` §5.10；仅存档 |
| [`chat-architecture-review.html`](chat-architecture-review.html) | 群聊与单聊 | **#1/#2/#3 已落地**（DecideReplies / BuildTranscript / Delivery）；#4 一半、#5 仍成立、#6 已修 |
| [`knowledge-plan.md`](knowledge-plan.md) | 知识库修复实施计划 | 已执行 |
| [`collaboration-plan.md`](collaboration-plan.md) | 协作/讨论/主动派活 | 已落地（Route / ask_agent / delegate / Supervisor） |
| [`floating-window-review.html`](floating-window-review.html) | 悬浮窗拖动/点击/动画 | ⚫ **已废弃**：`src/mini.tsx` / `mini.html` / `API.MiniStatus` 均已移除 |
| [`code-review-gateway-m3-2026-11-26.md`](code-review-gateway-m3-2026-11-26.md) | 内嵌网关 M3 + 记忆/调度/前端 | **评审快照**；§4.1 的 #1/#2 仍未修，见 [`status-2026-11-26.md`](status-2026-11-26.md) |
| [`status-2026-11-26.md`](status-2026-11-26.md) | 当前收工状态、验证结果、已知风险 | **当前有效**，优先于 11-25 的上下文与待办 |
| [`capability-gap-and-integration-2026-11-26.md`](capability-gap-and-integration-2026-11-26.md) | 能力短板核实 + 二次开发四路线 + 上下文工程（§7 gp-context-lab） | **核实版**；替换一份口头分析（其中 3 条已过期），§4 标出未核实的外部说法；§7 为参考仓合并结论 |
| [`ux-audit-2026-11-26.md`](ux-audit-2026-11-26.md) | 用户视角体验审计（19 屏取证） | **2 个 P0**：未配置仍可发送且宣称「已就绪」、创建流程缺模型必填；§0 标出待验证边界 |
| [`article-building-an-honest-agent.md`](article-building-an-honest-agent.md) | 对外文章：做 Agent 产品难在「诚实」 | 基于本仓真实记录的技术随笔；数字均为实测，含反直觉发现（工具 schema 已占固定开销 87%） |
| [`user-voice-2026-11-26.md`](user-voice-2026-11-26.md) | 用户视角体验报告（6 次真实操作复盘 + 统计） | **当前有效**；5/6 踩坑，P0 四条：双发、读错项目、插话=排队、消息错序 |
| [`req-web-remote-2026-11-26.md`](req-web-remote-2026-11-26.md) | Web 端接真后端（需求卡） | **v1 已落地**，且改为单页方案（`/remote` 已删）；含 `/api/rpc` 与浏览器实拍验证 |
| [`req-write-safety-2026-11-26.md`](req-write-safety-2026-11-26.md) | 需求：多写者下的写安全（版本校验 / 写意图广播 / 默认 worktree + 合回） | **P0 已实现**（版本校验 / 写意图事件+提示条 / worktree_merge / 三态隔离），P1/P2 未做；动机是四次并行踩踏事故 |
| [`req-code-capability-2026-11-26.md`](req-code-capability-2026-11-26.md) | 需求：编码能力提升（verify 含测试/lint、失败闭环、repo_map、变更集自查） | **P0 + P1-1/P1-3 已实现**（verify 带相关测试/lint、3 次未过即停、repo_map、changeset）；P1-2 部分、P2 未做 |
| [`req-context-budget-2026-11-26.md`](req-context-budget-2026-11-26.md) | 需求：上下文预算与工具输出防护（参考 gp / gp-context-lab） | **提案，待开发**；P0 3d＝折叠+restore（1d）、MCP stdio 客户端（2d）；PPR/ACT-R/MMR 与 Ledger 明确列 P1 不排期 |
| [`todo-2026-11-26.md`](todo-2026-11-26.md) | 今日清单（逐条核实到行号，P0→P3 排序） | **当前有效**；P0 为网关并发两处 + verify 闭环，含「不做」及理由 |
| [`final-acceptance-2026-11-25.md`](final-acceptance-2026-11-25.md) | 全量验收命令与清单 | 带有 11-26 收尾更新，历史清单保留 |
| [`review-recheck-2026-11-25.md`](review-recheck-2026-11-25.md) | 三份评审对当前代码的逐条复核 | 当前有效 |

## 直接读纯文本

HTML 报告依赖 CDN（Tailwind + Mermaid），离线或命令行下不好读。摘要：

### 知识库现状

`internal/knowledge` 本体干净（fileStore / memoryStore 两个真实 adapter，JSONL 追加 + rename 重写，
7 个测试覆盖并发与损坏行）。**摩擦不在这个包内，在它对外的 interface 和周围五条注入路径。**

| 注入路径 | 内容 | 可撤销 |
|---|---|---|
| `Manager.Create` | top-8 全局笔记 → 首条 system | 否 |
| `AttachNoteToAgent` | 单条笔记 → 追加 system | 否 |
| Channel 广播 | 无 | — |
| CLI agent（claude/codex） | 无（`runner.Run(ctx, text)` 只传最后一句） | — |
| 删除笔记后 | 仍在 `Agent.Messages` 里 | 否 |

### 候选（按依赖顺序）

1. **`knowledge` 是 shallow 的** — `NotesForSystemPrompt()` 只是 `Query` + `strings.Join`，过不了
   deletion test；选笔记策略散在 `main.go:932` 与 `internal/agent/manager.go:104` 两处。`Strong`
2. **attach 泄漏进 history** — `main.go:511` 在 service 层做三件业务事（扫 store / 渲染 / 追加
   system），绕过 `KnowledgeProvider` 这条唯一 seam，不记「哪个 agent 引了哪条」。`Strong`
3. **`Scope` 存了但没人实现** — UI 可选「仅 Agent X」，但 `knowledgeProvider` 只查 `Scope==""`，
   那条笔记永不生效。`Strong`
4. **固定 top-8 无排序** — 判据是写入序，不是相关性；200 条时昨天写的关键约定被丢弃。`Worth exploring`
5. **CLI agent / Channel 拿不到知识库** — 唯一改变外部行为的一项，建议独立提交。`Worth exploring`
6. **引用不可见、无 Detach** — #2 的 UI 后置项。`Speculative`

### 建议顺序

**先 #2 再 #1**。#2 是唯一已产生错误行为的地方，先把 `Agent.Knowledge []string`（走
`Save`/`Load`/`Snapshot`/`viewMap` 四处白名单）这个数据形状定下来，#1 的 `View(agent)` 才有确定入参。
之后 #3 的 scope 分层与 #4 的排序都退化为 `View` 内部的事，#6 免费。

### 待决策

- **#1 的 seam 位置**：`internal/agent` 反向 import `internal/knowledge`（更 deep，但耦合）；
  或 `View` 只做纯函数、渲染留给 Sidecar（更松，但浅）。
- **#4 不做向量检索**：笔记规模是人类手写的几十到几百条，标签 + 子串 + 时间已足够；引入 embedding
  会把零依赖的纯内存 module 变成需要外部服务的 adapter，在笔记上千条之前是负 depth。

## 重新生成

评审报告写在 OS 临时目录、用 `open` 打开是原流程；如果报告值得留在仓库里，再拷到这里并补一行索引。
