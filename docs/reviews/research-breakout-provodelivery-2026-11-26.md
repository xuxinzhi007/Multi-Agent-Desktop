# 一鸣惊人的位置：可证明交付（Provable Delivery）

> 2026-11-26 · 市场 + 论文调研 · 结论先行
> 数据来源全部挂在文末；次要来源（媒体报道）已标注「经报道」，不当一手事实用。

## 0. 结论

**不要在"上下文工程"上再卷一层——那是 2026 年最拥挤的赛道，我们追不上也不该追。**
真正空着、且我们手上已有原材料的位置是：

> **让每一次 AI 交付都能被证明：断言 → 证据 → 可回放 → 可归因。**

一句话叙事：**"我们不承诺模型更聪明，我们承诺每一次交付都能被验证、被回放、被追责。"**

---

## 1. 市场：痛点不在"更聪明"，在"不敢信"

| 数据 | 出处 |
|---|---|
| 99% 组织已在流水线里用 AI agent，75% 允许它改代码/CI/生产 | Palo Alto《2026 State of Cloud Security》，2,800 名安全负责人 |
| **40% 无法完整重建 agent 做过什么**；53% 把 agent 行为记在人名/个人 token 下 | 同上（经 TMCnet 报道） |
| 只有 33% 能把代码从 commit 追到 runtime；仅 37% 强制人工安全评审 | 同上 |
| **76% 生成 AI 代码的速度快于他们能验证的速度**；53% 花在 review 的时间超过写代码（引入 AI 前是 18%） | Sonar《2027 State of Code》1,026 人，2026-11 |
| 94% 并不完全信任 AI 代码；77% 说"强验证工具"能提升信任 | 同上 |
| Gartner 首份《企业级 AI Coding Agent》象限（2026-05）把 **human oversight 与 traceability、每条决策可审计** 列为必备能力 | Gartner MQ，经报道 |
| Agent 代码的 provenance 正在变成合规/保险/安全要求 | Crash Override 2026 知识库 |

翻译成人话：**市场已经从"能不能写"转到"敢不敢用"。** 而"敢不敢用"这一侧，卖的都是组织级平台（Sonar 做代码质量、Palo Alto 做云侧审计），**没有人做"这台机器上这个 agent 这一次到底干了什么"的本地证据链**。

---

## 2. 论文：四条硬结论

| 论文 | 结论 | 对我们的意义 |
|---|---|---|
| **MAST**《Why Do Multi-Agent LLM Systems Fail?》(2503.13657) | 14 种失败模式、3 大类；**任务验证类占失败模式的可观份额**：过早终止 7.82%、无/不完整验证 6.82%、错误验证 6.66%；SOTA 开源 MAS 正确率可低至 25% | "验证"不是加分项，是主要失败源 |
| **Trace-Based Assurance** (2603.18096) | 把执行instrument成 Message-Action Traces + 可机器判定的 contract；能定位"第一个违规步"、支持确定性重放；治理作为运行时组件（允许/改写/拦截）；给出可比较的指标（成功率、终止可靠性、契约合规率、遏制率） | 这就是"黑匣子"的学术原型，且明确说可做**确定性回放** |
| **Silent Failures in a Production Agent Runtime** (2606.14589) | 8 周 22 起事故；提出 **fail-plausible**：模型把错误包装成流畅可信的叙述交给用户；**70% 的静默失败靠人眼看输出发现**，测试/健康检查/治理审计全程绿；声明式治理**事前拦截率 0%，事后回归拦截率 87%**；存活最久的故障都在"组件之间的缝"里 | 你遇到的"牛头不对马嘴"就是 fail-plausible；治理的价值在**事后可查 + 回归**，不在事前 |
| **Communication Failures in MAS** (2610.32060) | 3,211 条失败轨迹；失败集中在**纠错/恢复**与**共识/决策**，而非首轮错误；建议强化证据校验、保留少数意见、收尾前做恢复检查 | 多 agent 的瓶颈是"证据与恢复"，不是"更会推理" |

关键处：**这三篇都在说同一件事——可验证性比智能更缺。** 而 2606.14589 那句话几乎是给我们写的：治理层"事后 87%、事前 0%"，说明**别指望事前预测，把力气放在"事后可重建 + 可回归"**。

---

## 3. 空白矩阵

| 能力 | Claude Code | Cursor | Codex CLI | OpenHands | Sonar / Palo Alto | **我们** |
|---|---|---|---|---|---|---|
| 工具调用留痕 | transcript | 无 | 日志 | trajectory | 组织级 | `audit.jsonl` 独立成册 ✅ |
| 变更证据（diff） | 有 | 有 | 有 | 有 | 有 | `internal/diff` ✅ |
| 审批归属（谁批的） | 会话内 | 无 | 策略 | 无 | 平台侧 | owner + 白名单 ✅ |
| 危险动作拦截 | shell 语义 | 无 | sandbox | sandbox | 平台侧 | `shellguard` ✅ |
| 隔离执行（分支） | checkpoint | 无 | sandbox | 容器 | 无 | `worktree` `/mad/<slug>` ✅ |
| **断言↔证据绑定** | ❌ | ❌ | ❌ | ❌ | 部分（代码质量） | ❌ |
| **确定性回放** | ❌ | ❌ | ❌ | 部分 | ❌ | ❌ |
| **失败归因（第几步/谁）** | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ |

