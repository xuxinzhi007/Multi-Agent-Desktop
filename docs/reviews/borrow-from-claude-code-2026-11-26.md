# 参考 Claude Code 改造：四条机制 + 六个可读的开源实现

> 2026-11-26 · 配套 `benchmark-cli-speed-2026-11-26.md`（差距清单）与 `direction-cli-first-2026-11-26.md`（方向）
> 本文回答一个问题：**具体抄什么、抄到哪个文件、抄完能快多少。**
> 所有外部结论都挂了来源，未核实的不写。

## 0. 结论

Claude Code 快，不是因为模型，是因为 harness 的四条机制：

① **按「是否并发安全」分批执行工具**　② **五层压缩 + 缓存边界固定**　③ **权限分档，默认放行编辑**　④ **子 agent 独立上下文、只回摘要**

这四条我们**一条都没对齐**，而且四条都有现成开源实现可读，不需要自己发明。

---

## 1. 四条机制拆解

### ① 并发分批（这条最立刻见效）

官方文档写得很清楚：一次回合里模型给出多个工具调用时，**只读工具（Read / Glob / Grep / 标记为只读的 MCP 工具）并发执行，改状态的工具（Edit / Write）串行执行**。
泄露源码的分析更细：模型的流式输出 `[Read(a), Read(b), Write(c), Read(d)]` 会**按 `isConcurrencySafe` 分批**，变成「Batch1 并行 Read+Read → Batch2 独占 Write → Batch3 并行 Read」。

我们的现状：`loop.go:391` 一个 `for _, c := range turn.Calls` 从头串到尾，`internal/loop` 与 `internal/tool` 全包无 `errgroup`/`WaitGroup`。**读 5 个文件 = 5 次等待。**

### ② 压缩分层 + 缓存边界

arXiv 那篇架构分析把 Claude Code 的压缩总结成**五层流水线**：budget reduction → snip → microcompact → context collapse → auto-compact，每次模型调用前依次过一遍。
泄露分析里还有一条关键设计：**prompt cache 边界**（`__DYNAMIC_BOUNDARY__`），把系统提示切成「字节级稳定的静态前缀 + 每轮可能变的动态后缀」，并且所有 fork 出来的调用（压缩、记忆抽取、摘要）共享同一份 `CacheSafeParams`（systemPrompt / userContext / tools / messagePrefix 都字节级一致），所以**压缩本身不击穿缓存**。

我们的现状：预算固定 `sys+8000+400`（`loop.go:476-477`），越过就每轮判断、可能重算（`loop.go:307/326` → `compact.go:96`）；**没有静态/动态分段，也没有 cache_control 标记**（`usage.go` 只统计命中，不产生命中）。

### ③ 权限分档

官方 SDK 列了六档，语义是这样的：

| 模式 | 行为 |
|---|---|
| `default` | 需要批准且未被 allow 规则覆盖的调用走回调；**没有回调＝拒绝** |
| `acceptEdits` | **自动放行工作目录内的文件编辑与常见文件系统命令**，bash 仍按规则 |
| `plan` | 只探索出方案，编辑即使匹配允许规则也要过回调 |
| `dontAsk` | 从不弹窗，会弹的一律拒绝（无人值守用） |
| `auto` | 模型分类器逐条审（shell/网络请求） |
| `bypassPermissions` | 全放行，仅限隔离环境 |

注意它的默认推荐：**在开发机上用 `acceptEdits`**——编辑自动放行，危险集中在 bash 命令上管。

我们的现状：`ask` / `auto` / `readonly` 三档，**默认 `ask`**，每写一步一次点击。方向对，默认值错了。

### ④ 子 agent 独立上下文

官方文档：子 agent 由模型自己决定要不要开、开几个；**每个子 agent 独立上下文窗口，只把摘要回给父级**；可以限制嵌套深度（默认 3 层）、并发数（默认 20）、总花费。

我们的现状：`delegate.go:140` 每个子任务**无条件** `worktree.Add` 开 git 工作树 + 新建子会话，结果整体回填。

---

## 2. 六个可直接读的开源实现

| 仓库 | 语言 / 许可 | 对我们最有用的点 |
|---|---|---|
| **ClawHarness**（`agenmod/claw-harness`） | MIT | 自称逐条逆向 Claude Code：**并行读 / 串行写自动分批**、5 种压缩策略、bash 语义安全库（130+ 命令）、**prompt cache 边界**、子 agent 隔离上下文、413 恢复。四条机制都有对应实现，最适合当教科书 |
| **OpenCode** | TS / MIT | 后台 subagent、build / plan 双 agent 切换、75+ provider —— 我们"多 provider"这条路它走完了 |
| **openHarness** | MIT | 体量小（42 工具 + subagents + plan mode），适合直接读一遍完整 harness 的形状 |
| **Crush**（charmbracelet） | FSL（源码可见，非 OSI） | TUI 的流式渲染与输入处理，我们前端该学的部分 |
| **Goose**（block） | Apache-2.0 | MCP 集成方式；我们将来挂 MCP 时读它 |
| **Pi** | MIT | "lean / token 高效"的极端版本，用来校准我们的提示词预算 |

