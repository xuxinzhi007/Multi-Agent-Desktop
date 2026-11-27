# 开放生态 CLI、工具箱复制与选中手动复制设计方案

**日期**: 2026-11-27

## 1. 设计原则

1. **主流 CLI 基础能力对齐（Claude Code / Codex / Cursor）+ 自有协同能力叠加**：
   - 基础交互、上下文可视化、斜杠命令、技能规范（`SKILL.md`）、项目规则（`AGENTS.md` / `CLAUDE.md`）、外部 MCP 发现全部遵循开源社区事实标准，零迁移心智负担。
   - 多智能体并行协同（`/agents`、`delegate` worktree 隔离）作为差异化增强层叠加其上。
2. **开放接口与零负担性能**：
   - Skill 仅在索引阶段读取 YAML Frontmatter（前 4KB），绝不将几十份技能全文塞进每轮上下文；仅在用户显式输入 `/<skill-name>` 或模型按需调用 `read_skill` 时加载单篇正文。
   - 外部 MCP 支持项目级 `.mcp.json`、用户级 `~/.multi-agent-desktop/mcp.json` 及生态标准 `~/.cursor/mcp.json`（仅合并本地 `stdio` 型 server 并按同名去重），避免封闭配置孤岛。

## 2. 四大模块设计

### 2.1 终端鼠标划选保留高亮 + 手动复制（`internal/clitui/vscroll.go` & `cli_tui.go`）
- **拖拽松手保留选区**：`MouseActionRelease` 仅结束 `selDragging = false`，**保持 `selActive = true` 蓝色高亮选区不消失**，且**不自动写入剪贴板**。
- **手动复制与清除**：
  - 当存在高亮选区（`m.vp.HasSelection()`）时：按 `Ctrl+C`、`y` 或 `Ctrl+Y` 将选中纯文本写入系统剪贴板 + OSC-52，状态栏提示 `✓ 已复制 (N 字)`；
  - 单击空白处（无拖拽移动）或按 `Esc` 清除高亮选区；无选区时 `Ctrl+C` 保持原有退出行为。
  - 状态栏在有选区时实时显示：`已选中 N 字 · 按 Ctrl+C / y 复制 · Esc 取消`。

### 2.2 工具箱独立副本 `/Users/xuxinzhi/Documents/gowork/tools/x2-logs-mcp`
- 在 `/Users/xuxinzhi/Documents/gowork/tools/x2-logs-mcp` 建立拥有独立 `go.mod`、`README.md`、完整单测的独立项目副本，并编译到 `~/bin/x2-logs-mcp`（对齐 `tools/Makefile` 约定），同步更新 `tools/README.md` 索引与 `~/.cursor/mcp.json`，同时保留 `multi-agent-desktop` 仓内实现。

### 2.3 多目录 `SKILL.md`、项目规则 `AGENTS.md`/`CLAUDE.md` 与开放 MCP 发现
- **`internal/skill` 扩展**：
  - 兼容扁平 `*.md` 与标准目录式 `<dir>/<name>/SKILL.md`，支持 YAML 单行及多行折叠（`>-` / `|`）`description` 解析。
  - 扫描优先级：工作区（`.mad/skills`、`.cursor/skills`、`.claude/skills`、`.agents/skills`）> 全局（应用 `skills` 目录、`~/.agents/skills`、`~/.cursor/skills`、`~/.claude/skills`、`MAD_SKILL_DIRS`），同名以高优先级覆盖。
- **项目规则加载**：`internal/agent/loop_backend.go` 自动读取工作区根目录 `AGENTS.md` 或 `CLAUDE.md`（安全截断 4000 字符上限，防超大文档撑爆上下文）。
- **开放 MCP 发现**：启动时自动合并应用内配置、工作区 `.mcp.json`、`~/.cursor/mcp.json` 中的 `stdio` MCP Server（同名不重复拉起，可通过 `MAD_DISABLE_EXTERNAL_MCP=1` 关闭）。

