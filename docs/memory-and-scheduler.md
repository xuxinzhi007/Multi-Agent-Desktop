# 记忆与计划调度（桌面内嵌）

本文说明 `internal/memory`（跨会话记忆）与 `internal/scheduler`（计划任务）在桌面主进程中的真实形态。
两者都随桌面应用启动、随应用退出关闭，不单独起进程。

## 1. 与知识库的区别

| | 记忆（Memory） | 知识库（Knowledge） |
|---|---|---|
| 目的 | 自动沉淀跨会话的对话与事实，供以后召回 | 用户手工整理、可挂载给某个会话的资料 |
| 写入 | 主进程自动写入 `agent.message` 的 user/assistant 文本 | 用户通过知识库面板/工具显式新增 |
| 存储 | 单文件 SQLite `memory.db`（FTS5 trigram） | `knowledge.jsonl` |
| 读取 | `memory_search` 工具 + 记忆页 | 会话挂载的知识笔记 + 检查器 |
| 可见性 | 所有会话共享，按 `agentId` 可过滤 | 仅挂载了它的会话 |

记忆页保留「打开知识库」入口，但**不把知识库内容混进记忆列表**。

## 2. 自动写入边界

- 只处理 `agent.message`，且 `role` 为 `user` / `assistant`。
- 跳过：流式 delta、工具调用/结果、空内容、非文本块、`reasoning`。
- 正文超过 8000 字符截断（保留头尾并标记），避免单条撑爆库。
- 去重：`sha1(agentId|role|content|timestamp)` 作为 `dedupe_key`；同一条消息被重复 emit 只落一行。
- 限流：每个 Agent 每分钟最多 60 条，超出丢弃并计数；写入异步入队，不阻塞事件循环。
- 飞书/频道消息也会以 `agent.message` 形式出现，因此同样进入记忆；网关与 Hub 的发送行为不受影响。
- 不自动写事实三元组、不做摘要/衰减；`facts` 表保留给后续能力。

## 3. 检索

- 工具 `memory_search`：参数 `{query, agentId?, limit?}`，查 events + notes + facts，合并去重后按时间倒序返回 `kind/id/source/agentId/title/snippet/createdAt`；limit 默认 8、最大 30。
- 空结果返回明确文案「没有找到相关记忆。」，不伪造。
- 中文走 FTS5 trigram，短词用 LIKE 兜底。
- 桌面 IPC：`MemoryStatus / MemorySearch / MemoryRecent / MemoryNotes / MemoryUpsertNote / MemoryDeleteNote`。

## 4. 计划调度

- 任务字段：`id/type(interval|cron)/intervalSec/cron/agentId/prompt/channel/enabled` + 运行态 `createdAt/lastRunAt/nextRunAt/failures/lastError`。
- 持久化：桌面数据目录下 `schedules.json`，同目录临时文件 + rename 原子替换；启动恢复任务与运行态。
- 心跳：主进程 `Start(ctx, 30s)`；退出 `Stop()` 并等待 goroutine 结束，不泄漏。
- interval 首次到期 = 创建时间 + interval；cron 用 5 字段 cron-lite（`*`、`*/n`、单值；不支持列表/区间/命名）。
- 静默时段：默认 23:00–08:00 不触发（可配置）；「立即运行」显式绕过静默与限频。
- 限频：保留每任务与全局令牌桶；失败按 `30s * 2^(failures-1)` 指数退避，上限 30 分钟；成功后清零。
- 执行：Runner 调 `agent.Manager.Prompt(ctx, PromptRequest{AgentID, Text})`，只入队，不伪造回复；回复照常走 `agent.message`，也会写入记忆。
- **审批**：不自动批准任何工具；仍走 `approval.Center`。无人值守/审批超时按 fail-closed 处理（超时拒绝，只读模式拒绝写操作，`auto` 仅按会话权限档放行）。计划任务不会绕过审批。
- 桌面 IPC：`PlanStatus / PlanList / PlanCreate / PlanUpdate / PlanPause / PlanDelete / PlanRunNow`；创建时校验类型、间隔、cron、agentId、prompt，错误为中文可读文案。

## 5. 启动与关闭顺序

```
mustDataDir()
  ├ memory.Open(<dataDir>/memory.db)      → mgr.SetMemory / Bridge
  ├ scheduler.NewService(<dataDir>/schedules.json, Runner=mgr.Prompt)
  ├ gateway host（既有）
  └ feishu.Hub（既有）
窗口 / MyGo IPC

退出：停调度心跳 → 关记忆 Bridge（排空）→ 关记忆库 → 关网关 → Manager.Shutdown
```

记忆库或调度文件打开失败只记日志 + 页面显示「未启用/错误」，不阻断桌面启动。

## 6. 当前边界

- 不做记忆摘要、遗忘曲线、跨设备同步；`facts` 暂不自动生成。
- 计划不支持一次性任务、时区选择、列表/区间 cron；静默时段与限频暂未在 UI 暴露。
- 计划任务默认只发 prompt，不额外注入“这是定时任务”的系统指令；需要时写在 prompt 里。
- 记忆与知识库都不进网关 HTTP API，只走 MyGo IPC。
