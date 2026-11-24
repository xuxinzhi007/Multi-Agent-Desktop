# 产品价值、竞争位置与成熟度评估

> 调研日期：2026-11-24
> 数据来源：GitHub 仓库页与 README 原文（当日 `curl` 抓取，星标数为抓取时实时值）；本仓库代码与运行记录（`~/Library/Application Support/multi-agent-desktop/loop/runs/`）；16 张真实界面截图（`npx playwright test e2e/visual-audit.spec.ts`，亮/暗各 8 个界面）。
> 置信度：⭐ 抓取到的一手事实｜🔶 我的推论，需你判断

---

## 0. 三句话结论

1. **它不是 demo。** 19k 行业务 Go + 12k 行测试 + 61 个 e2e，核心循环、审批、审计、worktree、飞书长连接都是真实现，前端没有一个「敬请期待」占位。
2. **但它现在是「作者本人能用的生产工具」，不是「能交给别人用的产品」。** 差的不是功能，是三件事：签名分发、密钥安全、失败可解释。
3. **它最值得继续做的理由不是「又一个多 agent 编码工具」，而是「飞书里的值班/排障 copilot」。** 这条赛道上，抓取的 14 个竞品没有一个做飞书。

---

## 1. 市场：这一层已经很挤，但挤的不是你在的那一格

### 1.1 ⭐ 当日抓取的真实数据

**通用编码 agent（CLI / SDK / IDE 插件）——不是你的对手，是你的上游**

| 项目 | ★ | 形态 |
|---|---|---|
| anomalyco/opencode（原 sst/opencode） | 228,879 | 开源编码 agent |
| anthropics/claude-code | 156,878 | CLI |
| openai/codex | 136,296 | CLI |
| google-gemini/gemini-cli | 109,042 | CLI |
| All-Hands-AI/OpenHands | 94,206 | 自主开发平台 |
| cline/cline | 71,860 | SDK / IDE 插件 / CLI |
| block/goose | 56,922 | 本地 agent |
| Kilo-Org/kilocode | 28,105 | IDE |
| RooCodeInc/Roo-Code | 24,236 | IDE 内的「AI 团队」 |

**多会话编排壳——这才是你真正的那一格**

| 项目 | ★ | 它做什么 | 与你的关系 |
|---|---|---|---|
| BloopAI/vibe-kanban | 28,799 | kanban 规划 + workspace 跑 agent + diff 内联评论 + **内置浏览器预览**；接 10+ 种 CLI | 最接近，且已产品化 |
| getAsterisk/claudia | 22,463 | Claude Code 的 GUI 工具箱、自定义 agent、后台 agent | 单后端 GUI |
| siteboon/claudecodeui | 14,744 | 把 Claude Code / Codex / Cursor CLI 搬到 web 和手机 | 远程访问层 |
| smtg-ai/claude-squad | 8,862 | 终端里管多个 Claude/Codex/Gemini/Aider，**每任务独立 git workspace** | 你的 worktree 思路一致 |
| omnara-ai/omnara | 3,444 | 自托管 managed agent 平台（Apache 2.0），API + 沙箱池 | 平台化方向 |
| stravu/crystal | 3,128 | 并行 Codex/Claude 会话 + git worktree | ⭐ **2026-02 已废弃，转为 Nimbalyst** |

**桌面 / GUI agent 与框架**

| 项目 | ★ |
|---|---|
| browser-use/browser-use | 121,906 |
| zed-industries/zed | 95,613 |
| microsoft/autogen | 61,967 |
| langchain-ai/langgraph | 45,346 |
| bytedance/UI-TARS-desktop | 39,592 |

### 1.2 ⭐ 三条从数据里读出来的事实

**① 编排壳这一层，全部是「包装别人的 CLI」。**
claude-squad、vibe-kanban、claudia、claudecodeui、crystal——README 里写得明明白白：管理 Claude Code / Codex / Gemini。它们的价值是**调度与评审体验**，模型能力与工具循环是借来的。

