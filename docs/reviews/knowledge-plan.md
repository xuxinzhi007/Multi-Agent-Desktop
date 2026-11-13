# multi-agent-desktop 知识库：存储与联动方案

> 架构评审的可执行版本。评审原文见 [`knowledge-architecture-review.html`](knowledge-architecture-review.html)，
> 索引与纯文本摘要见 [`README.md`](README.md)。
> 本文只写**要做什么、按什么顺序、怎样算做完**；不写代码。

---

## 1. 结论先行

`internal/knowledge` 本体没有病。`fileStore` / `memoryStore` 是两个真实 adapter，JSONL 追加 O(1) +
rename 重写，7 个测试守住并发与损坏行。**问题在它对外的 interface 形状，和周围五条注入路径的语义分叉。**

三条注入路径写进 `Agent.Messages`，两条完全不注入，而没有一条能撤回。用户在 UI 上做的选择
（scope、attach）在读取侧没有对应实现。

一句话：**知识库有存储层，没有「一个 agent 该看到什么」这一层。**

---

## 2. 现状取证

| 事实 | 位置 | 判据 |
|---|---|---|
| 六条路径中三条写 system prompt | `main.go:104` `systemPromptFor` / `manager.go:667` `AppendSystemMessage` | 已证实 |
| `AttachNoteToAgent` 在 service 层做业务 | `main.go:511` | 已证实 |
| Scope 只被写、未被读 | `main.go:938` `Query{Scope: ""}` | 已证实 |
| top-8 是魔数，过滤条件只有「全局」 | `main.go:932` `maxKnowledgeLines` | 已证实 |
| CLI agent 拿不到 history 与知识库 | `manager.go:558` → `runner.Run(ctx, text)` | 已证实 |
| Channel 广播 prompt 只有一句话 | `main.go:318` `SendToChannel` | 已证实 |
| 删除笔记后 system message 仍在 | `manager.go:667` 追加即永久 | 已证实 |
| attach 无 detach、无回读 | `AgentView` 不含引用集合 | 已证实 |

### 注入路径矩阵

| 触发 | 内容 | 取自 | 可撤销 |
|---|---|---|---|
| `Manager.Create` | top-8 全局笔记 | `Query{Scope:""}` | 否 |
| `AttachNoteToAgent` | 单条笔记 | 全量 `Query{}` 线性扫描 | 否 |
| Channel 广播 | 无 | — | — |
| CLI agent | 无 | — | — |
| 删除笔记后 | 已写入的内容 | — | 否 |

---

## 3. 目标形状

```
        ┌─────────────────────────────────────┐
调用方  │ Create / attach / Preview / Channel │
        └──────────────┬──────────────────────┘
                       │  View(agent) → (prompt 段, 命中 ID)
        ┌──────────────▼──────────────────────┐
        │  internal/knowledge  ← deep module  │
        │  ┌────────────────────────────────┐ │
        │  │ scope 解析 · 打分 · 预算裁剪     │ │
        │  │ 稳定排序 · prompt 渲染 · 变更检测│ │
        │  └────────────────────────────────┘ │
        │  Store（fileStore / memoryStore）    │
        └─────────────────────────────────────┘
```

**interface 收敛到一个函数**，`agent` 包只依赖它的返回，不再各自实现选笔记逻辑。
`Store` 保留在 interface 里 —— 两个真实 adapter 让这条 seam 成立，不动。

---

## 4. 分步实施

顺序不可调换：#2 修数据形状，#1 才收得动 interface。

### 步骤 1（P0）· `Agent.Knowledge []string`

**为什么先做**：这是唯一已经在产生错误行为的地方。attach 写进 history 后删不掉、看不见、
重复 attach 会叠加、绕过唯一 seam。

**改什么**
- `Agent` 新增 `Knowledge []string`，存 note ID。
- 同步四处白名单：`Save` / `Load` / `Snapshot` / `viewMap`（`manager_test.go` 的 round-trip 测试守住）。
- 删除 `AppendSystemMessage`；`AttachNoteToAgent` 改为改引用集合。
- 注入点下移到 `executeMock` / `executeCLI` 组装 history 的那一行。

