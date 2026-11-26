# 能力差距与二次开发路线（核实版）

> 日期：2026-11-26 · 分支 `fix/p0-correctness-stopgap` · HEAD `2c3f2f1`
> 本文替换一份口头分析。**那份分析描述的是 11-25 之前的工具面，其中三条建议已被 11-26 的提交做掉。**
> 逐条核实结果见 §1，未核实的外部说法见 §4。

## 0. 一句话

能力短板不在模型，在工具面 —— 这个判断成立。但**具体清单要更新**：
检索工具（`grep`/`glob`）和 patch 编辑（`apply_patch`）**已经有了且不进审批**，
真正还缺的是 **verify 闭环**、**只读工具并行**、**评测集**。

## 1. 逐条核实（有代码证据）

| 原分析的说法 | 核实结果 | 证据 |
|---|---|---|
| 「整个工具面只有 4 个原语」 | ❌ **过期** | `internal/tool/builtin/builtin.go:499 RegisterRoots` 注册 7 个：read/write/edit/bash/**grep/glob/apply_patch**。加上 web、会话/笔记/技能/记忆/邀请，实际约 20 个 |
| 「没有 grep、没有 glob」 | ❌ **错** | `internal/tool/builtin/grep.go:30`、`glob.go:27` 均存在并已注册 |
| 「每条 grep 都要弹审批卡」 | ❌ **错，这是原分析的核心论据** | `grep.go:32` 与 `glob.go:29` 都是 `Writes() bool { return false }` → 不进 `Approver`。检索**零审批成本**，已经是最优形状 |
| 「没有 list_dir」 | ✅ 成立 | 全仓 grep `"list_dir"` 无命中 |
| 「没有 verify 工具」 | ✅ **成立，这是当前最大的缺口** | 全仓 grep `"verify"` 工具名无命中 |
| 「edit_file 靠字符串唯一匹配，缺行号锚点」 | ⚠️ **部分过期** | `edit_file` 仍是唯一匹配（`builtin.go:390`），但 `apply_patch.go:34` 已提供 unified diff 通路。原建议「新增 apply_patch」**已完成** |
| 「`Bash.Writes()` 恒 true」 | ✅ 成立 | `builtin.go:49`。但因为 grep/glob 已独立，bash 不再是检索的唯一通路，危害比原分析描述的小得多 |
| 「`defaultNoProgress = 3`，badArgs 到 6 停」 | ✅ 成立 | `internal/loop/loop.go:199` `= 3`；`:431` `badArgs >= r.MaxRoundsWithoutProgress*2` |
| 「loop 里工具顺序 Invoke」 | ✅ 成立 | `loop.go:391` `for _, c := range turn.Calls` 串行；全文件无 `go func`/`WaitGroup`/`errgroup` |
| 「Registry 有 Specs() 和统一 Invoke，便于挂 MCP」 | ✅ 成立 | `internal/tool/tool.go` 的 `Registry` 形状未变 |

**结论**：原分析 10 条里，3 条错、1 条部分过期、6 条成立。
错的那三条恰好是它**论证最用力**的部分（「检索靠猜」「每条 grep 弹卡」「能力差与权限洞同源」），
所以它给出的**第一优先级建议已经不需要做了**。

## 2. 真正还剩的缺口（按投入产出排）

### P0 · verify 闭环（唯一还没动的大缺口）

没有 verify 工具，模型说「改完了」时系统只能记录单次 bash 退出码，
无法证明整个任务通过。这类失败看起来做完了、其实是错的，最容易被感知成「能力差」。

做法：新增 `verify` 工具，接受命令（默认取仓库测试入口），
把 exit code / 通过失败数 / 失败摘要写进 `audit` 与会话，
并在系统提示里写死「声称完成前必须调用它」。

注意：本仓 `config.yaml` 的 `verify` 是 `npx tsc --noEmit`，**不覆盖 Go 侧**。
默认命令不能直接取它，否则 Go 改动会被判「验过了」。

### P1 · 只读工具并行

`read_file`/`grep`/`glob`/`web` 可并发，写类仍串行。
模型一次给五个 read 时现在只能排队。改动集中在 `loop.go:391` 那个 for。

### P1 · 循环参数按任务分档

探索阶段 `noProgress=5`，写码阶段回到 3。
现在恒 3，对「先探索再动手」的大任务容易误判成打转。

### P2 · list_dir

`glob` 能覆盖大部分场景，但「这个目录下有什么」用 glob 表达不自然。小改动。

### P2 · 评测集（最重要，但放最后）

拿本仓库 10-20 个真实 issue，量完成率 / 工具调用数 / 耗时。
没有这个，「能力差」永远只是体感，改提示词也不知道是变好还是变坏。
前面没做完时评测也没意义，所以排最后 —— 但它是唯一能把体感变成数字的东西。

### P0 · 上下文工程四条（源自 `gp-context-lab` 评估，证据见 §7）

1. **摘要 Epoch 冻结**：预算只有 `sys + 8000 + 400`（`loop.go:472-490`），越过之后每轮迭代都重算摘要（`loop.go:687` → `compact.go:96-104`），多一次模型调用、多一份延迟，还让前缀每轮失效。
2. **摘要账本化 + 溯源**：`compactSystem` 是六段自由文本，新旧决定冲突无法仲裁；`PutFact` 的 upsert 还会把 `source_event_id` 清空（`store.go:324-330`）。
3. **截断可回查**：`tool.go:298-316` 头尾各半、原文不落库、全仓无 restore 工具。
4. **缓存记账**：零 `cache_control`、零 `cached_tokens`，现在答不出「命中率是多少」。

## 3. 二次开发四种做法 · 对照本项目现状

| 做法 | 行业形态 | 本项目现状 |
|---|---|---|
| ① **MCP 客户端化** | 不自造工具，把 server 的 tools 挂进来 | ❌ **未做**。全仓唯一 MCP 痕迹是 `internal/runtime/acp/client.go:356` 的 `"mcpServers": []any{}` —— 那是给 ACP 外部 agent 传空列表，**不是 MCP 客户端**。`Registry` 形状适合挂 bridge，这条建议仍然有效 |
| ② **SDK / 插件化** | runtime 是引擎，工具与策略是插件层 | ⚠️ **部分**。`internal/runtime` 已把 native loop 与 ACP 外部进程抽到统一协议（`native.go` 的 `ExecFunc`/`Native`），这是引擎层。但**工具仍是硬编码注册**，加一个工具要改 `RegisterRoots` + 提示词两处 |
| ③ **Harness fork** | fork 改提示词与工具，做垂直领域 | 不适用本项目定位（自己就是 harness） |
| ④ **Agent 当网关 / UI 当壳** | 消息渠道 → 网关 → agent | ✅ **已做**。`internal/gateway`（broker/binder/ports/service）嵌进主进程，监听 `127.0.0.1:8780`；飞书仍走 Hub，网关对 feishu 入站返回 `409 CHANNEL_NOT_OWNED`，不重复回复 |

**注意 ACP ≠ MCP。** `internal/runtime/acp` 是 Agent Client Protocol（接外部 agent 进程），
MCP 是 Model Context Protocol（接工具 server）。两者互补，不能互相替代。
本项目已有 ACP，缺 MCP。

## 4. 未核实的外部说法（不要当事实引用）

原分析里这些说法**我没有能力在本机验证**，写在这里以免后续被当成已证事实：

- 「MCP 在 2026 已是事实标准」「Anthropic 2025-12 捐给 AAIF」「社区 1000+ server」
- 「MAST 里『无或不完整验证』占失败模式 8.2%」
- 「Cline 把引擎做成 `@cline/sdk`，可 `createTool` + lifecycle hooks」
- 「OpenClaw MIT、29 个渠道」

方向判断（工具生态外包、不自造浏览器/数据库工具）我认同，
但**具体数字与版本号需要查一手来源再引用**。本仓已有一手调研的 precedent：
`docs/reviews/research-multi-agent-orchestration.md` 每条结论都挂了出处页面。

## 5. 对 11-26 那批改动的评价

见 `docs/reviews/status-2026-11-26.md`（已完成清单与验证结果）。我的判断：

**做对的三件事**

1. **`internal/runtime` 这条缝抽得准。** native loop 与 ACP 外部进程统一到 `Event`/`Stop`/`Turn`，
   上层不再关心事件来自进程内还是子进程。这正是「引擎 / 插件」分层的正确起点，
   也是后续挂 MCP bridge 的落脚处。
2. **网关与飞书的归属边界划清了。** `409 CHANNEL_NOT_OWNED` 是显式拒绝而不是静默丢弃，
   避免了「两条长连接重复回复」这类最难查的问题。
3. **grep/glob 定为只读**（`Writes()=false`）。这一个决定就把检索成本从「一次点击」降到零，
   是本轮体感提升最大的改动 —— 且它已经做完了。

**需要留意的两点**

1. **记忆缺长期保留策略（单事件防护已有，长期没有）。**
   已核实**有**的：`internal/memory/bridge.go:106` `sha1Key(agentID,role,content,ts)` 去重、
   `:113` `seen` 命中即跳过、限流与队列满计入 `Dropped`、单条正文按 rune 截断。
   这几层做得对，短期不会灌爆。
   已核实**没有**的：保留期 / 容量上限 / 定期清理。
   `internal/memory/store.go:145-157` 的 `DELETE FROM *_fts` 是 FTS 索引同步触发器，
   不是保留期清理。所以 `memory.db` 随使用时长单调增长，没有上界。
   建议加一条保留策略（按条数或时间窗），否则长期使用后检索会被陈旧记忆稀释。
2. **verify 缺口没被这批改动覆盖。** 记忆和计划解决的是「跨会话连续性」，
   不是「本轮做对了没有」。后者仍然没有闭环 —— 这是我会排在下一位的事。

**一句话**：这批改动把「基础设施」补齐了（运行时分层、网关、记忆、调度），
方向对、边界清楚。剩下的短板收敛到很具体的一件事：**证明任务真的做完了**。

## 6. 建议的下一步

**两条线并行：先工具面、后上下文，评测贯穿。**

1. `verify` 工具 + 系统提示强制（P0，本仓 verify 命令要区分 Go/TS）——「做完了没有」的闭环，是现在最大的能力缺口。
2. 摘要 Epoch 冻结（半天；`State` 加 `SummaryAtIter` / `SummaryDroppedTokens`，改 `loop.go` 的 compact 触发条件）——省每轮一次摘要调用，顺带稳住前缀。
3. `PutFact` 保留 `source_event_id`；`facts` 加 `status` / `supersedes`，注入只渲染 active（半天，迁移幂等）。
4. 只读工具并行（P1，改 `loop.go:391` 的串行 for）。
5. 截断带 `call_id` + 新增 `restore_observation` 只读工具（1 天，零审批）。
6. LCP 缓存仿真 + 10–20 个真实 issue 评测集（2–3 天；与 §2 的 P2 合流，先量后改）。
7. MCP bridge（P2，`Registry` 形状已具备；MCP 的「需要确认」映射到现有 `Approver`）。

**不建议继续投入**：自研浏览器工具、数据库工具（Cline/Cursor 已做透）；图扩散 + ACT-R + k-DPP/MMR 检索栈（需实体图与嵌入，而 `grep`/`glob` 已零审批覆盖主动检索）。

差异化在「控制点 + 舰队视图 + 审批归属 + 内嵌网关」，这些别人没有。

## 7. 参考来源：`gp-context-lab` 的可抄与不抄

> 评估日期 2026-11-26，对象 `github.com/npc1607/gp-context-lab` @ `6330ec8`（5,336 行零依赖 Go，28 个合成用例）。
> 该仓库是只读参考，**已从工作区删除**；需要复查时按上述 commit clone 到仓库外，不要放回项目内。

### 7.1 它诊断的四个瓶颈 vs 我们的现状

| 瓶颈（lab 对 gp 的诊断） | 我们的对应现状 | 判定 |
|---|---|---|
| G1 压缩前线性全量发送 | 超预算才压，但预算只有 `sys+8000+400`、上限 70% 窗口 | 部分同构：中长会话几乎每轮都触发 |
| G2 无多跳关联召回 | `memory_search` = FTS5 trigram + LIKE + 时间倒序（`search.go:26-70`） | 同构且更弱（纯朴素 RAG） |
| G3 自然语言摘要漂移、无溯源 | 六段自由文本 + 每轮重算（`summarize.go:52`） | 完全同构 |
| G4 滑动窗口击穿前缀缓存 | 摘要每轮重写、压缩边界每轮前移；无 `cache_control` | 比 gp 更糟 |

### 7.2 可抄的四件（已并入 §2/§6）

1. **Epoch 冻结**：lab 实测 `Epoch=12–16` 是帕累托平台（Suite D 缓存命中 79–80%、成本降 52%）；小于 8 频繁重建击穿缓存，大于 24 近期窗口过肥、总 Token 降不下来。我们只需「阈值触发重算摘要」，不需要工作集。
2. **结构化约束账本**：`facts(subject,predicate,object,confidence,source_event_id)` 现成，只缺 `status`/`supersedes` 与确定性序列化（按 id 字典序，字节级稳定）。
3. **可逆溯源**：占位符带 `call_id` + `restore_observation(call_id,start,end)`。gp 已实现同一思路，按 `call_id` 分页取回原始 `RoleTool`。
4. **LCP 缓存仿真**：`internal/cache/simulator.go` 核心 45 行，逐轮 token 序列最长公共前缀 + 双厂商计费（Anthropic write 1.25 / read 0.10；OpenAI read 0.25；最小可缓存 1024）。

### 7.3 不抄的四件

| 不抄 | 理由 |
|---|---|
| PPR + 热核扩散 + ACT-R 幂律 + k-DPP/MMR | 需实体图 + 嵌入 + 图索引；我们由 `grep`/`glob` 主动检索覆盖，边际收益远低于 gp，且 lab 只做到离线仿真 |
| 自研子词 tokenizer | `llm.EstTokens` 已够预算控制；真实误差用 provider usage 校准即可 |
| LLM client / judge / dataset 全套 | 我们已有 provider 抽象；judge 依赖外部付费 API，日常迭代用不起 |
| HeuristicExtractor | 作者自测：5 个隐晦用例把过时污染率从 0% 打到 41.7%，明确「绝不能用于生产」 |

### 7.4 数字边界（不能当我们的 KPI）

- 28 个合成用例、自制 tokenizer（对真实 DeepSeek 中位误差 8.1%、p95 23.6%）、Phase 3 单 provider。
- 诚实 `B1` 基线：缓存命中 40.1%、含缓存账单比全量发送**还贵 5.4%**；`E4-A` 79–80% / 降 52%。
- 早期 `B1` 用 48 字符截断，作者自认削弱基线，修正后召回 33.3% → 83.3%，旧 E2E 29.2% 已标注高估。
- `E4-A` 的收益有一半来自「少发 70% token」，不只是缓存布局；我们预算本来只有 ~8.4k，收益结构不同。

### 7.5 证据索引

- 我方：`loop.go:294/307/472-490/497-503/687`、`llm/compact.go:96-104`、`agent/summarize.go:18-60`、`tool/tool.go:298-316`、`memory/search.go:26-70`、`memory/store.go:312-343`、`llm/openai.go:118`
- lab 侧（clone 后）：`docs/01-问题诊断与总体目标.md`、`docs/03-结构化工作集与防丢信息架构.md`、`docs/07-实验结果与准出评审报告.md`、`internal/cache/simulator.go`、`internal/strategy/experiments.go`

## 出处索引

- 当前状态：`docs/reviews/status-2026-11-26.md`
- 网关评审：`docs/reviews/code-review-gateway-m3-2026-11-26.md`
- 后端交接：`docs/reviews/backend-delivery-handoff.md`（§11 四件事仍待拍板）
- 一手调研范例：`docs/reviews/research-multi-agent-orchestration.md`