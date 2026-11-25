# 后端交付交接单（CI · 审批 · 定位 · 并发治理）

> 整理日期：2026-11-26
> 来源：`ci-positioning-approval-review.md`（总纲）+ `ci-commit-hygiene-plan.md`（CI 详案）+ 频道体系历轮运行沉淀。
> 口径：本单是**待后端/基础设施实施**的清单。前端能热更的部分已落地（见 §10），其余在此转交。
> 红线：线上业务运行期间严禁擅自重启承载业务的后端进程；下列每项标注是否需重启，合并窗口发布。

## 0. 接手人先读这三句

1. **先拍板 §11 的四件事**（ContextLimit 4096/8192、产品定位 A/B、审批身份策略、bash 是否高危），否则 CI 与审批方案无法开工。
2. **P0-1 是直接越权安全洞**（飞书群里任何人能点「允许」放开本机文件写入/命令执行），优先级最高，前端文案改动**不替代**它。
3. CI 详案已在 `ci-commit-hygiene-plan.md`，本单 §1 只留结论与阻塞项，不重复。

---

## 1. CI 与提交卫生（基础设施，需重启窗口外即可做）

详案见 `ci-commit-hygiene-plan.md`。结论三句：
- 门禁脚本已存在（`package.json:verify`），缺自动触发（无 `.github/`、`.githooks/`）。
- **接 CI 会立刻红**：`internal/llm/TestContextLimitUsesWindowOrProvider` 期望 4096、实现返回 8192（`compact_test.go:10` vs `compact.go:15`）。**这是决策不是改测试**——先看 `git log -S "return 8192" -- internal/llm/compact.go`（命中 `01e84f3`、`e7b89ec`）判断意图。
- 全量 2m46s（agent 165s / llm 127s），pre-push 只跑 13 个快速包，全量留 CI；`go test` 用显式包路径避开 `tmp-gp-context-lab/`。

交付物：`.github/workflows/ci.yml`、`.githooks/pre-commit`、`.githooks/pre-push`、`package.json:verify:fast/full`、装 oxlint、23 条改动切 4 提交。验收含两条**反向验证**（删 shellguard 规则 pre-push 红、gofmt 不合规 pre-commit 拦）。
是否需重启：否（纯仓库配置）。阻塞：ContextLimit 决策。

## 2. 审批安全与归属（后端，需重启）

总纲 §3。不是「子会话卡没显示」一个 bug，是审批模型缺三字段长出五类问题。

| 编号 | 问题 | 根因（文件:符号） | 改法 |
|---|---|---|---|
| **P0-1** | 谁能点批准无校验，群成员可越权放行 | `internal/feishu/service.go:798 decide(cfg,Turn)` 只取 ActionID，`Turn.Sender` 未参与判断 | 卡片回调拿 Sender 比对会话 owner/白名单，不匹配拒绝并回权限提示 |
| **P0-2** | 子会话卡飞书静默消失 | `Ask` 遍历 `s.runs` 条件 `cur.agent==v.AgentID`，delegate/Supervisor 子会话不被认领 | `ResolveApprovalRoute(agentID)` 沿 ParentID 上溯≤3 层找 run；找不到**降级**（写审计+主会话发非阻塞文本+app 卡高亮），不静默 |
| **P0-3** | 必然超时 | `approval.Center` 固定 5min，Supervisor 子跑 10min/父等 20min | 超时跟随运行剩余时间，子会话对齐 20min |
| **P1-4** | 授权记忆粒度错位，bash 一次批准永久放行 | `allowed map[agentID]map[toolName]`，父批子不继承 | 记忆沿父链继承；**bash 永不参与 remember** |
| **P1-5** | 审计答不出谁批的 | `audit.jsonl` 无批准人/来源/等待时长 | 补 `decided_by`/`origin`/`latency_ms` |

结构：`ApprovalView` 补 `RootAgentID`/`Origin`(app/feishu/chat)/`DecidedBy`（现仅 AgentID）。
验收：总纲 §3.4 八条均可写 e2e（子会话卡标注、降级不静默、10min 不超时、非 owner 拒绝、bash 二次仍弹卡、write_file 继承/bash 不继承、桌面处理后飞书收起、审计含批准人来源）。
前端对接点：§10 已用 parentId 链做归属显示近似；后端补 RootAgentID/Origin 后前端改读字段。
是否需重启：是。

## 3. 默认追问「是否开始」（后端，需重启）

总纲 §5-A。代码无硬编码，是模型生成。`loopSystemPrompt` 第 4 条只禁过程句没禁结尾追问。
- 软约束：system 加「不要以『是否开始』结尾，能做的直接做」。
- 硬约束（更适合无人值守，近 Claude Code）：loop 迭代边界判「本轮只有文字、无工具调用、以问句结尾」→ 自动插 system 提示继续跑。
定位：复现那轮看 `agent.tool` 是否为空（空=纯聊天停下等确认）。
是否需重启：是。

## 4. 消息时间顺序异常（后端，需重启）

