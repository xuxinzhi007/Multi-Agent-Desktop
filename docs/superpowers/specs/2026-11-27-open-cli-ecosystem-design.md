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
