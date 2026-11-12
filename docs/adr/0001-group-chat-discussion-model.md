# ADR-0001：群聊从「广播」改为「共享 transcript + 显式回合策略」

- 状态：已采纳
- 日期：2025-11-12
- 取代：`docs/design.md` §7「频道是广播触发，非讨论收敛」的「语义保持为广播」决定
- 相关：`docs/reviews/chat-architecture-review.html`、`docs/reviews/collaboration-plan.md`

## 背景

现有群聊（`internal/channel`）与单聊（`internal/agent`）是两套平行的 module：

- 群消息只落在 `channel.Messages`，从不进入任何 agent 的上下文。
- 构造给成员的 prompt 只有一句 `fmt.Sprintf("[channel:%s] %s: %s", channelId, fromName, text)`（`main.go:360`）。
- 因此 agent A 看不到 agent B 在同一频道里的发言。

原先 `docs/design.md` 记录的决定是「语义保持为广播，不做多轮收敛编排」。那是一个成立的决定：广播模式实现简单、无回声风险、无收敛判定问题。

但产品的目标已经从「多个 AI 聊天窗口并排」变为「多个 AI 在同一份上下文上协作」。广播语义下，「协作」不可能成立——参与者看不到彼此，谈不上协作。

## 决策

**群聊改为「共享 transcript + 显式回合策略」。**

1. **共享 transcript**：频道的 `Messages` 成为群上下文的唯一事实源。每个成员在每轮执行前，按预算从 transcript 裁剪后拼入 prompt。
2. **`Agent.Messages` 降级为私有轮次**：只保留该 agent 自己参与过的轮次，不再承载群上下文。
3. **回合策略显式化**（`TurnPolicy`）：
   - `PolicyDirect` —— 只有被点名的人回（单聊；群聊可显式指定）
   - `PolicyDiscuss` —— 所有成员都能看到上下文，按 @ 或显式指派决定谁回
   - `PolicyRoundRobin` —— 轮流发言，用于评审场景
4. **删除 `isRelevantToRole`**（`main.go:397`）。该函数的键是「后端/前端/测试」等短词，而 `a.Role` 的实际值是「Go 专家，关注 API 设计…」（`src/lib/presets.ts:3`），两者永不匹配，函数恒返回 `false`，导致「不 @ 就没人回」。这是缺陷不是策略，不作为可配置项保留。
5. **@ 解析只保留一份实现**，前后端共用同一语义。现有三份实现（`ChannelView.tsx:74` 插入、`ChannelView.tsx:79` 过滤、`main.go:378` 解析）合并。

## 备选方案与放弃理由

| 方案 | 放弃理由 |
|---|---|
| 维持广播，只修 `isRelevantToRole` | 修完仍然互相看不见，解决不了「协作」这个目标 |
| 广播 + 每轮把整份频道历史塞进 prompt | 无预算裁剪时 prompt 会随频道增长无限膨胀，且历史越久噪声越大 |
| 引入 Supervisor 自动决定谁发言 | 收敛判定不可测；出问题时分不清是编排选错还是 agent 执行错。先让参与者能看见彼此，再考虑是否替它决定 |

## 后果

**正面**

- agent 之间能真正互相看到 → 「协作」从描述变成能力。
- 群上下文只从 transcript 一处计算，可测、可单点修。
- 一条群消息从「channel 1 份 + N 份 agent 改写副本」降为「1 份事实 + N 份纯引用」。

**负面 / 代价**

- 引入**回声风险**：A 看到 B 的发言后可能继续回应 B，形成往复。必须由 `TurnPolicy` 与轮次上限约束。
- prompt 变大，token 成本上升（与知识库预算裁剪共享同一套机制）。
- 需要一次数据迁移：`channels.json` 的消息结构新增 `Deliveries`，`kind` 缺省为 `user`。

**必须配套的约束**（否则本决策会失控）

- 单次 `Send` 的传播轮次上限（建议 1 轮：一次用户发言最多触发一轮成员回复，回复不自动再触发）。
- `Transcript` 必须有字符/token 预算。
- 投递结果（含「跳过」）必须作为数据返回，不能只打日志。

## 不在此 ADR 范围内

- agent 主动派活（工具调用循环、`create_agent`、`ParentID` 层级）—— 见 `collaboration-plan.md` 阶段 2.3，本 ADR 只确立「共享上下文 + 回合策略」这一层。
- 子 agent 在侧栏的父子嵌套展示 —— 属于交互层。