**语义**
- attach 幂等：同一个 ID 重复 attach 不新增。
- 新增 `DetachNote`。
- 删除笔记时，所有 `Agent.Knowledge` 里的该 ID 一并清理（或渲染时跳过失效 ID，二选一，见待拍板）。

**怎样算满足**
1. 命令：`go test ./internal/agent -run 'TestKnowledgeRef|TestSessionConfigPersistenceRoundTrip'`
   期望：通过；且 `Agent.Knowledge` 经 Save/Load 往返不丢。
2. 命令：`go test ./internal/agent -run TestAttachIdempotent`
   期望：连续 attach 同一条两次，`len(Messages)` 不变、`len(Knowledge)` == 1。
3. 命令：旧 `TestAppendSystemMessage` 已删除（不是改成 pass）。

---

### 步骤 2（P0）· `knowledge.View(agent) `

**为什么**：`NotesForSystemPrompt()` 是 `Query` + `strings.Join`，删掉它复杂度只会搬进
`Manager` —— 过不了 deletion test。

**改什么**
- `knowledge` 新增 `View(agent) → (Block string, IDs []string)`。
- 删除 `main.go` 的 `knowledgeProvider` 与 `maxKnowledgeLines`。
- `agent.KnowledgeProvider` 收窄为 `View`。
- `TestSystemPromptFor` 的表驱动用例下移到 `knowledge`。

**待拍板**：seam 位置——
- **A**：`agent` 反向 import `knowledge`（deep，但耦合；无循环依赖，agent 已 import `llm`/`cli`）
- **B**：`View` 只做纯函数（`[]Note → Block`），渲染留给 Sidecar（松，但浅）

建议 A。B 等于把「选哪些」留在调用方，已经证伪。

**怎样算满足**
1. 命令：`go test ./internal/knowledge -run TestView`
   期望：三种 scope、预算裁剪、稳定排序各有用例；同一输入两次调用结果字节一致。
2. 命令：`go vet ./... && go test ./...`
   期望：`knowledgeProvider` 在代码中零引用。

---

### 步骤 3（P1）· 三档 scope

**为什么**：UI 让用户选「仅 Agent X」，`Note.Scope` 写进 JSONL，然后没有任何读取路径用它。
用户以为写了一条私有约定，实际永不生效。**这是承诺与行为的背离，不是功能缺失。**

**改什么**
- `ScopeGlobal`（`""`，兼容现状）/ `ScopeAgent(id)` / `ScopeChannel(id)`。
- `View` 按 agent 私有 → channel → global 取并集，同 key 后者覆盖前者。
- 全局笔记受预算约束，agent 私有不占全局预算。
- `Query.Scope` 保持精确匹配语义不动（它只是 store primitive）。

**向后兼容**：现有 `knowledge.jsonl` 里 `scope` 为空或 agent ID，两种都是新语义子集，**无需迁移**。

**怎样算满足**
1. 命令：`go test ./internal/knowledge -run TestQueryFilters`
   期望：旧 scope 用例原样通过（不改断言）。
2. 命令：`go test ./internal/knowledge -run TestViewScopeLayers`
   期望：agent 私有笔记只出现在该 agent 的 View 里，不出现在兄弟 agent。

---

### 步骤 4（P2）· 相关性排序 + 字符预算

**为什么**：判据是写入序，不是相关性。200 条笔记时，昨天写的关键约定被静默丢弃。

**改什么**
- `maxKnowledgeLines`（条数）→ 字符预算。
- 三档：scope 命中（必选）→ tags 命中最近 3 轮文本（相关）→ 最近写入（兜底）贪心填满。
- `View(agent, recentText)` —— `recentText` 由调用方传，View 保持纯函数。

**不做**：向量检索。笔记规模是人类手写的几十到几百条，标签 + 子串 + 时间足够。
引入 embedding 会把零依赖的纯内存 module 变成需要外部服务的 adapter —— 笔记上千条之前是**负 depth**。

