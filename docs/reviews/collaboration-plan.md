# 协作 · 讨论 · 主动派活：从 0 到 1 的实现方案

> 目标：把现在「多个 AI 聊天窗口并排」变成「多个 AI 在同一份上下文上协作」。
> 前置评审：`chat-architecture-review.html`（问题取证）、`knowledge-architecture-review.html`。
> 本文只写要建什么、按什么顺序、怎样算可用。

> **决策状态（2025-11-12 已定）**
> - P1 群聊语义 → **共享 transcript + 协作**，不是纯广播。记于 [`ADR-0001`](../adr/0001-group-chat-discussion-model.md)。
> - P2 `create_agent` → **可指定 backend**，仅限用户已配置的 provider。
> - P3 子 agent 位置 → **侧栏嵌套在父 agent 下方**。
> - P4 → ADR 已记。

---

## 0. 三个问题的真实结论

### 结论一：现在**根本没有协作**

证据（全部已证实）：

| 事实 | 位置 |
|---|---|
| 群聊 prompt 只有一句 `[channel:X] 发言人: 正文` | `main.go:360` |
| 频道 `Messages` 从不进入任何 agent 的 `Messages` | `channel.go` / `manager.go` 无交叉引用 |
| agent 在群里看不到别的 agent 说了什么 | 推论自上一行 |
| `Route()` 存在，但**只能由用户右键菜单触发** | `src/App.tsx:113` → `agent.route` |
| `llm.Message` 只有 `Role/Content`，无 `tool_calls` | `internal/llm/llm.go:9` |
| LLM 客户端 `Stream(msgs)` 返回纯文本 | `internal/llm/llm.go:28` |

**所以：agent 既看不见彼此，也没有任何主动动作的通道。** 所谓「群聊」是 N 个独立单聊共用一个消息列表，所以「协作」是描述，不是能力。

### 结论二：agent **不能**主动派活，而且差的不是一点

要让 agent 主动派活，需要四层，现在**一层都没有**：

| 层 | 需要什么 | 现状 |
|---|---|---|
| 1 | LLM 能返回结构化工具调用 | ❌ `Message` 无 `tool_calls` |
| 2 | 有一个工具注册表 | ❌ 无 `tools` 包 |
| 3 | 执行循环：模型 → 工具 → 结果 → 再问模型 | ❌ `execute` 是单轮 |
| 4 | 子 agent 的生命周期（创建/等待/回收） | ❌ 只有用户手动 `Create` |

`cli` backend 的 `tool_use` 事件只是**把 CLI 自己跑的工具体现在 UI 上**（`manager.go:641`），不是我们能调度的。

### 结论三：单聊/群聊的问题是「一个 module 被复制了两份」

| 问题 | 证据 | 后果 |
|---|---|---|
| `isRelevantToRole` 恒 false | `main.go:397` 键是「后端」，`a.Role` 值是「Go 专家…」 | 不 @ 没人回 |
| @ 解析三份三义 | `ChannelView.tsx:74/79` + `main.go:378` | 名字带空格即断 |
| 两个 manager 逐字重复 | 落盘/串行/原子写各一份 | 新特性改两遍 |
| 投递失败被吞 | `main.go:315` errs + `.catch→null` | 消息显示成功但没人收到 |
| 切换丢草稿 | `App.tsx` remount | 输入白费 |
| 缺 `Delete` | 前端有按钮，后端无方法 | 按钮指向空 |

---

## 1. 目标形状：一个「会话」抽象，两种投递

根因是 `channel` 和 `agent` 是两套平行结构。**统一到「Conversation」**，单聊和群聊只是「参与者数量 + 投递策略」不同。

```
Conversation（唯一事实源）
├── ID / Name / Kind(dm | channel)
├── Members[]       参与者（agent 引用）
├── Messages[]      全部消息（带 Kind / From / Deliveries）
└── TurnPolicy      投递策略 ← 群里就是这里不同
```

**单聊** = 2 个参与者 + `PolicyDirect`（只有对方回）
**群聊** = N 个参与者 + `PolicyDiscuss`（按规则选谁回、能看到上下文）