> 许可证提醒：Crush 是 FSL，不是 OSI 开源，商用/分发前要看条款；其余 MIT/Apache 可放心读代码。

---

## 3. 逐条移植到我们的代码

| 机制 | 改哪里 | 具体做法 | 预期 |
|---|---|---|---|
| 并发分批 | `internal/loop/loop.go:391` + `internal/tool/tool.go` 的接口 | 给 `Tool` 加 `ConcurrencySafe() bool`（或复用现有 `Writes()` 取反）；把 `Calls` 切成「连续只读段 / 写段」，只读段用 `errgroup` 并发，写段串行；**结果按原 `Calls` 顺序回填**，消息序列不变 | 读 5 文件：5 次等待 → 1 次 |
| 权限默认 | `internal/approval` 的 `ParseMode` 默认值 | 默认改成 `auto`（≈ `acceptEdits`：写文件放行，`bash` 仍走 shellguard + 规则）；新增「这个工具以后都允许」按 `工具名 + 根目录` 记忆 | 每天点击从几十降到个位数 |
| 压缩分层 | `internal/agent/summarize.go`、`llm/compact.go`、`loop.go:307/326` | **先只做第 0 层**：`State` 加 `SummaryAtIter`，只在「距上次摘要新增 ≥N token」才重算；其余轮次复用。五层全上留到后面 | 中长会话每轮少一次模型调用 |
| 缓存边界 | `loop_backend.go` 系统提示拼装 + `llm/openai.go` / `claude` 请求体 | 系统提示拆「静态段（身份、规则、工具说明）」+「动态段（工作目录、项目列表、时间）」；Anthropic 形状在静态段末尾打 `cache_control`；OpenAI 兼容靠服务端自动，不用改 | 首 token 变快，成本下降 |
| 子 agent 轻量化 | `internal/agent/delegate.go:140` | `delegate` 入参加 `mode: "read" \| "write"`；`read` 类子任务**不开 worktree**，同进程、独立上下文、只回摘要 | 调研类子任务省掉 git 开销 |
| **不做** 7 档权限 + ML 分类器 | — | 我们没这个训练/调用预算；`shellguard` 正则 + 路径展开已经能挡住真实危险 | 避免把简单问题复杂化 |
| **不做** 五层压缩全套 | — | 一次上五层=一次改五处，回归风险极高；先拿到 epoch 的收益再评估 | 控制爆炸半径 |
| **不做** append-oriented 会话存储 | `agents.json` 整份重写 | 长会话会慢，但改成 append 存储是破坏性迁移，等前面几条稳定后再说 | 降低本次改动面 |

---

## 4. 三步走（每步都可独立验收）

1. **今天（半天）**：并发分批 + 一条「并发后消息序列不变」的测试。改动只碰 `loop.go` 与 `tool.go` 的接口。
2. **接着（半天）**：权限默认改 `auto` + 「以后都允许」记忆。改动只碰 `approval` 与 UI 弹层。
3. **然后（1–2 天）**：压缩 epoch + 系统提示静态/动态分段 + `cache_control`。改动碰 `loop.go`、`summarize.go`、provider 请求体。

三步都不涉及 ACP、网关、记忆、调度，互不冲突；仓库里其他人正在改的 `verify.go` / `broker.go` 也不会打架。

---

## 5. 验收指标

| 指标 | 现在 | 目标 |
|---|---|---|
| 一轮读 5 个文件 | 5 次往返 | 1 次 |
| 中长会话每轮模型调用 | 2（正常 + 摘要） | 1 |
| 每天点「允许」 | 几十 | ≤ 5 |
| 首 token 延迟（长会话） | 全量前缀重算 | 命中缓存前缀 |

---

## 出处

- 并发执行与权限模式：<https://code.claude.com/docs/en/agent-sdk/agent-loop>
- 子 agent 深度/并发/花费上限：<https://code.claude.com/docs/en/agent-sdk/subagents>
- 架构分析（五层压缩、七档权限、AgentTool）：<https://arxiv.org/html/2604.14228v2>
- 泄露源码复盘（按 `isConcurrencySafe` 分批、`CacheSafeParams` 缓存共享）：<https://01.me/files/claude-code-talk/dist/slidev-exported.pdf>
- ClawHarness（并行/串行分批、5 种压缩、cache 边界、子 agent 隔离）：<http://www.shturl.cc/cf0cd17fa76c130636c66a2dfb48536b>
- 开源 CLI agent 目录（OpenCode / Goose / Pi / openHarness 等）：<http://www.shturl.cc/77322048d0216c48c23b5336cf89d444>
- 开源替代品横向对比（许可、本地模型、定位）：<https://pinggy.io/amp/blog/best_open_source_cli_coding_agents/>