**② 最接近你的那个开源项目已经死了。**
Crystal（并行会话 + git worktree，桌面应用）2026 年 2 月标记废弃，团队转做 Nimbalyst。🔶 这说明「多会话 + worktree」本身不足以支撑一个独立产品——它太容易被上游 CLI 自己的 `agents` / `subagents` 功能吃掉（Claude Code、Cursor、Devin 都已经内置）。

**③ 内置浏览器不是差异点。**
vibe-kanban 已经有「built-in browser with devtools, inspect mode, device emulation」。🔶 但它的浏览器是给**你预览自己写的 app**；你的浏览器是给**agent 去操作别人的业务系统**（本次运行记录里就是 agent 在 devops 日志平台点筛选、查 SLS）。这两个用途完全不同，后者竞品里没人做。

---

## 2. 你的项目真正的差异化在哪（对着代码说，不是对着愿景说）

| # | 能力 | 代码位置 | 竞品状况 |
|---|---|---|---|
| 1 | **飞书多机器人长连接**：群内 @ 回答、卡片流式回写、用户身份读群历史、权限逐项自检 | `internal/feishu/*`（约 5000 行） | ⭐ 抓取的 14 个项目**无一支持飞书** |
| 2 | **进程内可治理循环**：停止 / 预算 / 检查点续跑 / 写前 diff 预览 / 审批 / `audit.jsonl` / 危险命令前置拦截 | `internal/loop`、`internal/tool`、`internal/shellguard`、`internal/approval` | 编排壳把执行交给 CLI，这些控制点拿不回来 |
| 3 | **agent 操作业务系统**：内置浏览器 + `web` 工具（控件表、steps、弹层优先） | `internal/browser`、`internal/tool/web` | vibe-kanban 的浏览器是预览自家 app，方向不同 |
| 4 | **单 Go 进程跨平台打包** | MyGo，`docs/design.md` §2.1 | 竞品普遍 Electron / Tauri |
| 5 | 知识库 + 学习摘要 + 父子派活（深度 2、名额 8） | `internal/knowledge`、`internal/agent/delegate.go` | Cursor / Claude Code 已有类似能力，不构成差异 |

🔶 **结论：#1 + #2 + #3 的组合是别人短期内不会做的。** 不是因为技术难，而是因为市场不重叠——国外产品不做飞书，国内 IM 机器人不做「可治理的本地执行 + 代码库访问」。

---

## 3. 用户为什么用它（用你自己的运行记录回答，不是设想）

本次从 `loop/runs/agent-1794468530762089000.json` 读到的真实使用链路：

```
飞书群里同事问：job-dduxcvryl6qc2roe 在任务管理里查不到
  → agent 打开 devops 日志平台，切生产环境、填关键词、拉时间范围
  → 同时 grep 本地 data-service 代码，定位 baseJobListQuery 的三条租户分支
  → 得出结论：job_source=PLATFORM 且未打 qc_tenant_id，两条视图 SQL 必然过滤掉它
  → save_note 落进知识库
```

🔶 这条链路里，用户得到的价值是**「跨系统排障」：IM 提问 → 日志平台取证 → 代码库验证 → 结论回到 IM**。

- 单独用 Claude Code：能读代码，但进不了需要飞书登录的日志平台，也回不到群里。
- 单独用飞书机器人：能回话，但没有代码库和浏览器。
- 用 vibe-kanban / claude-squad：能并行跑 CLI，但没有 IM 入口，也不面向「值班」。

**这就是用户用它的理由，也是你该写进 README 第一行的定位。** 现在 README 和 `design.md` 写的是「多 Agent 开发工具桌面程序」——把自己放进了最挤的那一格。

---

## 4. 你该继续维护它的理由（以及不该的理由）

### 该继续（🔶 我的判断）

1. **它是你工作流的真实基础设施，不是玩具。** 运行记录里是真生产问题、真结论、真知识库沉淀。停掉它的成本立刻发生。
2. **差异化落在别人不会来的地方。** 飞书 + 内网业务系统 + 本地代码库，这个三角国外产品不会做。
3. **沉没的不是代码，是约束。** `shellguard`、审批、审计、worktree 隔离、飞书权限矩阵——这些是踩坑换来的，重写一遍要再踩一遍。
4. **可迁移性高。** 循环、工具治理、浏览器自动化这三块与飞书解耦，换 IM（企微/钉钉）只是换一个 `internal/feishu` 同构包。