**怎样算满足**
1. 命令：`go test ./internal/knowledge -run TestViewBudget`
   期望：200 条输入下，昨天写的关键约定出现在结果里；输出字符数 ≤ 预算。

---

### 步骤 5（P2）· CLI / Channel 复用 assemble

**为什么**：`executeCLI` 调 `runner.Run(ctx, text)`，只有这一轮的 prompt —— CLI agent 实际是
无状态单轮调用，UI 却表现得它有记忆。Channel 广播同理。

**改什么**
- `CLIRunner.Run` 入参 `string` → `Request{System, History, Prompt}`。
- `claude -p` 支持 `--append-system-prompt`；history 拼进 `-p` 正文。
- Channel 广播复用同一个 assemble。

**风险**：**本方案唯一改变外部行为的一项** —— CLI agent 输入变长，token 成本与输出风格都会变。
独立提交、单独验证，不和 1~4 混在一个 diff 里。

**怎样算满足**
1. 命令：`go test ./internal/agent -run TestAssemble`
   期望：`assemble` 是纯函数，快照比对；mock 与 CLI 两条路径产出同一份 prompt 段。

---

## 5. 一次不做

| 项 | 理由 |
|---|---|
| 向量 / embedding 检索 | 负 depth，见步骤 4 |
| 知识库版本历史 / 回滚 | 没有需求证据，JSONL 追加已够追溯 |
| 多用户 / 权限 | 单机桌面应用，无此场景 |
| 富文本 / 附件 / 图片 | 笔记定位是「短约定」，不是文档系统 |

---

## 6. 风险

| 风险 | 缓解 |
|---|---|
| `Agent.Knowledge` 加字段漏掉白名单某处 | round-trip 测试已在，补一条断言引用集合 |
| 步骤 2 引入 `agent → knowledge` 耦合 | 若选 B 方案则绕开；这是唯一需要拍板的架构选择 |
| 步骤 5 抬高 CLI token 成本 | 独立提交 + 字符预算上限 |
| 旧 `knowledge.jsonl` 兼容 | scope 语义是新集合的子集，无迁移 |

---

## 7. 已拍板（回填）

1. **步骤 2 的 seam：选 A。**
   `agent.KnowledgeProvider` 收窄为 `View(agentID, role, knowledgeIDs, recentText, budget) (block, ids)`，
   `agent` 包反向依赖 `knowledge` 的语义（不 import 具体 store）。
   证据：`internal/agent/manager.go` 的 `KnowledgeProvider` 接口 + `main.go` 的 `knowledgeProvider.View`。

2. **删除笔记时：渲染时跳过失效 ID。**
   不主动清理 `Agent.Knowledge`，`ViewFromNotes` 里按 `knownIDs` 查找，找不到的 ID 自然不出现。
   理由：清理需要跨 module 写（`channel` 已有先例，但 `knowledge` 不该反向知道 agent 存在）；
   渲染时跳过让 `Agent.Knowledge` 保持「用户意图的快照」，删掉笔记不会静默改用户选择。

3. **步骤 5 已做。**
   `cli.Request{System, History, Prompt}` + `assemblePrompt` 已落地；
   `ClaudeRunner` 走 `--append-system-prompt`，`CodexRunner` 拼 `-p` 正文。

### 落地状态

| 步骤 | 提交 | 状态 |
|---|---|---|
| 1 · `Agent.Knowledge` | `6368183` | ✅ |
| 2 · `knowledge.View` | `0f15c82` | ✅ |
| 3 · 三档 scope | `66db0fa` | ✅ |
| 4 · 相关性排序 + 预算 | `5c8013e` | ✅ |
| 5 · CLI/Channel 复用 assemble | `28a54d3` | ✅ |

### 顺带修掉的两个缺陷

- `Agent.Snapshot()` 的拷贝白名单漏了 `Knowledge` —— 导致 `toAgentView` 返回的视图里
  引用集合永为空。已补（`internal/agent/manager.go`）。
- 根包 `TestAttachNoteToAgent` 仍断言旧的「append system message」语义。
  已重写为断言引用集合 + 幂等 + Save/Load 往返 + detach。