这样单聊/群聊不再是两套代码，只有一个 module 和两种 policy。**这是删除测试的答案**：删掉「channel 和 agent 各一套」，复杂度会集中到 Conversation 一处，而不是散掉。

### Conversation 的 interface（定形）

```
Send(convID, msg)          → Delivery[]     发消息，返回每人投递结果
Transcript(convID, budget) → []Message      取上下文（按预算裁剪）
Append(convID, msg)        → Message        落一条（agent 回复用）
```

三个方法。实现里藏：提及解析、该谁回、上下文裁剪、投递重试、成员失配兜底。

---

## 2. 实现路径（五个阶段，每阶段可独立验收）

顺序有依赖：**2.1 是地基，2.2 才能谈论讨论，2.3 才谈得上派活。**

### 阶段 2.1 · Conversation 统一（地基）

**为什么第一**：后面两阶段都建立在「消息只存一份、上下文能取到」上。

**建什么**
- 新包 `internal/conversation`，取代 `internal/channel`。
- `Message` 统一结构：
  ```
  Message{ ID, ConvID, FromID, FromName, Kind(user|agent|system), Text,
           At, Deliveries []Delivery }
  Delivery{ AgentID, Status(queued|ran|failed|skipped), Reason }
  ```
- `Members` 保留，但**只用于投递决策**，不再用于渲染（渲染用 `FromName` 快照）。
- `Conversation` 增加 `Kind(dm|channel)` 与 `Policy`。单聊固定 `PolicyDirect`，群聊建时可选定 `PolicyDiscuss`（默认）。
- 消息结构带 `Kind` 字段区分 `user` / `agent` / `system`，因为回合策略需要知道「这条是谁说的」才能决定下一轮谁回。
- 单聊收敛为 `Kind=dm` 的 Conversation，`agent.Messages` 降级为**该 agent 的私有轮次**（不含群上下文）。
- 补 `Delete`。
- 抽 `Collection[T]`：有序集合 + 原子落盘，channel/agent 共用一份实现。

**怎样算可用**
1. `go test ./internal/conversation -run TestSend|TestTranscript|TestDelete` 通过
2. 建一个群、发一条消息，`channels.json` 里这条只存 1 份（不再 N 份改写）
3. 删除 agent 后，历史消息仍显示原 `FromName`，无失色
4. 前端删除频道按钮真的能删

**迁移安全**：`channels.json` 用一次性 transformer 读入新版结构；旧字段 `fromId/fromName/text` 直接映射，`kind` 缺省为 `user`。**不丢数据。**

---

### 阶段 2.2 · 讨论：共享 transcript + 显式策略

**为什么在 2.1 后**：需要 `Transcript()` 这个能力存在。

**建什么**
- `Transcript(convID, budget)` 按 token/字符预算裁剪，返回最近的对话。
- 群上下文**每轮现算**，不写进 `Agent.Messages`。这和数据形状：
  - `Agent.Messages` = 我自己说过/看到的私有轮次
  - 群上下文 = 每轮从 Conversation 现取
- **`TurnPolicy` 显式化**，删除 `isRelevantToRole`：
  ```
  PolicyDirect      只有被点名的人回（单聊、群聊默认）
  PolicyDiscuss     所有成员都看到上下文，按 @ 或显式指派决定谁回
  PolicyRoundRobin  轮流发言（可选，用于评审场景）
  ```
- `@` 解析**只有一份**，前后端共用同一份语义（后端导出、前端复刻同样的规则，或改为后端提供 `ParseMentions`）。

**语义已定（ADR-0001）：共享 transcript + 显式回合策略**

不是纯广播，也不是自由讨论，而是**互相可见 + 受控回合**：

```
用户发言
  └→ 一轮成员回复（每个被选中的成员都能看到 transcript）
       └→ 回复写入 transcript
            └→ 停止（不自动再触发下一轮）
```

关键约束：**一次用户发言最多传播一轮**。A 看到 B 的发言是在**同一轮内**（transcript 已含 B 之前的发言），
但 A 的回复不会再自动触发 B。这既保证「互相可见」，又避免往复风暴。

要实现「多轮讨论」，由用户再发一句（或 agent 在 2.3 用 `send_message` 显式发起），而不是自动收敛。