### 不该继续投入的方向（🔶 逆耳但必要）

1. **不要和 CLI 编排壳正面竞争。** Crystal 已死，vibe-kanban 28.8k★ 且接 10+ 后端。你在「并行跑 Claude Code」这件事上没有胜算。
2. **不要再加通用编码能力。** 上游 CLI 每月都在变强，你追不上也不必追。
3. **不要做「关系图 / 灵动岛」这类形态创新。** `research-product-forms.md` 已经取证：6 个成熟产品无一采用常驻小浮窗承载并发状态。

---

## 5. 成熟度评分卡

| 维度 | 状态 | 证据 |
|---|---|---|
| 核心执行循环 | 🟢 成熟 | 停止/预算/检查点/插话/防打转，`internal/loop` 全覆盖测试 |
| 工具治理与安全 | 🟢 成熟 | schema 校验、幂等去重、写前 diff、审批、`shellguard`、`audit.jsonl` |
| 多会话与派活 | 🟢 成熟 | 队列+worker、父子、worktree、名额与深度限制 |
| 飞书集成 | 🟢 成熟 | 多机器人、双身份、权限自检、卡片回写、房间只读 |
| 前端完整度 | 🟢 无占位 | `src` 全量 grep 无 TODO / 敬请期待 / 未实现 |
| 自动化验证 | 🟢 良好 | 61 个 e2e + 12k 行 Go 测试 |
| **失败可解释** | 🟡 刚补齐 | 本次修：终止理由带真实错误；参数错误不再判死；`tools/runs.py` 可自查 |
| **视觉与可读性** | 🟡 刚补齐 | 本次修：字号刻度、失效 token、氛围层、阅读宽度（见 §6） |
| **密钥安全** | 🔴 未做 | `config.json` / `agents.json` 明文存 API Key 与飞书 Secret（仅 600 权限） |
| **分发** | 🔴 未做 | 未签名，Gatekeeper / SmartScreen 拦截；开发版 ad-hoc 签名导致钥匙串反复弹窗 |
| 存储 | 🟡 已知债 | 全量 JSON，非 SQLite；会话多了会有写放大 |
| shell 体验 | 🟡 已知债 | 手动命令不流式、无超时 |
| 检查器 | 🟡 缩水 | 只有工具/知识两页，无 diff / 任务页 |
| 内置浏览器兼容 | 🟡 结构性 | WebKit 引擎，Chrome-only 特性天然缺失，只能标注不能消除 |

### 判定

**不是 demo，也不是半成品——是「单人可用的生产工具」（alpha 尾 / beta 头）。**

要成为「能交给同事用」的产品，按优先级只差三件事：

1. **签名与分发**（否则别人装不上，你自己每天被钥匙串弹窗打断）
2. **密钥进钥匙串**（否则不敢把配置给别人看，也不敢多机同步）
3. **失败可解释**（今天已补一半：还需要把 `runs.py` 的能力做进界面）

---

## 6. 本次视觉与体验改动（已落地，附前后证据）

方法：`e2e/visual-audit.spec.ts` 用 mock 数据渲染 8 个真实界面 × 亮/暗 = 16 张截图，逐张人眼过。**不是凭感觉改 CSS。**

### 6.1 找到的是 bug，不是审美分歧

| 问题 | 根因 | 修法 |
|---|---|---|
| **用户消息气泡完全透明** | CSS 写了 `color-mix(in srgb, var(--accent) 12%, …)`，但 token 名是 `--accent-primary`，`--accent` 从未定义 → 整条声明失效 | 补 `--accent` / `--accent-soft` 别名，6 处失效规则一起复活 |
| 诊断面板用 off-palette 红绿 | 引用未定义的 `--c-danger` / `--c-success`，回落到硬编码 `#ff5a7a` / `#34d399` | 改用 `--danger` / `--success` |
| **e2e 把 bug 当成了规范** | `message-interaction.spec.ts` 断言用户气泡背景必须 `rgba(0,0,0,0)` | 改为断言气泡有色，并注释说明来历 |
| 版本号是假的 | 前端写死 `'0.3.0-mygo'`，与打包版本无关 | `SystemInfo.Version` 取 `mygo.App.Version()`，前端启动时拉取 |