**结论：前四行我们已经有料；后三行全世界都空着。** 这正是"原材料齐、成品没人做"的位置。

---

## 4. 三个候选方向，为什么选第一个

| 方向 | 卖点 | 为什么现在没人做 | 我们凭什么 | 风险 |
|---|---|---|---|---|
| **A. 可证明交付（黑匣子）** | 每次交付可验证/可回放/可归因 | 大厂都在做"更强的模型/更好的上下文"，没人做"单机 agent 的证据链"；组织级平台离开发者太远 | audit/diff/approval/worktree/事件全齐，只差"串起来" | 叙事偏"信任"，不直接变现；需要 demo 才立得住 |
| B. 异构 agent 编排插座（ACP 治理） | 把 Claude Code/Codex/OpenCode 当可治理工人 | 各家只想让人留在自己生态 | 已有 ACP + 端口 + 审批 | 生态博弈，别人可能不配合 |
| C. 本地守夜（调度 + 断点续跑） | 关机也能干活 | 云端 Routines 收费，本地没人做 | 已有 scheduler + checkpoint | 价值太窄，撑不起"一鸣惊人" |

**选 A。** 理由：B 受制于人，C 天花板低；A 是"我们已经有的东西换个组织方式"，且能直接回答市场那两个数字（40% 无法重建、76% 验证跟不上）。

---

## 5. 30 天 MVP：三个件 + 一个 demo

**① Claim → Evidence 绑定（治 fail-plausible）**
`agent.message` 里"我做完了"这类断言，必须挂 evidence id（`verify` 输出 / `diff` 哈希 / 测试结果）。
没有证据的断言在界面上灰显 + 标"未证实"。这不改模型，只改"能不能宣称"。
→ 直接对应 MAST 的 FM-3.2（无验证）与我们自己的 `verify.go`。

**② MAT 轨迹与回放（治"无法重建"）**
每次运行落 `runs/<id>/trace.jsonl`：消息、工具调用、审批（谁/何时/允许还是拒）、文件变更哈希、退出码。
新增 `mad replay <run>`：在**新的 worktree** 里按序重放只读+写操作，比对最终 diff 与 verify 结果，输出"可复现 / 不可复现 + 第一处偏差"。
→ 对应 2603.18096 的确定性重放。

**③ 最小契约集（治归因）**
五条机器可判定的 contract："改前必读"、"完成必 verify"、"删除必审批"、"越界必拒"、"审批必有归属"。
违规时 trace 直接定位第一违规步与责任人（哪个子 agent / 哪次审批）。
→ 对应 MAST 三类失败 + VerifyMAS 的"谁/何时"。

**Demo 脚本（这个方向能不能立住，全看这一段）**
挑一周真实提交，跑一次：
`把过去 7 天所有 agent 的改动列出来 → 每条给出：谁改的、证据是什么、验证过没有、能不能重放。`
能答出来，就是"市场 40% 答不出来"的那件事。

---

## 6. 指标（可证伪）

| 指标 | 现在 | 30 天后目标 |
|---|---|---|
| 无证据的"完成"断言占比 | 接近 100%（只有文字） | < 5% |
| 一次交付的可重建率（能说清谁/何时/改了什么/验证结果） | 无 | 100%（对我们自己的 run） |
| 失败归因定位（第几步、谁导致） | 靠人读日志 | 自动给出第一违规步 |
| 回放成功率（同环境重放一致） | 无此能力 | ≥ 80% 的只读+写任务 |

---

## 7. 别做什么（免得又变成重复劳动）

- 上下文压缩、缓存布局、工具并行：抄，别设计（半天到两天的事，不是护城河）；
- 图检索 / 语义索引：预算 8k + FTS5，收益远低于成本（前文已论证）；
- 组织级平台（代码质量扫描、云侧审计）：Sonar/Palo Alto 已经占了，我们做"单机这一次"的粒度才有差异；
- 新 UI 视图：证据链要的是**一页可读的 trace**，不是又一个看板。

---

## 出处

- MAST：<https://arxiv.org/pdf/2503.13657>
- Trace-Based Assurance Framework：<https://arxiv.org/html/2603.18096>
- Silent Failures in a Production Agent Runtime：<https://arxiv.org/html/2606.14589>
- Communication Failures in MAS：<https://arxiv.org/html/2610.32060>
- VerifyMAS / failure attribution：<https://arxiv.org/html/2605.17467>
- Palo Alto 2026 报告（经 TMCnet 报道）：<https://blog.tmcnet.com/blog/rich-tehrani/ai/two-in-five-companies-cant-reconstruct-what-their-ai-agents-did-palo-altos-data-says-the-audit-trail-is-the-missing-approve-button.html>
- Sonar 2027 State of Code（新闻稿）：<https://www.prnewswire.com/news-releases/sonar-survey-ai-makes-code-abundant-but-code-confidence-remains-scarce-302936034.html>
- Gartner MQ 企业级 AI Coding Agent（经报道）：<https://www.beri.net/article/gartner-magic-quadrant-enterprise-ai-coding-agents-cloud-giants-dethroned-2026>
- Agent 代码 provenance：<https://crashoverride.com/resources/knowledge-base/provenance/state-of-ai-agent-code-in-production>
