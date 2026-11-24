# 研究：多 agent 协作完成任务的成熟工业方案

> 调研日期：2026-11-24
> 范围：一个进程里跑多个 agent、由其中一个把任务拆给其余几个去做。对照的是本仓库现在的东西：
> `Queue+Worker` 会话模型、`internal/loop` 执行器（`Budget` 四项上限）、`internal/tool.Registry`（`Writes()` 触发审批）、
> `ask_agent` / `create_agent` 两个工具、`internal/worktree` 会话级 git 工作树、`backend=supervisor` 的「自动拆任务」会话类型。
> 资料：Anthropic *Building effective agents* 与 *How we built our multi-agent research system* 两篇原文、
> Anthropic Claude Code 官方文档（subagents / worktrees）、OpenAI *A practical guide to building agents*（PDF）、
> OpenAI Agents SDK 官方文档与仓库内 `docs/*.md` 原文、LangGraph 官方 subgraph 文档、
> Cursor 官方 worktrees 文档、Devin 官方 CLI/Cloud 文档、以及 MAST（*Why Do Multi-Agent LLM Systems Fail?*）论文。
> 每条结论下面挂拥有该说法的页面。
>
> **置信度**
> - ✅ 官方文档、一方法源码、已合并 PR、或论文原文明确写出的说法
> - 🔶 官方博客/白皮书里的测量数字，文档没把方法钉死；或由已证事实推出的工程判断
>
> 本文只记各家怎么分工、怎么传上下文、怎么限流、怎么收失败。不写实现代码。

---

## 0. 一句话结论（先看这个）