**`TurnPolicy` 三种的具体语义**

| Policy | 谁能回 | transcript 进谁的 prompt | 用例 |
|---|---|---|---|
| `PolicyDirect` | 只有被点名者 | 只有被点名者 | 单聊；群里 @ 一人 |
| `PolicyDiscuss` | 被 @ 或显式指派者 | **所有成员** | 群里 @ 多人 / @all |
| `PolicyRoundRobin` | 按成员顺序轮流 | 所有成员 | 评审、头脑风暴 |

单聊固定 `PolicyDirect`。群聊建频道时可选，默认 `PolicyDiscuss`。

**怎样算可用**
1. 群里 A 说「用 REST」，B 被 @ 后**能在 prompt 里看到 A 的那句话**（测试断言 prompt 内容）
2. 群里没人被 @ 时，行为符合 `TurnPolicy` 且**可预测**
3. `@名字带空格` 能正确解析
4. 投递失败时，消息上显示 `Deliveries[i].Status=failed`，UI 可见

---

### 阶段 2.3 · 主动派活：工具调用循环

**这是最大的一块，也是唯一真正「从 0 到 1」的部分。**

**建什么（四层，缺一不可）**

**(1) 让 LLM 能返回工具调用**

`llm.Message` 增加 `ToolCalls []ToolCall`，`Chunk` 增加工具增量。OpenAI 兼容格式：

```go
type ToolCall struct {
    ID       string
    Name     string
    Args     json.RawMessage
}
type Message struct {
    Role       string
    Content    string
    ToolCalls  []ToolCall   // assistant 发起的
    ToolCallID string       // tool 结果的回执
}
```

`Stream` 的签名要能接收 `tools []ToolSpec` 并支持流式工具增量累加。

**(2) 工具注册表**

新包 `internal/tools`。核心**只有 5 个工具**，不要多：

| 工具 | 作用 | 为什么必须 |
|---|---|---|
| `list_agents` | 看有哪些 agent、各自角色和状态 | 派活前得知道谁能干 |
| `create_agent` | 建一个子 agent（name / role / **可选 backend**） | **派活的核心动作**。backend 必须在用户已配置的 provider 白名单内，否则报错给模型 |
| `send_message` | 给某 agent / 某 conversation 发消息 | 派活 / 回话 |
| `wait_for` | 等某个 agent 或某个 conversation 到达某状态 | **同步语义，防失控** |
| `report` | 把结果汇报给用户（结束本轮） | 终止条件 |

**不要**在这一层加 shell/文件/网络工具 —— 那是 W3 的活，混进来会让派活路径难以验证。

**(3) 执行循环**

`execute` 从「单轮」改成「多轮直到 `report` 或超预算」：

```
loop (max 8 turns):
    resp = llm.Stream(history, tools)
    if resp 有 tool_calls:
        for each call:
            结果 = dispatch(call)        // 落到 tools 包
            history += tool_result
    else:
        break
```

**必须有硬上限**：轮次（8）、总 token、总时长、子 agent 创建数量（如 5）。没有这些，一个循环错误的 agent 会产生无限 agent 或无限 token。

**(4) 子 agent 生命周期**

- `create_agent` 创建的子 agent 记 `ParentID`。
- 父 agent 结束后，子 agent **保留**（用户能看到它们干了什么），但标记 `Orphaned`。
- `ParentID` 进 `Agent` 结构体，**必须同步四处白名单**（`Save` / `Load` / `Snapshot` / `viewMap`）——
  这是仓库既有约定，`manager_test.go` 的 round-trip 测试会守住。
- 侧栏按 `ParentID` 做**一层缩进嵌套**（P3 已定），不建独立分组。
- 防环：`ParentID` 链深度上限（如 3），超过则 `create_agent` 直接返回错误给模型。
- 防风暴：同一父 agent 在单轮内创建 agent 数量受限。

**怎样算可用**
1. mock backend 下一句话「让前端和后端各出个方案」→ 真的创建 2 个 agent 并收到各自回复（mock 工具调用可脚本化，用于测试）
2. 真实 LLM 下，工具调用能往返超过 1 轮
3. 超预算时优雅终止，不是崩
4. `ParentID` 链超过 3 时不创建，返回错误给模型