### 2.4 上下文仪表盘与主流 CLI 命令集（`cli_tui.go` & `internal/agent`）
- **底部状态栏实时上下文仪表盘**：实时计算并展示会话上下文占用 `ctx 14% (18.2k/128k)`（包含历史消息与本轮增量估算，60%/85% 阈值变色预警）。
- **对齐主流操作习惯**：
  - `!<shell命令>`：输入框以 `!` 开头直接在当前工作目录执行本地命令并输出到转录流，零 Token 消耗；
  - `Shift+Tab`：一键循环切换 `ask -> auto -> readonly` 权限模式（对齐 Claude Code 手感）；
  - 主流斜杠指令补齐：
    - `/context`：展示上下文窗口占用拆解（消息数、估算 Token、已加载规则文件、可用技能数、MCP 工具数）；
    - `/compact [重点]`：调用模型对当前长会话执行真实上下文摘要压缩（`mgr.CompactSession`）；
    - `/undo`：撤回上一轮对话消息（`mgr.UndoLastTurn`）；
    - `/diff`：查看当前 Git 工作区变更统计与 diff 摘要；
    - `/status`：查看工作区 Git 分支、模型、审批模式、MCP/Skill/Plugin 状态；
    - `/review [基准]`：对当前未提交或指定分支改动发起结构化代码审查；
    - `/init`：扫描当前仓库结构生成或完善 `AGENTS.md`；
    - `/skills [reload|<技能名>]` 及直接 `/<技能名> [补充说明]`：浏览或直接激活任意已安装技能；
    - `/mcp`：查看所有已连接 MCP Server 及工具列表；
    - `/doctor`：一键体检模型配置、本地端点、Git/Ripgrep、MCP 握手与目录权限；
    - `/cost` / `/export [路径]`：查看本轮/累计 Token 统计、导出会话 Markdown。

### 2.5 斜杠命令的检索、补全与执行契约（`internal/clitui/suggest.go` & `cli_tui.go`）
目标：命令数量随技能与插件增长后，仍能“输入即找到、回车不乱跑”，且新增指令只改数据不改分支。

- **分级匹配**（`MatchScore`）：精确 = 100、前缀 = 85、词首（`-`/`_` 后的片段）= 65、连续子串 = 50、子序列（`/cmp` → `/compact`）= 30~49。排序规则：强度 → 命令名长度 → 字典序；已有前缀级命中时丢弃纯子序列低分噪音（`/co` 不再拖出 `/doctor`）。
- **回车安全**：输入不是完整指令名时，Enter 只做补全（补全带参指令时自动补一个空格），补全后再次 Enter 才执行；输入已是完整指令（`/quit`、`/clear`）则一次 Enter 执行。避免 `/cl` 误触 `/clear`、`/q` 误触 `/quit`。
- **参数选择器登记表**：`slashPickerKinds` 声明“空参数时需要交互选择”的指令（当前 `/model`、`/resume`、`/mode`）；输入 `/cmd ` 即自动拉起选择器，新增交互式指令只需在表里加一行。
- **帮助页分组**：`slashCmd.Group` 把 32 条系统指令归入会话 / 上下文 / 工作区与代码 / 模型与权限 / 扩展与协作 / 帮助，技能与插件单独成组并按数量折叠；`/help` 输出使用动态命令表，含已发现技能与插件。
- **未知指令兜底**：`/compac` 这类近似输入会提示“是不是想用 /compact”，低置信度（低于词首级）则保持原文提示，不做生硬推荐。
- **列表行只放“来源 + 名字 + 一句短描述”**：参数签名（`[reload|<技能名>]`）不进列表行，只在底部“用法”行展示；技能/插件的长描述先折叠成一行再按列宽截断，来源用 4 列标签（`技能` / `插件`，内置留空）区分，不再在描述前拼 `[技能]`。
- **置信度决定高亮**（对齐 Claude Code 的 `Enter` 语义）：只有从命令名开头或词首命中时才高亮首项并允许补全；打错字/子序列命中时不高亮，`Enter` 直接提交原文，交给未知指令兜底，避免“猜错还替我执行”。
- **不放进指令表的东西**：`/cd` 已移除。会话中途改主工作目录会让上下文里的相对路径、`AGENTS.md` 规则、技能/插件目录全部错位，还要重扫插件、重建上下文统计；换目录的正解是**在目标目录下启动 cli**，想同时访问别的仓库用 `/add-dir`（挂载额外工作区根，不动主目录语境）。误输入 `/cd` 时给一条迁移说明，而不是笼统的“未知指令”。
- **同一份信息只留一个入口**：`/session` 并入 `/status`（附加根也挪过来了），`/cost` 并入 `/context`（窗口估算 + 累计 tok），误输入旧名给迁移说明。判断标准：一条指令要么**改变会话状态且口径一致**，要么**有真实副作用**（跑 git、写文件、读 MCP、剪贴板）；只是换个说法重复能力的（prompt 模板、快捷键别名）不进表。

