# 架构评审

保存对当前代码库的架构评审报告。**评审是提案，不是既定事实** —— 报告里的候选一旦被采纳并落地，
应把结论移进 `docs/design.md`，这里是历史存档。

| 报告 | 主题 | 状态 |
|---|---|---|
| [`knowledge-architecture-review.html`](knowledge-architecture-review.html) | 知识库存储与联动（scope / 注入路径 / CLI 与频道） | 提案，待决策 |
| [`chat-architecture-review.html`](chat-architecture-review.html) | 群聊与单聊（投递 fan-out / 共享 transcript / 双 manager 重复） | 提案，待决策 |
| [`knowledge-plan.md`](knowledge-plan.md) | 知识库修复实施计划（分步 + 验收命令） | 待执行 |
| [`collaboration-plan.md`](collaboration-plan.md) | 协作/讨论/主动派活 从 0 到 1 实现方案 | 待拍板 |

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