总纲 §5-B。`AgentView` 按 `rawMessages` 原序渲染不排序；`Sync` 每轮用 `out.Messages` 整体覆盖 `a.Messages`，检查点续跑若 `out.Messages` 比历史短则覆盖错序。
改法：Sync 改为按消息 ID/时间戳合并而非整体覆盖；续跑时校验前缀一致。**前端不做时间戳排序**（会打乱 tool/assistant 配对）。
前置：先重新构建工作区版本再复现（截图对应 HEAD，与工作区有差）。
是否需重启：是。

## 5. 上下文场景隔离（后端，需重启）

频道历轮 backend-handoff A 项。根因：`loop_backend.go:executeLoop` 群回合 history=`a.Messages`(私聊全量)+transcript；`doubao_backend.go:40-50` 同；检查点 `runID=a.ID` 可复活远古状态。
改法：群回合 history 只用 role/system+该群 transcript+本条，不拼私聊；检查点键 `agentID+sceneID`；learn/domainBrief 摘要保留。
不变量：chat-isolation 已 DONE（群内容不进私聊时间线，`TestGroupReplyStaysOutOfPrivate` 守着），不得违反。
是否需重启：是。

## 6. 频道成员管理 + 群主/管理 + 邀请（后端，需重启）

backend-handoff B 项。`channel.Manager` 无成员更新方法；`Channel` 无角色字段。
改法：`SetMembers`/`InviteToChannel`/`KickFromChannel` RPC；`Channel.Owner/Admins`(omitempty)；权限判定 Owner/Admins 可踢、成员 agent 可邀；agent 工具 `invite_to_channel`；`mygo generate` 后前端接灰按钮。
是否需重启：是。

## 7. @所有人 防刷屏意图路由（后端，需重启）

backend-handoff C 项 + 总纲 §4 Progent 借鉴。`postChannel` 对短确认类（@所有人 且正文≤20 字/问候模式）prompt 追加「一句话回应≤15 字」（总纲收紧到 15）；复杂任务 planner 统筹为候选 3 后议。
是否需重启：是。

## 8. 精确抢占 / 单点重试 / 回合状态事件（后端，需重启）

viewport 轮 §8。前端现用 `agent.stop` 近似抢占（会清队列含私聊）、@重发近似重试（多一条用户消息）。
改法：按 scene 取消只断群回合+落「已中断」标记消息；按 delivery 重投不新增用户消息；`agent.status` 带 channelId 的 queued/started/ended 事件替换前端推断。
是否需重启：是。

## 9. 产品定位决策（待拍板，非代码）

总纲 §2。A 本机多 agent 工作台 vs B 飞书值班 copilot。建议「A 为主、B 只做三件事（发任务/看进度/批写操作）」，判定线「下季度飞书功能增量不超本机」。反方：若用户主要在手机上则应直接做 B。**只有用户能答**。结论写进 `design.md` 删「待决策」段。

## 10. 前端已落地（本轮及历轮，热更，无需后端）

- 审批卡归属显示 + 「本子会话允许」文案（本轮，ApprovalModal 用 parentId 链近似；**不替代 P0-1 安全校验**）。
- 飞书房间 post 类机器人消息 markdown 渲染（FeishuRoomView）。
- 频道：行内 @ 胶囊（TipTap，全员不截断、弹层上移可滚）、agent 消息 markdown+卡片+复制/转派、Smart Scroll、停止收口输入区+Esc、≥3 并发限高折叠、送达汇总、状态行生命周期收敛、@所有人 成本提醒、成员面板/个人主页/权限入口/全员只读、评审类创建即只读。
- 单聊：ChannelStrip 频道入口、权限图标⚡。

**前端待 CI 后再做（总纲 §6，高风险，CI 前置）**：7 套 per-agent map 收成 `AgentRuntime`、事件桥收表、`AgentView` 拆层。本轮不做。

**查无/待复现**：总纲 §5-C「知识库（1条命中）」徽章在当前工作区代码 grep 无此渲染（文档自述截图对应 HEAD 与工作区有差），需重新构建复现后再定是否改「仅打开知识面板时显示」。

## 11. 需用户拍板四件事（阻塞开工）

1. ContextLimit 4096 还是 8192？（决定测试改哪边）
2. 定位收窄 A 还是 B，或「A 为主 B 三件事」？
3. 审批身份策略：飞书卡仅会话 owner 可点，还是显式白名单？群场景保留「@ 机器人的人」作批准人？
4. bash 是否单列高危：永不 remember、默认不进 auto 自动放行？

## 12. 排期（总纲 §6）

红灯→门禁→提交切分（本周）→ 审批 P0-1/P0-2/P0-3（下周）→ 前端 Runtime 收口+事件桥（再下周）→ AgentView/manager 拆层（之后，CI 护栏就位）→ 定位决策会（并行）。

## 出处索引

- 总纲：`docs/reviews/ci-positioning-approval-review.md`
- CI 详案：`docs/reviews/ci-commit-hygiene-plan.md`
- 频道历轮后端设计：运行记录 `specs/backend-handoff.md`、`specs/channel-live-ux/backend-design.md`
- 论文：MAST(2503.13657) 失败模式当 e2e 靶子；Progent(2504.11703) 规则带参数+单调收窄；CaMeL(2503.18813) 工具输出标数据来源、不可信来源动写操作强制提级；AgentDojo 当安全 regression 基准。