### 2.6 三级补全：命令 → 参数 → 路径（`Suggest.mode`）
对齐 Claude Code / Gemini CLI / Cursor 的共同做法，一个面板按上下文切换三级：

| 触发 | 模式 | 候选来源 | 补全结果 |
|---|---|---|---|
| `/` + 字母 | `ModeCommand` | 系统指令 + 技能 + 插件 | `/name`（带参再补空格） |
| `/cmd ` + 字母（有枚举） | `ModeArg` | `slashArgItems` 登记表：`/skills`→技能名与 `reload`、`/plugins`→`reload` / `init`、`/mode`→`ask` / `auto` / `readonly`、`/logs`、`/paste`、`/add-dir`；静态表没有的命令走 `SetArgLister` 动态源（目前只有 `/resume` 列会话） | `/cmd arg`，保留命令前缀 |
| `/cmd ` + 自由文本（无枚举） | `ModeArgFree` | 没有候选，不占屏 | 原样保留，`Enter` 直接把参数带进执行 |
| 任意位置 `@` + 片段 | `ModePath` | 工作区文件/目录（`rg --files` 尊重 .gitignore，缺失时退回受限遍历，5s 缓存） | 只替换 `@` 之后的片段，保留前后正文；目录保留结尾 `/` |

**面板只留给“需要人做选择”的地方**（`Suggest.Visible`）：

| 场景 | 面板 | 回车 |
|---|---|---|
| `/` 还没敲到完整指令名 | 候选列表（可过滤、可↓↑） | 补全；无把握时提交原文走未知指令兜底 |
| 命令名已完整（`/cd`、`/help`） | 不出现 | 直接执行 |
| 二级参数有枚举（`/skills`、`/mode`、`/plugins`、`/resume`…） | 候选列表 | 空参数=执行；已命中=执行 |
| 二级参数是自由文本（`/compact 保留重点`、`/cd ../foo`） | 不出现 | 直接执行，参数原样带走 |
| `@` 片段 | 文件/目录列表 | 采用路径，再回车才发送 |

原则：**不需要你决策的地方不占屏**。命令名敲完整之后列表里只剩它自己，等于没说任何新东西；路径参数也不再做目录选择器——选文件/路径统一走 `@`，两个入口做同一件事只会增加学习成本（`SetArgLister` 仍保留，供 `/resume` 这类真有待选集合的命令使用）。

**参数只有两种形态**（同 Codex CLI / opencode / Crush 的做法）：能枚举的才给二级菜单，其余一律是自由文本——`/compact 保留接口改动`、`/cd ../foo`、`/技能名 任务说明` 都属于后者。面板在 `ModeArgFree` 下渲染三行：`参数 <已输入文本>`、`用法 <cmd> <签名>`、`Enter 执行`；不接受参数的指令显示“该指令不接受参数，回车直接执行”。

- **Enter 的唯一判据**（`Suggest.ExecutesOnEnter`）：命令名已完整（一级 exact）或参数已定稿（二级为空 / 已命中某个候选 / 自由文本）。满足就**执行**，不再补全。这条专门修掉“`/compact ` 带尾空格 → 补全值多一个空格 → 判不相等 → 回车把原文写回、界面毫无反应”的死键。
- **空参数=直接执行**：`/skills ` + `Enter` 是列出全部技能，而不是默默补成 `reload`；想补全按 `Tab`。
- **多词参数自动转自由文本**：`/skills win 再解释一下` 命中不到子项就转 `ModeArgFree`，不拿一块空列表挡路。
- **查无结果不静默**：单 token 匹配为空时留一行“没有匹配项，可继续输入过滤，或直接回车执行原命令”。
- **执行失败要说清**：技能出现在清单里但读不到 `SKILL.md` 时提示“试 /skills reload”，不再笼统报“未知指令”。
- **@ 只认词首**：`@` 必须位于行首或空格之后、片段内不含空白，邮箱 `a@b.com` 不会误触发。
- **参数不再靠方括号沟通**：`[reload|<技能名>]` 只在底部“用法”行提示，实际候选靠空格后的二级面板展开；`/mode` 因此从“空格即弹选择器”改为二级补全，`/model`、`/resume` 仍保留带状态徽标的选择器。
- **路径回车语义**：`Enter` 先采用路径，补全到实项后再次 `Enter` 才是发送消息（与 Claude Code 的 `@` 行为一致）。