**风险如实说**：openAI 兼容接口的工具调用格式在各家（Ollama / DeepSeek / Claude 网关）**有差异**，流式工具增量累加是主要坑点。这一阶段的工作量大约是 2.1+2.2 的总和。

---

### 阶段 2.4 · 交互层重做

**建什么**
- **composer module**：草稿 + 补全 + 校验 + 提交，单聊/群聊共用。
- 切换会话保留草稿、滚动位置。
- 消息上的 `Deliveries` 变成可见状态（✓ / ✕ 队列满 / — 跳过原因）。
- 侧栏按 `ParentID` 嵌套在父 agent 下方（一层缩进）。父 agent 停止后子 agent 标「孤立」但仍可操作。

**怎样算可用**
1. 在 A 会话输入一半切到 B 再切回，草稿还在
2. 群里发消息后能看出谁收到了、谁没收到、为什么
3. 被派活的子 agent 在侧栏有可见的父子关系

---

### 阶段 2.5 · 观测与止血

**建什么**
- 活动流（已有 `ActivityFeed`）接入真实投递事件。
- token 用量按 agent / 按会话树汇总。
- 一键停止整棵 agent 树（不是单个）。

**为什么放最后**：前面都通了才需要看。放前面会和调试搅在一起。

---

## 3. 一张表：现在 vs 目标

| 能力 | 现在 | 2.1 后 | 2.2 后 | 2.3 后 |
|---|---|---|---|---|
| 群聊消息存几份 | N+2 | 1+N（N=纯引用） | 1+N | 1+N |
| agent 看见彼此发言 | ❌ | ❌ | ✅ | ✅ |
| 群聊不 @ 时的行为 | 恒不回（bug） | 可配置 | ✅ 按 policy | ✅ |
| 投递失败可见 | ❌ 被吞 | ✅ | ✅ | ✅ |
| agent 主动派活 | ❌ | ❌ | ❌ | ✅ |
| 子 agent 层级 | 无概念 | 无 | 无 | ✅ 有 ParentID |
| 切换会话保留草稿 | ❌ | ❌ | ❌ | ✅（2.4） |

---

## 4. 已拍板的决策

### P1 · 群聊 = 共享 transcript + 协作（已定）
记于 [`ADR-0001`](../adr/0001-group-chat-discussion-model.md)。
互相可见，但**一次用户发言最多传播一轮**，避免往复风暴。多轮讨论由用户再发或 agent 显式发起。

### P2 · `create_agent` 可指定 backend（已定）

签名形状：

```
create_agent(name, role, backend?) → AgentRef

backend 省略时继承父 agent 的 backend。
backend 显式给出时，必须在「用户已配置的 provider 白名单」内，否则返回错误给模型。
```

**白名单从哪来**：`SettingsModal` 里用户配过的 provider（`mock` 恒可用；`ollama`/`openai`/`deepseek`/`claude`
以用户填过 `baseUrl` 或 `apiKey` 的为准）。**不接受模型自己编 provider 名**——那是提权路径。

### P3 · 子 agent 在侧栏嵌套于父 agent 下（已定）

侧栏结构：

```
▼ 后端 agent        [运行中]
    ├ 前端 agent    [运行中]   ← 由后端 create_agent 创建
    └ 测试 agent    [空闲]
▶ 前端 agent        [空闲]
● #评审频道         3 成员
```

- 缩进一层，不建独立分组 —— 独立分组会让「谁派了谁」在视觉上断掉。
- 父子关系来自 `Agent.ParentID`。
- 父 agent 停止时，子 agent 不自动停（它们可能还在干有用的事），但 UI 标「孤立」。

### P4 · ADR 已记（已定）
`docs/adr/0001-group-chat-discussion-model.md`。

---

## 5. 一个明确的不做

**不做多 agent 自动编排（Supervisor 自动拆解决策）**。

理由：2.3 做完之后，agent 已经能自己派活，这已经是「自动编排」。再加一层「Supervisor 替 agent 决定拆解」是重复，而且会让调试变成不可能 —— 出问题时分不清是 Supervisor 选错还是 agent 执行错。

**先让 agent 自己会派活，再看是否需要替它决策。**