> ⭐ 全量 token 校验：90 个已定义、78 个被引用，其中 **7 个引用不存在**。这类错误不会报错，只会静默变成透明/默认值——正是「看着拉胯」的隐形来源。

### 6.2 可读性

- **字号刻度整体上调**：`10.5/11.5/12.5/13.5/15/18` → `11/12/13/14/16/20`。桌面正文不低于 13px，次要信息不低于 11px。
- 清掉 **20 处 CSS 硬编码 9/9.5/10/11px** 与 **35 处 TSX 内联小字号**，全部走 token。
- 亮色氛围层收敛：网格 `0.045→0.016`、光斑 `0.72→0.16`、暗角去掉。之前白底被染成蓝紫渐变，是「脏」的主因。
- 单聊、频道、飞书三个视图统一 **820px 阅读宽度**（此前只有单聊有）。

### 6.3 信息密度与噪音

- 舰队卡片**高度跟着内容**，不再被拉成整列高的四个空箱子；卡面改中性色，身份色只留顶部色条与头像。
- 空闲卡片不再摆「当前任务 / 还没有任务」两个空标题，直接说下一步。
- 侧栏删除 `×` 改为**悬停/聚焦才出现**，不再每行挂一个危险按钮。
- 侧栏 `AGENTS` → `会话`；`0 个已选模型` → `未配模型`；版本号不再折行。

### 6.4 可访问性

- 侧栏会话/频道/飞书三类行原本是 `div onClick`，**键盘完全不可达**。补 `role="button"` / `tabIndex` / `aria-current` / `aria-label` / Enter·Space 处理。

### 6.5 验证

`npm run typecheck` ✅｜`go build ./...` ✅｜`go vet ./...` ✅｜`go test ./internal/...` ✅｜`npx playwright test` **61 passed** ✅

---

## 7. 建议的下一步（按投入产出排序）

| 优先级 | 事项 | 理由 |
|---|---|---|
| P0 | 固定代码签名身份（`tools/dev-sign.sh` 已备）+ 打包签名 | 每天被打断的钥匙串弹窗；别人装不上 |
| P0 | API Key / 飞书 Secret 进系统钥匙串 | 唯一的硬安全债 |
| P1 | 把 `tools/runs.py` 做进界面：失败原因、失败工具、参数一屏可见 | 「不清楚怎么查」这个问题应该由产品回答，不是靠命令行 |
| P1 | 子会话模型上下文预检（本次记录里 26677 > 4096 直接 400） | 派活必然失败的场景应提前拦住 |
| P2 | README / design.md 改定位：**飞书里的值班排障 copilot** | 现在的定位把自己放进最挤的赛道 |
| P2 | 历史迁 SQLite；shell 流式 | 已知债，不影响当前可用性 |
| — | 不做：CLI 编排壳竞品对标的功能 | Crystal 已废弃，赛道不成立 |

---

## 附：本次抓取来源

GitHub 仓库页（星标、描述、归档状态）与 README 原文，2026-11-24 抓取：
`anomalyco/opencode`、`anthropics/claude-code`、`openai/codex`、`google-gemini/gemini-cli`、`All-Hands-AI/OpenHands`、`cline/cline`、`block/goose`、`Kilo-Org/kilocode`、`RooCodeInc/Roo-Code`、`BloopAI/vibe-kanban`、`getAsterisk/claudia`、`siteboon/claudecodeui`、`smtg-ai/claude-squad`、`omnara-ai/omnara`、`stravu/crystal`、`browser-use/browser-use`、`zed-industries/zed`、`microsoft/autogen`、`langchain-ai/langgraph`、`bytedance/UI-TARS-desktop`、`e2b-dev/fragments`、`voideditor/void`、`phodal/routa`。

本仓库内相关一手调研：`docs/reviews/research-product-forms.md`（Claude Code / Cursor / Devin / Warp / Raycast / Linear / Apple HIG 官方文档原文）、`research-multi-agent-orchestration.md`、`research-embedded-browser.md`。