✅ Anthropic 和 OpenAI 的官方口径是一致的：**默认把单 agent 做到极致，只有当「任务真的能并行」或「工具/指令已经把单个上下文挤爆」时才拆多个 agent**。
Anthropic 明确说多数编码任务的可并行部分比研究任务少，而且 LLM agent 实时协调、互相委派的能力还不够好（[multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system)）。
OpenAI 的白皮书同样写「先最大化单 agent 能力」，拆分的两个触发条件是 prompt 里条件分支太多、或工具数量/重叠太多
（[A practical guide to building agents, p.15](https://cdn.openai.com/business-guides-and-resources/a-practical-guide-to-building-agents.pdf)）。

✅ 两条主流形态的定位很清楚：

| | Manager / orchestrator-worker（agents as tools） | Handoff / 去中心化 |
|---|---|---|
| 谁掌握用户 | 中央 agent 独占 | 交出去，专门 agent 接管 |
| 子 agent 拿到什么 | 一段**任务 brief**（tool 参数） | **整段对话历史**（转移 state） |
| 父 agent 收什么 | tool 返回值（可自定义抽取） | 子 agent 直接面向用户继续 |
| 适用 | 子任务是「有界的活」，干完要交回汇总 | 「路由」本身就是工作流，交出去就不用回来 |

来源：OpenAI Agents SDK [multi_agent.md](https://github.com/openai/openai-agents-python/blob/main/docs/multi_agent.md) 的对照表、
[handoffs 文档](https://openai.github.io/openai-agents-python/handoffs/)（"it's as though the new agent takes over the conversation"）、
[tools 文档 Agents as tools 一节](https://github.com/openai/openai-agents-python/blob/main/docs/tools.md)。

对本仓库：`ask_agent` / `create_agent` 是 **manager 形态**（父会话等回复再继续），Supervisor 是**代码编排的 orchestrator-worker**，
两者都不是 handoff。这个定位是对的，不建议往 handoff 改（理由见 §5、§7）。

---

## 1. Orchestrator-worker vs 去中心化

### 1.1 Anthropic 为什么选 orchestrator-worker

✅ 研究任务是「开放问题 + 路径依赖」，无法预先写死步骤；subagent 的作用是**压缩**：各自在自己的上下文窗口里并行探索不同侧面，
再把最重要的 token 交回 lead agent。同时提供 separation of concerns（不同工具、不同 prompt、不同探索轨迹），降低路径依赖
（[multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system)）。

✅ 架构是严格的 orchestrator-worker：lead agent 分析 → 拆 → 并行 spawn subagent → subagent 各自搜索并回 findings →
lead 综合，判断是否还要再来一轮；够了就交给 CitationAgent 做引用定位
（同上，"Architecture overview for Research"）。

✅ 选择理由里最关键的一条是**测量**：3 个因素解释了 eval 里 95% 的性能方差，其中 **token 用量一项就解释 80%**。
换句话说多 agent 的收益主要来自「花掉了更多 token」，而不是某种架构魔法；多 agent 架构的本质是
「在单 agent 上下文放不下的任务上，把 token 用量撑上去」
（同上，Benefits of a multi-agent system）。

### 1.2 官方明确说「不该用多 agent」的情形

✅ Anthropic 的原文有四处明确的「不要用」：

1. **需要所有 agent 共享同一份上下文的领域**，或**agent 之间依赖很多**的领域，不适合当前的多 agent 系统（同上）。
2. **多数编码任务真正可并行的部分比研究任务少**；而且 LLM agent **还不擅长实时协调和互相委派**（同上）。
3. 文章列的多 agent 擅长的是三件事：高价值且**大量可并行**、信息量**超过单个上下文**、需要对接**大量复杂工具**（同上）。
4. 更早的 *Building effective agents* 总纲：**先找最简单的方案，只在必要时增加复杂度**；
   在很多应用里「单次 LLM 调用 + 检索 + in-context 示例」就已经够用
   （[Building effective agents](https://www.anthropic.com/engineering/building-effective-agents)）。

✅ 同一篇还写了 orchestrator-workers 的适用边界：它适合「**无法预知需要哪些子任务**」的复杂任务
（原文举的例子正好是编码："in coding, for example, the number of files that need to be changed and the nature of the change in each file likely depend on the task"）。
和「并行化（parallelization）」的区别就在于子任务不是预先切好的，而是 orchestrator 按输入现场决定（同上）。

✅ OpenAI 白皮书的对应结论：「我们的总体建议是**先把单个 agent 的能力最大化**，多 agent 会带来额外复杂度和开销，
所以**一个带工具的 agent 往往就够了**」。真正该拆的信号是：
- **复杂逻辑**：prompt 里 if-then-else 分支多到模板难以维护；
- **工具过载**：问题不在工具数量本身，而在**相似度和重叠度**——有的实现能扛 15+ 个定义良好的工具，有的不到 10 个重叠工具就崩；
  且「先把工具名、参数、描述改清楚」试过无效之后才该拆。

来源：[A practical guide to building agents, p.15](https://cdn.openai.com/business-guides-and-resources/a-practical-guide-to-building-agents.pdf)。

🔶 对本仓库的含义：本仓库是**编码**类桌面工具，Anthropic 明确点名「编码可并行部分少、agent 实时协调不行」。
所以 `ask_agent`/`create_agent` 适合的场景应该是**读多写少、边界清晰的旁支任务**（调研一处代码、跑一遍测试并汇报、
查一个报错原因），而不是「把一个 feature 切成 4 块让 4 个 agent 一起写」。这和现在 Supervisor 的默认叙事
（`internal/agent/supervisor.go:22` 起，2-4 个子任务并行写代码）有张力，见 §7。

### 1.3 OpenAI Agents SDK：handoffs vs agents-as-tools

✅ 官方对照表（[multi_agent.md](https://github.com/openai/openai-agents-python/blob/main/docs/multi_agent.md)）：

| Pattern | 怎么工作 | 什么时候用 |
|---|---|---|
| **Agents as tools** | manager agent 保持对对话的控制，通过 `Agent.as_tool()` 调用 specialist | 你要**一个 agent 拥有最终答案**、要**合并多个 specialist 的产出**、要在一处统一施加 SDK guardrails |
| **Handoffs** | triage agent 把对话路由给 specialist，specialist **在本次 turn 余下时间里成为 active agent** | 你要 specialist **直接回复用户**、要 prompt 保持聚焦、要「交接即切换指令」而**不需要 manager 转述结果** |

✅ 官方的选择口径：「当 specialist 只该协助一个**有界的子任务**、不该接管面向用户的对话时，用 **agents as tools**；
当**路由本身就是工作流的一部分**、你想让选中的 specialist 拥有本次 turn 剩下的时间时，用 **handoffs**」（同上）。
两者可以混用：triage 先 handoff 给 specialist，specialist 内部再用 as_tool 调窄子任务（同上）。

✅ Handoff 的语义要点（[handoffs 文档](https://openai.github.io/openai-agents-python/handoffs/)）：
- handoff 在 LLM 眼里就是一个 tool；`handoff()` 的 `description` 用来提示模型何时该选它。
- **发生 handoff 时，新 agent 会看到整段之前的对话历史**；想改这一点必须显式传 `input_filter`（`handoff输入` / `new_items`，可裁剪或替换转发内容）。
- `on_handoff` 回调可以在交接瞬间做副作用；`input_type` 可以要求模型在交接时附带一小段结构化元数据（如 `{"reason": ...}`），
  但官方明确写了：**这类结构化输入是「模型临时决定的元数据」，不能用来传应用状态或依赖**。

✅ Agents-as-tools 的语义要点（[tools.md, Agents as tools](https://github.com/openai/openai-agents-python/blob/main/docs/tools.md)）：
- **默认不继承父 run 的对话状态**：「the parent run's conversation state is not inherited automatically」，
  要用共享历史必须显式把同一个 `session` 传给两边。
- 默认输入形状是 `{"input": "..."}` 一个字符串；要结构化入参用 `parameters`（Pydantic/dataclass）+ `include_input_schema`，
  或用 `input_builder` 完全自定义「结构化参数 → 子 agent 输入」的映射。
- 出参可以用 `custom_output_extractor` 改写：从子 agent 的 `RunResult` 里**只抽一段 JSON 或一段文本**交给父 agent——
  这正是「压缩回传」的官方挂点。
- `Agent.as_tool(..., max_turns=...)` 可以给子 run 单独设轮次上限；`needs_approval=` 复用 HITL 的 pause/resume 审批流（同文档 "Approval gates for tool-agents"）。

🔶 对本仓库的含义：本仓库的 `ask_agent` 实际上是 **agents-as-tools 且默认无状态共享**（`internal/agent/ask_tool.go:76` 只传 `Text`，
子会话自己的 `Messages` 是它自己的），这一点和 OpenAI 默认行为**天然一致**，不需要改。
但 OpenAI 明确提供了 `custom_output_extractor` 这一层——本仓库现在没有，回传的是子会话最后一条 assistant 文本
（`internal/agent/ask_tool.go:116` 的 `lastAssistantTextSince`）。这就是 §2 里「压缩回传」缺的那一环。

---

## 2. 子 agent 的上下文隔离与结果回传

### 2.1 子 agent 拿到什么：精简 brief，不是全量历史

✅ Anthropic 在 research 系统里踩过坑并且写下了修正：一开始允许 lead agent 给「research the semiconductor shortage」这种**简短指令**，
结果 subagent **误解任务或做了完全重复的搜索**（一个去查 2021 汽车芯片危机，另两个重复查 2025 供应链）。
结论是每个 subagent 都需要：**目标（objective）、输出格式（output format）、该用什么工具和来源的指引、清晰的任务边界**
（[multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system)，"Teach the orchestrator how to delegate"）。

✅ 同一篇还强调**按复杂度缩放投入**，并给了具体数字：简单事实查找 1 个 agent、3-10 次工具调用；
直接对比 2-4 个 subagent、每个 10-15 次调用；复杂研究可以 >10 个 subagent，职责明确划分。
早期版本常见失败是「简单查询也 spawn 50 个 subagent」（同上，"Scale effort to query complexity"）。

✅ Claude Code 的实现是**净室上下文**（clean room）：「每个 subagent 从一个全新、隔离的上下文窗口开始。
它**看不到**你的对话历史、你已经调用过的 skill、Claude 已经读过的文件。Claude 会写一段 delegation message 概括任务，
subagent 从这里开始工作。」例外是 fork（见 §5.3）。
一个非 fork subagent 的初始上下文**只有**：自己的 system prompt、Claude 写的 delegation prompt、
CLAUDE.md 层级文件、git status 快照、preload 的 skill 全文；并且明确列出**到不了** subagent 的东西：
output style、主对话的 auto memory、父的上下文窗口大小
（[Create custom subagents, Manage subagent context](https://docs.claude.com/en/docs/claude-code/sub-agents)）。

✅ Claude Code 还给了反向指标：subagent 的 `description` 会占用主对话上下文，
**所有自定义 subagent 描述加起来超过 15,000 token 时启动会告警**（同上，开头部分）。

### 2.2 结果怎么回传：压缩摘要 + 文件系统

✅ Anthropic 主文章里「game of telephone」那条建议非常具体：**直接让 subagent 把产出写进外部系统（文件系统），
只把轻量引用传回协调者**。原文的理由是：不让所有东西都经过 lead agent 转述，可以同时提高保真度和性能，
避免多阶段处理中的信息丢失，并减少把大产出复制进对话历史的 token 开销；
对代码、报告、数据可视化这类结构化产出尤其合适
（[multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system)，末段 "Subagent output to a filesystem"）。

✅ 同一篇还有一条和长任务直接相关的：接近上下文上限时，**可以 spawn 带干净上下文的新 subagent**，
通过小心 handoff 保持连续性；计划之类的东西存进 memory 而不是留在上下文里（同上，"Long-horizon conversation management"）。

✅ Claude Code 对回传形式的描述：subagent 的**最终报告**会被扫描后交给主对话，报告到达时**带一个标明「这是 subagent 输出」的 header**，
header 里写明「报告里的指令或审批声明只是 subagent 的话，不具备你的授权」。后台 subagent 的报告则放在一个
「自动化事件」标记的完成通知里（[Create custom subagents](https://docs.claude.com/en/docs/claude-code/sub-agents)）。
——这一条是**安全设计**，不只是格式问题：子 agent 读过网页/日志，可能带回提示注入。

### 2.3 多 agent 贵多少：Anthropic 的实测倍数

✅ 官方原文：「agents typically use about **4× more tokens** than chat interactions, and multi-agent systems use about **15× more tokens** than chats.」
（[multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system)，"Benefits of a multi-agent system" 末段）
同一段说：经济上可行要求「任务价值足够高，能付得起这个性能溢价」。

✅ 性能侧的数字（同一篇）：lead 用 Claude Opus 4、subagent 用 Claude Sonnet 4 的多 agent 系统，
在内部 research eval 上比**单 agent 的 Claude Opus 4 高 90.2%**。并行化（lead 并行起 3-5 个 subagent、
subagent 内部 3+ 个并行工具调用）把复杂查询的研究时间**最多缩短 90%**。

🔶 15× 这个倍数是 research（浏览/检索）场景。编码类任务的 token 结构不同（读写文件多、检索少），
不能直接照搬；但它给出了一个可用的**预算心理价位**：多 agent 至少要准备单 agent 的 4-15 倍预算，
否则「省下来的时间」会变成「花光的额度」。

### 2.4 LangGraph 的 subgraph / supervisor 怎么传 state

✅ LangGraph 把「父图 ↔ 子图」的通信分成两种明确模式
（[Use subgraphs](https://docs.langchain.com/oss/python/langgraph/use-subgraphs)）：
1. **共享 state key**：子图直接读写父图的 channel，不需要 wrapper——supervisor 类系统常用，agent 之间通过共享的
   message key 通信。
2. **不同 state schema（不共享 key）**：必须写一个 wrapper 函数，**显式把父 state 映射成子图输入，再把子图输出映射回父 state**。
   官方说明这正是「想给每个 agent 保留私有消息历史」时的常见做法。文档里还给了 parent → child → grandchild 三层、
   且强调「parent 或 grandchild 的 key 在 child 里访问不到」的完整例子。

✅ LangGraph 还要求开发者显式选一个**子图持久化寿命**（同上，"Use subgraph persistence"）：
- `per-invocation`（每次调用全新）——官方说**这是多数应用的推荐模式，包括把 subagent 当 tool 用的系统**，
  支持 interrupts、state 检查、并行调用，且每次调用互相隔离；
- `per-thread`（跨调用累积）——给需要多轮记忆的 subagent；**官方明确警告 per-thread 子图不支持并行工具调用**：
  模型可能并行调同一个 per-thread 子 agent，两个调用写同一个 namespace 会造成 checkpoint 冲突，
  必须用 `ToolCallLimitMiddleware` 或禁用并行调用挡住。

🔶 对本仓库的含义：本仓库已经是 **per-invocation 语义**——`ask_agent` 每次都往目标会话的队列里投一条消息、等本轮结束
（`internal/agent/ask_tool.go:76`），子会话的历史属于它自己，不与父共享。这正是 LangGraph 和 OpenAI 都推荐的默认。
**不要**为了「让子 agent 懂更多」而把父会话的 `Messages` 灌进子会话——那会同时破坏上下文隔离（省不下 token）
和 OpenAI 的默认无状态语义。要传的是 §2.1 说的 brief（目标、输出格式、边界、工具指引）。

---

## 3. 递归与成本控制：业界的数字

这一节是全文里唯一有**官方具体数字**的地方，逐条列。

✅ **递归深度**：Claude Code 官方——「默认情况下，一个 subagent 可以再 spawn subagent，**最多到主对话以下三层**。
到达深度上限时，Claude Code 把 `Agent` 工具从除 fork 外的所有 subagent 里撤掉，所以在上限处的 subagent 自己做掉被委派的工作并返回一份 summary。」
可通过 `CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH` 改成任意层数（设 0 关掉嵌套）
（[Create custom subagents, Let subagents spawn their own subagents](https://docs.claude.com/en/docs/claude-code/sub-agents)）。

✅ **并发上限**：Claude Code 官方——「默认当**一个会话里有 20 个 subagent 正在跑**时，再用 Agent 工具 spawn 新的会失败并返回
`Concurrent subagent limit reached`，而且**错误里明确告诉 Claude 不要重试**；等运行数掉到上限以下才能再 spawn。」
可通过 `CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS` 改。会占用名额的还包括用户手动 fork、resume 的 subagent；
teammate 类走自己的限制。**官方补充：整个会话内可 spawn 的 subagent 总数没有上限**（同上，"Concurrent subagent limit"）。

✅ **单个子 run 的轮次上限**：OpenAI Agents SDK 的 `Agent.as_tool(..., max_turns=...)` 给**嵌套 run 单独**设上限，
和父 run 的轮次预算分开（[tools.md](https://github.com/openai/openai-agents-python/blob/main/docs/tools.md)，"Customizing tool-agents"）。

🔶 **预算传递（父的 token/time budget 如何传给子）**：Anthropic、OpenAI 两家**都没有**给出「父预算按比例切给子」的官方机制。
能在一手资料里找到的只有两类做法：
1. **在 prompt 里写死投入规则**——Anthropic 就是这么干的：把「简单 1 个 agent / 3-10 次调用，对比 2-4 个 agent / 各 10-15 次调用，
   复杂 >10 个 agent」直接写进 prompt，因为「agent 判断该花多少力气这件事本身很弱」
   （[multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system)，"Scale effort to query complexity"）。
2. **在代码里设独立的硬上限**——Claude Code 的深度 3 层 / 并发 20 / `max_turns`，都是有默认值的代码常量或环境变量，
   不是模型自己算出来的（见上）。

🔶 结论：**「父预算切分给子」不是业界通行做法；通行的做法是「每一层都有独立的、写死的硬上限，外加 prompt 里的软性投入指引」。**
这条直接决定了本仓库该怎么做：`internal/loop.Budget` 的 `MaxIterations=24 / MaxToolCalls=64 / MaxWallMS=600_000`
（`internal/loop/loop.go:55-62`）已经是子会话自带的硬上限，**不需要父会话再传一份**；
需要补的是「**一次派活能开多少个子会话**」和「**子会话能不能再派活**」这两条。

---

## 4. 失败处理与部分成功

✅ **Anthropic 的原则是「错误会复合，所以要么能续跑，要么让模型知道并自己适应」**：
- 出错误时**不能从头重启**（太贵、用户会烦），系统要能从出错的位置**恢复**；
- 同时「让 agent 知道某个工具在失败、让它自己去适应，效果出奇地好」——
  也就是把确定性重试/检查点和模型的自适应结合（[multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system)，"Production reliability"）。
- **同步执行是个明确的已知瓶颈**：官方写「目前 lead agent 同步执行 subagent，等一组 subagent 全部完成才继续」，
  好处是协调简单，坏处是「**整个系统可能因为等一个最慢的 subagent 而卡住**」，lead 无法中途纠偏、subagent 之间无法协调。
  异步能带来更多并行，但会引入结果协调、状态一致、错误传播的难题，官方说等模型能力上来再吃这个复杂度（同上，"Synchronous execution creates bottlenecks"）。

✅ **Claude Code 对部分成功有非常具体的回传规则**（[Create custom subagents, API errors in subagents](https://docs.claude.com/en/docs/claude-code/sub-agents)）：
- 前台 subagent 因 rate limit / overload / server error 中断：**如果它已经产出了文本，Agent 工具就把那段部分产出返回**，
  并附注「subagent 被截断，没完成任务」；
- **一点产出都没有、或只有工具调用的**：返回失败 `Agent terminated early due to an API error` + 错误详情；
- 后台 subagent：标记 failed，但「**Claude 收到的消息里会带上 API 错误名和 subagent 最后的输出，所以部分工作不会丢**」。
- 撞到上下文上限时：「返回的产出被标记为 partial，Claude 可以 resume 这个 subagent 继续干」（同文档 "Resume subagents"）。
- 配了 fallback model 链时，subagent 会切到链上第一个可用的模型继续跑，而不是直接失败；报告开头会有一行
  `⚠ This agent switched to <model> because its earlier model could not be used.`（同上）。

✅ **子 agent 输出是不可信输入**：Claude Code 会扫描每个 subagent 的最终报告，对
「仿冒 harness 标签（如 `<system-reminder>`）」「提到 `bypassPermissions` 等权限设置」的内容插入反斜杠或加标记行；
官方明确这条扫描**不判断内容恶意与否**，也不改变报告能造成的影响——真正的防线仍是权限系统
（同上，"Subagent output scanning"）。

🔶 **对本仓库的含义**：现在 Supervisor 的收集逻辑（`internal/agent/supervisor.go:158-186`）在 ctx 超时/用户停止时
把未回来的子任务标成「未完成：ctx error」，**但不会保留已经产生的部分文本**——因为 `RunPrompt` 只在整轮结束时才返回。
而 Anthropic 和 Claude Code 的一手结论都是「部分产出必须交回，否则前面的钱白花」。
这一条是可落地的改动点（见 §7）。

---

## 5. 工具化 vs 独立入口

### 5.1 业界主流：agent 通过 tool call 自主调起

✅ Claude Code：`Agent` 工具（**在 v2.1.63 之前叫 `Task`**，现有 `Task` 引用作为别名继续生效）。
Claude 根据每个 subagent 的 `description` 自己决定何时委派；用户也可以显式 `@agent-<name>` 提及、或让它「explicitly」调用。
官方还给了「鼓励主动委派就在 description 里写 *use proactively*」的 prompt 技巧
（[Create custom subagents](https://docs.claude.com/en/docs/claude-code/sub-agents)）。

✅ 两个层次并存，且**边界写得很清楚**：
- 「Subagents work within a single session.」（subagent 是会话内的能力，是工具）
- 「To run many independent sessions in parallel and monitor them from one place, see **agent view**；
  a coordinated team of sessions Claude spawns and supervises, see **agent teams**；
  separate sessions that pass messages to each other, see **cross-session messaging**。」
也就是说：Claude Code 自己就把「会话内 subagent（工具）」和「多会话编排（独立入口）」当成**两套东西**，
而不是把其中一个做成另一个的替代品（同上，开头）。

✅ OpenAI Agents SDK：handoff 本身就是注册给模型的 tool（[handoffs](https://openai.github.io/openai-agents-python/handoffs/)），
所以「自主调起」和「用 tool 表达」是同一件事的两面。

### 5.2 Claude Code Task/Agent 工具的具体设计

✅ **上下文隔离**：净室（见 §2.1）。**例外**是 fork：fork 继承**整段对话**（同样的 system prompt、tools、model、message history），
但 fork 自己的 tool 调用**不会**进父对话，只有最终结果回来
（[Create custom subagents, Fork the current conversation](https://docs.claude.com/en/docs/claude-code/sub-agents)）。
官方的选择口径是：「当任何其他 subagent 都需要太多背景才用得起来时，用 fork；或者你想从同一个起点并行试几种方案时」（同上）。

✅ **并行调用**：subagent 由 tool call 调起，因此**和普通工具一样受「一条 response 里多个 tool_use」约束**——
Anthropic 的 batch 语义是按出现顺序执行、失败即停（这一点在本仓库另一篇调研 [research-web-automation.md §2](research-web-automation.md) 里已引过
[Computer use 文档](https://platform.claude.com/docs/en/agents-and-tools/tool-use/computer-use-tool)）。
Claude Code 文档侧只承诺「并行研究」这个用法：「对独立调查，同时 spawn 多个 subagent…… 每个各自探索自己的区域，然后 Claude 综合」，
**并明确警告代价**：「当 subagent 完成，它们的结果回到主对话。**跑很多个各自返回详细结果的 subagent 会消耗大量上下文，
而且每个 subagent 在运行期间自己也在烧 token**」
（[Create custom subagents, Common patterns](https://docs.claude.com/en/docs/claude-code/sub-agents)）。

✅ **结果返回形式**：最终报告 + 「subagent 输出」header + 注入扫描标记（见 §2.2）；后台执行的 subagent
以「完成通知」形式在**后续 turn** 到达，Claude 在报告前会等这个通知（同文档，"Tools, permissions, and results of background subagents"）。

✅ **前后台**：默认（非 fork、非 teammate）从主对话 spawn 的 subagent **在后台启动**；后台 subagent 的工具集会**比前台小**；
后台 subagent 的权限提示会浮到主会话里，并注明是哪个 subagent 在问（同文档，"Run subagents in foreground or background"）。

✅ **可限制谁能被 spawn**：在 main thread 用 `claude --agent` 时，`permissions` 里的 `Agent(worker, researcher)` 是**allowlist**——
只允许 spawn 列出的类型，模型 prompt 里也只看到这些类型；`permissions.deny` 里的 `Agent(Explore)` 可屏蔽单个类型；
**完全不写 tools 列表则该 agent 一个 subagent 都 spawn 不了**（同文档，"Restrict which subagents can be spawned"）。

### 5.3 对本仓库的含义

🔶 本仓库现在**两条路都有**：`ask_agent`(`internal/agent/ask_tool.go:20`) 是工具，
`backend=supervisor`(`internal/agent/supervisor.go`) 是用户显式选的会话形态。这和 Claude Code 的
「subagent 是会话内工具 / agent view 与 agent teams 是独立入口」是**同一种分层**，不需要二选一。
建议保留两者，但把职责划清（见 §7 第 4 问）。

---

## 6. 并行执行的写冲突

✅ **git worktree 是这一代编码 agent 的事实标准，且官方文档明确写「为了并行、避免冲突」。**

✅ **Claude Code**：有专门页面 *Run parallel sessions with worktrees*，开篇即
「Isolate parallel Claude Code sessions in separate git worktrees so changes don't collide.」
「Running each Claude Code session in its own worktree gives it a separate copy of the files to edit, so one session can build a feature while a second fixes a bug.」
`claude --worktree <name>` 在仓库根的 `.claude/worktrees` 下建工作树，分支名 `<name>`；工作树是**全新 checkout**，
所以要自己装依赖（可以用 `setup` 脚本自动化）。退出时 Claude 会检查「删除会不会丢工作」（改动/未跟踪文件、子模块里的未提交内容、新提交），
并按情况提示保留或删除；**发现另一个 session 在用这个工作树时，工作树和分支都保留**（[Run parallel sessions with worktrees](https://docs.claude.com/en/docs/claude-code/worktrees)）。

✅ **Cursor**：有专门的 worktrees 文档，同样是「并行开工避免冲突」——
「Worktrees let Agent work in isolated Git checkouts. Each task gets its own files, dependencies, and changes while your main checkout stays untouched.」
「Use worktrees when you want to start several agents on the same repo **without conflicts**.」
并且做了工程化配套：每台机器按最大数量定期清理旧工作树、按 OS 分别给 setup 命令、明确建议**不要**把依赖 symlink 进工作树
（会污染主工作树），改用快的包管理器
（[Cursor docs: worktrees](https://cursor.com/docs/configuration/worktrees)）。

✅ **Devin**：隔离层次比 worktree 更重——**每个 cloud session 一台自己的 VM**（shell + 浏览器 + 仓库副本），
与本地会话分开；本地侧另有 OS 级 sandbox（`--sandbox` 强制可写路径和 deny 规则）
（[Devin CLI Cloud](https://docs.devin.ai/cli/cloud.md)、[Devin CLI Sandbox](https://docs.devin.ai/cli/sandbox.md)）。
并行做迁移这类工作的官方说法是「parallel sessions + skills」（[Java Upgrades](https://docs.devin.ai/use-cases/examples/java-upgrades.md)），
靠**多台 VM**而不是同一 checkout 上的分支隔离。

✅ **OpenAI Codex**：cloud 侧是**每个任务一个容器**：建容器 → checkout 到指定分支/SHA → 跑 setup 脚本 →
agent 在容器里循环跑终端命令、改码、验证 → 结束给 diff 并可开 PR；容器状态缓存最多 12 小时
（[Codex cloud: environments](https://developers.openai.com/codex/cloud/environments)）。
也就是「一任务一环境」，隔离粒度同样是**独立检出**而不是共享工作区。

🔶 归纳：**四家的隔离粒度不同（worktree / 每任务容器 / 每会话 VM），但方向一致——并行写同一个仓库必须给每个执行体一份独立的检出，
合并通过分支/PR 显式进行，绝不靠「小心别改同一处」这种约定。** 本仓库已有 `internal/worktree`（分支 `mad/<slug>`，
工作树在 `.mad-worktrees/<repo>/`，`docs/design.md:382`）并且 Supervisor 已经给每个子任务建工作树
（`internal/agent/supervisor.go:100-129`），**这一点和业界一致，属于已经做对的少数项**。

---

## 7. 对本仓库的结论

下面逐条回答那 6 个问题，并给具体取值。所有取值都标明是「照抄官方数字」还是「按本仓库约束推的」。

### 7.1 架构形态：保留 manager / orchestrator-worker，不引入 handoff

✅ 依据：Anthropic 与 OpenAI 都把「一个 agent 拥有最终答案、合并多 specialist 产出」指向 **agents-as-tools**；
handoff 的前提是「路由本身就是工作流、交出去不用回来」，而本仓库的用户始终在单聊窗口里等一个答案。
另外 Anthropic 明确说 LLM agent **不擅长实时协调和互相委派**——去中心化的 handoff 链路越长越危险。

**建议**：
- `ask_agent` / `create_agent` 保持 manager 形态（父会话等回复），**不改成 handoff**。
- 不要引入「子 agent 直接接管用户对话」的能力。本仓库已有 `internal/agent/manager.go:1143` 的 `beginAsk` 环检测，
  这条要**保留并扩展到 delegate 路径**（见 7.3）。

### 7.2 上下文与回传

🔶 依据：§2.1（Anthropic「brief 四要素」+ Claude Code 净室）、§2.2（Anthropic「写文件 + 回引用」、Claude Code「最终报告 + header」）、
§2.4（OpenAI agents-as-tools 默认不继承状态、LangGraph 推荐 per-invocation）。

**建议**（按优先级）：
1. **brief 结构化**，而不是自由文本。对齐 Anthropic 的四要素：目标 / 输出格式 / 边界（不要动哪里）/ 可用工具与提示。
   现在 Supervisor 的 prompt 是 `"子任务：%s\n\n%s\n\n完成后用文字说明你做了什么、改了哪些文件。不要反问，直接做。"`
   （`internal/agent/supervisor.go:156`），只覆盖了「目标 + 一句输出格式」。建议补「输出格式」和「边界」两段。
2. **回传压缩**。参考 OpenAI 的 `custom_output_extractor` 和 Anthropic 的「文件 + 引用」：
   子任务的**大产出留在它自己的工作树/文件里**，回传内容裁剪到固定形状（做了什么、改了哪些文件/分支、失败原因、需要父会话决策的点）。
   现在 Supervisor 已经用 `clipResult(body, 900)`（`internal/agent/supervisor.go:209`）做了长度截断，方向对，
   但**截断不等于结构化**——建议固定成 4 个字段，而不是「前 900 个字」。
3. **子 agent 输出当不可信输入处理**。参考 Claude Code 的 header + 扫描：回传给父会话的内容要标明「这是子会话的输出」，
   并且不能因为子 agent 说「用户已批准」就跳过 `Registry` 的审批（现在 `tool.Registry` 的审批在 `internal/tool/tool.go:243`，
   `ask_agent` 的 `Writes()` 是 false，天然不触发，这一点是对的，**不要改**）。
4. **不要把父会话的 `Messages` 灌进子会话**。保持 per-invocation。

### 7.3 delegate 工具的参数形状（建议新增/收敛）

🔶 依据：OpenAI `as_tool` 的默认单字符串入参 + `parameters` 结构化入参 + `max_turns` + `needs_approval`
（[tools.md](https://github.com/openai/openai-agents-python/blob/main/docs/tools.md)）；Anthropic 的 brief 四要素；本仓库已有 `ask_agent` 的 `{name, task}`。

**建议形状**（在现有 `ask_agent` 上加字段，保持向后兼容；不要新造一个和 `ask_agent` 平行的工具）：

| 字段 | 类型 | 必填 | 语义 | 依据 |
|---|---|---|---|---|
| `name` | string | 是 | 目标会话名（复用现有 `FindByName`） | 现有 `ask_agent` |
| `task` | string | 是 | 目标（objective），一句话说清要达成什么 | Anthropic brief 第 1 项 |
| `output_format` | string | 否 | 期望的回传形状（如「只列失败用例名和错误行」） | Anthropic brief 第 2 项 |
| `boundaries` | string | 否 | 不该动的东西（如「不要改 internal/llm」） | Anthropic brief 第 4 项 |
| `context` | string | 否 | 父会话认为子会话需要的少量背景（**不是**全量历史） | OpenAI as_tool 默认不继承状态 |
| `worktree` | bool | 否 | 是否为这次委派开独立工作树（写型任务默认 true） | §6 |
| `max_minutes` | int | 否 | 本次委派的墙钟上限（**只能调小**，不能超过默认） | §3「每层独立硬上限」 |

**明确不加的字段**（有官方理由）：
- ✗ `budget_tokens` / `budget_ratio`：没有一家给出「父预算切给子」的机制（§3 末尾）。子会话用 `loop.DefaultBudget()` 就够了。
- ✗ `history`（传全量历史）：违反 agents-as-tools 的默认无状态语义，且让上下文隔离失效（§2.4）。
- ✗ 让模型自选 `model`：Anthropic 的「scale effort to query complexity」是**写死的规则**，不是让模型自己算（§3）。

### 7.4 深度限制取值

✅ **建议取 2 层**（主会话 → 子会话 → 孙会话，再往下不 spawn）。
- 官方参照：Claude Code 默认 **3 层**（`CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH`，§3）。
- 为什么本仓库取 2 而不是 3：本仓库**会话总量硬上限只有 8 个**（`internal/agent/ask_tool.go:128` `maxAgents = 8`），
  而 Claude Code 是 20 并发、总 subagent 数不设限。8 个名额下 3 层嵌套会迅速撞满名额，
  按「每层独立硬上限」原则（§3），层数要跟着名额缩。
- **落地方式**：给 `Agent` 加一个 `Depth` 字段（主会话 0），spawn 时 `child.Depth = parent.Depth + 1`；
  `Depth >= 2` 时 `ask_agent`/`create_agent`/delegate **不出现在工具表里**（而不是调用时报错）——
  这是 Claude Code 的做法（「At the depth limit, Claude Code withholds the Agent tool」），比报错省一轮 token。

### 7.5 并发上限取值

✅ **建议显式区分两个数，并让它们都受 `maxAgents` 约束**：
- **全局会话总数**：保持 8（`maxAgents`）。这是本仓库的既有约束，且是 UI 侧栏能展示的量级。
- **单次派活的并发子任务数**：**3**。
  - 官方参照：Anthropic research 是「lead 并行起 **3-5** 个 subagent」（§3）；Claude Code 是 **20** 但那是 subagent 不是独立会话。
  - 本仓库取 3 的理由：Supervisor 现在取 4（`internal/agent/supervisor.go:22` `maxSubtasks = 4`），
    加上父会话本身是 5 个会话；如果同时还有一个飞书 helper（`assistCap = 3`，`internal/agent/manager.go:696`），
    很容易撞 8 的名额上限并在中途降级。**取 3 留出余量**，且落在 Anthropic 给的 3-5 区间内。
- **超出时不要静默截断**。现在 Supervisor 是 `plan.Subtasks = plan.Subtasks[:avail]` + 一行 note（`supervisor.go:80-86`）。
  Claude Code 的做法是**明确失败并告诉模型不要重试**（`Concurrent subagent limit reached`，§3）。
  建议：名额不足时**整个派活直接失败**并说明，而不是悄悄少派两个——静默降级会让父会话以为任务做全了。

### 7.6 失败处理与部分成功

🔶 依据：§4（Anthropic「错误复合，要能续跑 + 让模型适应」、Claude Code 的 partial 回传规则）。

**建议三条**（都是有官方对照的）：
1. **部分产出必须回传**。子任务超时/中断时，若已有 assistant 文本，标成 `partial` 交回父会话，并注明「被截断，未完成」——
   照 Claude Code 的前台规则。现在 Supervisor 在 `goto summarize` 分支里只写 `"未完成：ctx error"`（`supervisor.go:170-178`），
   丢掉了已有产出。
2. **一个子任务失败不取消其它**。现在已经是这个语义（每个 goroutine 独立跑，收集时按 `filled[]` 区分），✅ 保持。
   汇总里明确列出「N 个失败、分支和工作树保留」（`supervisor.go:211-213` 已有）——这一点和 Anthropic/Claude Code 一致。
3. **总时限必须短于子预算之和，且要单独可见**。现在 `supervisorTimeout = 20 * time.Minute` 而子会话自己还有
   `MaxWallMS = 600_000`（10 分钟）。两个数都合理，但**父的超时到了会连带毁掉所有还在跑的子任务**——
   建议父超时触发时**不 cancel 子会话**，只停止等待（子会话继续跑完，结果写进它们自己的会话里，用户仍然能进去看）。
   这条是 Anthropic「同步执行会因为等一个慢 subagent 把整个系统卡住」的直接对冲（§4）。

### 7.7 是否需要审批

✅ **建议：delegate 调用本身不需要审批；子会话内的写操作按现有规则审批。**
- 依据 1：OpenAI 的 `as_tool` 默认**不需要审批**，只在显式传 `needs_approval=` 时才走 pause/resume（§1.3）。
- 依据 2：Claude Code 的 subagent **继承父会话的权限规则**，权限提示会浮到主会话并注明是哪个 subagent 在问（§5.2）。
- 依据 3：本仓库的 `Registry` 已经是「`Writes()` 为 true 才走 `Approver`」（`internal/tool/tool.go:243`），
  `ask_agent`/`create_agent` 都是 `Writes() == false`（`ask_tool.go:28, 169`）。这套语义和两家一致，**不要给 delegate 加审批**。
- **需要补的是**：子会话的 `Approver` 要能标注来源。现在 `loop_backend.go:48` 取了 `m.Approvals()`，
  建议在审批上下文里带上 `parentAgentID` + `depth`，让用户在弹窗里看得出「这是 Supervisor 第 2 个子任务发起的写操作」。
- **例外（唯一建议加审批的场景）**：`depth > 0` 的会话**再 spawn 子会话**时，若该会话是写型工作树——
  建议加一次「子会话要再开一个会话，是否允许」的确认。理由是名额有限（8）而嵌套会让用户失去对会话树的控制。

### 7.8 是否保留 Supervisor 会话类型

✅ **建议保留，但要改叙事和默认值。**
- **保留的理由**：Claude Code 自己就同时有「会话内 subagent（工具）」和「多会话编排（agent view / agent teams）」两个层次，
  并明确说它们是给不同场景的（§5.1）。本仓库的 Supervisor 正对应后者，且它有两个工具给不了的东西：
  **固定形状的汇总**（`supervisor.go:196-212` 起的 numbered list）和**稳定可观测的会话树**（侧栏嵌套）。
- **要改的叙事**：现在 Supervisor 的 prompt 说「把用户的目标拆成 2-4 个可以同时进行的子任务」
  （`supervisor.go:236`），隐含「切 feature 一起写」。但 Anthropic 明确说**编码的可并行部分比研究少**
  （§1.2）。建议把 prompt 收窄到 Anthropic 点名的三个适合场景：
  ① 大量可并行的独立调查；② 单个上下文放不下的信息量；③ 需要对接多种不同工具。
  并**显式禁止**「并行改同一批文件」的拆法（这本来就是 §6 里所有厂商用独立检出绕开的问题，而不是靠拆任务解决的）。
- **要改的默认值**：`maxSubtasks` 4 → **3**（理由见 7.5）；子任务默认开工作树这一点 ✅ 保持。
- **新增一条**：Supervisor 拆解失败时现在退化成单任务（`supervisor.go:74-78`）。这和 Anthropic 的
  「先试单 agent」原则其实是一致的（§1.2），**保留**，但要在 UI 上说清「这次没拆，是按单个任务跑的」。

### 7.9 不建议做的事（附理由）

| 不做 | 理由 |
|---|---|
| 引入 handoff / 子 agent 接管对话 | 用户始终是「在一个窗口里等一个答案」；handoff 要求交出去不回来（§1.3） |
| 让模型自己算该花多少 token | 无任何一家这么做；Anthropic 明确 agent 判断投入能力弱，改为 prompt 里写死规则（§3） |
| 父预算按比例切给子 | 无一手资料支持；通行做法是每层独立硬上限（§3） |
| 子 agent 之间互传消息（去中心化） | Anthropic 自己都说 agent 实时协调能力不够；本仓库还有 `beginAsk` 环检测要维护（§1.2、§7.1） |
| 把父对话历史灌给子会话 | 违反 agents-as-tools 默认语义、上下文隔离失效、token 直接按 15× 起步（§2.4、§2.3） |
| 用「约定别改同一处」代替工作树 | 四家全部用独立检出（worktree / 容器 / VM）（§6） |
| 名额不足时静默少派子任务 | Claude Code 的做法是明确失败并告诉模型别重试（§3、§7.5） |

---

## 附：一手资料清单

- Anthropic, *How we built our multi-agent research system* — <https://www.anthropic.com/engineering/multi-agent-research-system>
- Anthropic, *Building effective agents* — <https://www.anthropic.com/engineering/building-effective-agents>
- Anthropic, Claude Code *Create custom subagents* — <https://docs.claude.com/en/docs/claude-code/sub-agents>
- Anthropic, Claude Code *Run parallel sessions with worktrees* — <https://docs.claude.com/en/docs/claude-code/worktrees>
- OpenAI, *A practical guide to building agents* (PDF) — <https://cdn.openai.com/business-guides-and-resources/a-practical-guide-to-building-agents.pdf>
- OpenAI Agents SDK, *Agent orchestration*（仓库原文）— <https://github.com/openai/openai-agents-python/blob/main/docs/multi_agent.md>
- OpenAI Agents SDK, *Tools / Agents as tools*（仓库原文）— <https://github.com/openai/openai-agents-python/blob/main/docs/tools.md>
- OpenAI Agents SDK, *Handoffs* — <https://openai.github.io/openai-agents-python/handoffs/>
- OpenAI Codex, *Cloud environments* — <https://developers.openai.com/codex/cloud/environments>
- LangGraph, *Use subgraphs*（state 传递与持久化寿命）— <https://docs.langchain.com/oss/python/langgraph/use-subgraphs>
- Cursor, *Worktrees* — <https://cursor.com/docs/configuration/worktrees>
- Devin, *CLI Cloud* / *CLI Sandbox* — <https://docs.devin.ai/cli/cloud.md>、<https://docs.devin.ai/cli/sandbox.md>
- Cemri et al., *Why Do Multi-Agent LLM Systems Fail?*（MAST，1600+ 条 trace、14 种失败模式）— <https://arxiv.org/abs/2503.13657>

本仓库内相关阅读：`docs/reviews/research-web-automation.md`（tool batch 语义、上下文裁剪的同类调研）、
`docs/adr/0001-group-chat-discussion-model.md`（为何暂不做「由系统替 agent 决定谁发言」）、
`docs/design.md:234`（Supervisor 现状）、`docs/reviews/collaboration-plan.md`（协作从 0 到 1）。
