# CLI 产品化重构设计方案（主对话中心化 · 交互式选择器 · 可执行插件 · ASCII 欢迎动效）

- **日期**：2026-11-27
- **状态**：待评审（Spec Ready）
- **目标模块**：`cli_tui.go`、`headless.go`、`internal/clitui/`、`internal/plugin/`（新增）、`internal/agent/`（子 Agent 事件透传与工具挂载）

---

## 1. 背景与当前痛点诊断

基于真实终端使用截图与源码审计，当前 CLI（`-headless` 终端模式）仍带有浓厚的「桌面多会话调试壳」痕迹，距离 Claude Code、OpenAI Codex CLI、Pi 等日常好用的主力工具存在 5 个核心断层：

1. **会话心智偏移（独立会话 vs 主对话 + 子 Agent）**：
   - 现状：每次进入终端创建 `Agent 25`、`Agent 26` 等平级独立会话；用户感知不到“当前终端就是我的唯一主工作台，其他 Agent 是帮我干活的子智能体”。
   - 底层事实：`internal/agent/delegate.go` 已经实现了完整的 `read`（轻量同进程只读子任务）与 `write`（独立 git worktree 并发子任务）派活内核，但 CLI 把它藏在了后台，且过滤掉了非当前 `agentID` 的子任务实时事件（`v.ID != m.agentID` 直接丢弃）。
2. **反人类的“纯文本打印 + 盲输序号”交互**：
   - 现状：输入 `/model` 只往聊天流打印一段静态文本（`1 llamacpp / 2 deepseek / 3 qoder`），用户在输入框敲 `3` 会被当成普通聊天消息发给模型；且 `qoder`、`deepseek` 连接未填模型名时直接报错 `qoder 这条连接没有填模型，先补一个 (-m qoder/<模型>)`，而不会利用已有的 `llm.ListModelMetas` / `QoderModelNames` 自动展开模型目录。
   - 默认连接陷阱：当持久化的第一个 Provider 是未启动的本地 `llamacpp`（`127.0.0.1:8080`）时，启动不做快速连通性探测，首条消息必然撞上生硬的 `dial tcp 127.0.0.1:8080: connect: connection refused`。
3. **底栏（Status Footer）拥挤截断，报错不可恢复**：
   - 现状：单行硬塞长路径、`Agent 25`、超长 `.gguf` 文件名、`[auto]`、截断的 `er...` 以及快捷键说明；连接失败时只吐原始 Go `net/http` 错误堆栈，没有人类可读诊断与一键切模型引导。
4. **`/` 命令写死在 `switch-case`，不支持外部插件扩展**：
   - 现状：`cliSlashCommands()` 与 `execSlash()` 硬编码了 13 个内置命令，其他人使用或开发者自己写脚本扩展命令/工具时无处挂载。
5. **缺少品牌感与开发趣味的启动欢迎界面**：
   - 现状：进入 TUI 只有两行灰字，缺少像主流 CLI 一样兼具辨识度与环境自检概览的 ASCII 启动欢迎卡片。

---

## 2. 总体架构路线

采用 **「主会话中心化 + 统一交互式 Picker 浮层 + JSON-Stdio 可执行插件宿主」** 架构，在不破坏桌面端共享 `agent.Manager` 的前提下，分 3 个阶段平滑落地：

- **阶段 1（核心体验与架构纠偏）**：主对话 + 子 Agent 实时协作树、通用交互式选择器 `clitui.Picker`（解决 `/model` 二级选模型、`/mode`、`/resume`）、启动本地端口快速自检回退、双层清爽底栏与友好错误诊断。
- **阶段 2（开放插件生态）**：新增 `internal/plugin` 可执行插件宿主（扫描全局 `~/.multi-agent-desktop/plugins/` 与项目级 `.mad/plugins/`），通过 `plugin.json` + `stdin/stdout JSON` 协议同时向 CLI 注入自定义 `/命令` 与向主 Agent 注入 `tool.Tool`。
- **阶段 3（视觉品牌打磨）**：基于用户「呦，写BUG呢」蘑菇头头像定制纯经典 ASCII 字符画欢迎卡 `clitui.Banner`，支持启动逐行扫描入场 + 眨眼微动效 + 右侧工作区/模型/插件就绪概览（窄终端自动降级，按键零阻塞）。

---

## 3. 核心模块详细设计

### 3.1 模块一：主对话（Lead Session）+ 子 Agent 实时协作展示

1. **主会话收敛与去编号化**：
   - CLI 启动默认创建或恢复当前工作目录（`cwd`）绑定的**主工作区会话**，界面角色统一显示为仓库名或 `Main`（不再向用户暴露 `Agent 25`、`Agent 26` 这类内部序号）。
   - `/resume` 仅切换历史主会话（过滤 `ParentID != ""` 的派生子会话，避免把内部打工的子会话混进会话恢复列表）。
2. **内嵌子 Agent 实时协作树（Sub-agent Tree）**：
   - 后端事件关联：检查并确保 `delegate` 派生的 `read` / `write` 子任务在发出 `agent.loop`、`agent.tool`、`agent.delta`、`agent.status` 事件时携带 `parentId`（或在 `Manager` 中通过 `ParentID == m.agentID` 识别归属）。
   - TUI 渲染（新增 `cellSubAgent` 或聚合卡片）：当主会话调用 `delegate` 派生子任务时，在主对话流内渲染实时折叠树，无需切走窗口：
     ```text
     ⏺ Parallel Sub-agents (2 running · 4.2s)
       ├─ ⠙ [代码检索] Search "RunPrompt" in internal/agent…
       └─ ✓ [单测验证] Bash go test ./internal/clitui/... (0.8s)
     ```
   - 新增 `/agents` 指令（及快捷键入口）：随时查看当前主对话下活跃/已完成的子 Agent 列表及其隔离分支/摘要。

### 3.2 模块二：通用交互式选择器 `clitui.Picker`、无缝切模型与底栏重构

1. **通用键盘选择器组件 `internal/clitui/picker.go`**：
   - 浮层定位于输入框正上方（与 `ApprovalBar` / `Suggest` 同槽位，严格遵守 `View()` 行数 `== m.height` 不变量）。
   - 支持：`↑/↓`（及 `Ctrl+P/Ctrl+N`）移动高亮、实时字符模糊过滤、`Enter` 确认、`Esc` 取消返回上一级或关闭。
2. **二级模型选择器（`/model` 或快捷键 `Ctrl+K`）**：
   - **第一级（选择提供商连接）**：列出已配置的提供商（如 `qoder`、`deepseek`、`llamacpp`），实时标注状态（`● 当前使用`、`✓ 已配置 Key`、`⚠ 本地 8080 未启动`）。
   - **第二级（选择具体模型）**：
     - 对 `qoder`：立即展示 `llm.ListModelMetas` / `QoderModelNames()`（`Auto`、`Ultimate`、`Performance`、`DeepSeek-V4-Pro`、`Qwen3.8-Max`、`GLM-5.2` 等）。
     - 对 `deepseek` / `openai` / `ollama` / `doubao-cli`：异步拉取 `llm.ListModelMetas` 并提供常用预设兜底（如 `deepseek-chat`、`deepseek-reasoner`），彻底消灭“这条连接没有填模型，先补一个 (-m ...)”的报错死胡同。
   - 选中回车后立即调用 `mgr.SetAgentLLM(m.agentID, cfg)` 落盘生效，并自动更新底栏。
3. **启动连通性快速自检与友好错误卡片**：
   - `ensureCLIAgent` 启动选默认模型时：若候选是本地 `llamacpp` / `ollama`，用 `80ms` 超时快速 `net.DialTimeout` 探测端口；若本地服务未开，自动回退到已配置好 Key 的云端 Provider（如 `qoder` 或 `deepseek`），并在欢迎区温和提示。
   - 当运行期发生网络/鉴权错误时，`renderCell(cellError)` 将原始错误转译为结构化提示（例如：`连接本地模型 127.0.0.1:8080 失败（服务未启动） → 输入 /model 或按 Ctrl+K 切换可用模型`）。
4. **双层清爽底栏**：
   - 输入框上方活动条（仅运行中出现）：动态阶段 + 子 Agent 进度 + 实时秒表。
   - 最底状态栏（单行左右对齐、零截断乱码）：
     - 左侧：`~/…/multi-agent-desktop  ·  qoder/Auto  ·  auto`（当有活跃子 Agent 时显示 `· 2 sub-agents`，有 token 消耗时显示紧凑 `1.2k tok`；彻底移除 `er...` 这类无意义状态截断）。
     - 右侧：`/ 指令  ^K 模型  ^T 思考  ^O 详情`（运行中切换为 `Esc 停止  ^T 思考  ^O 详情`）。

### 3.3 模块三：可执行插件体系 `internal/plugin`（`plugin.json` + JSON-Stdio）

面向“给其他人用、任何人用任意语言（Bash / Python / Go / Node）5 分钟写一个插件”设计：

1. **插件发现目录（双层优先级，项目级同名覆盖全局级）**：
   - 用户全局：`~/.multi-agent-desktop/plugins/<plugin-name>/plugin.json`
   - 项目本地：`<cwd>/.mad/plugins/<plugin-name>/plugin.json`（团队可随 Git 仓库共享项目专属命令与工具）
2. **清单格式 `plugin.json`**：
   ```json
   {
     "name": "git-helper",
     "version": "0.1.0",
     "description": "团队常用 Git 检查与提交辅助",
     "commands": [
       {
         "name": "pr-check",
         "args": "[base-branch]",
         "hint": "检查当前分支与目标分支的变更风险",
         "run": "./bin/pr-check.sh",
         "mode": "output"
       }
     ],
     "tools": [
       {
         "name": "jira_lookup",
         "description": "根据单号查询内部需求详情",
         "parameters": {
           "type": "object",
           "properties": {
             "key": { "type": "string", "description": "单号，如 PROJ-123" }
           },
           "required": ["key"]
         },
         "run": "./bin/jira-lookup.py"
       }
     ]
   }
   ```
   - `commands[].mode` 支持两种常见用法：
     - `"output"`（默认）：在终端执行插件并在对话流展示输出（或失败报错）。
     - `"prompt"`：插件脚本生成的 `prompt` 字段直接作为结构化上下文发给主 Agent 继续推理（非常适合做 `/review`、`/commit` 类智能插件）。
3. **JSON-Stdio 进程通信协议**：
   - **Stdin 输入（JSON）**：
     ```json
     {
       "kind": "command",
       "name": "pr-check",
       "rawArgs": "main",
       "cwd": "/path/to/repo",
       "agentId": "agent-xxx",
       "toolInput": {}
     }
     ```
   - **Stdout 输出**：
     - 若脚本输出合法 JSON `{"output": "...", "prompt": "...", "error": "..."}`，按字段结构化消费；
     - **零门槛兼容**：若脚本直接打印普通纯文本（非 JSON），自动将其 `stdout` 当作 `output`（或 `tool.Result.Content`）消费，`exitCode != 0` 时读取 `stderr` 作为错误！这样哪怕一行 `echo` shell 脚本也能直接当插件用。
4. **安全与管控**：
   - 继承 `ctx` 超时取消（默认 30s，可在 `plugin.json` 配 `timeoutSec`），按 `Esc` 可随时中断正在跑的插件进程。
   - 插件注册的 `tools` 默认纳入现有 `approval` 审批中心保护（`ask` 模式下外部插件写动作同样受控）。
   - 内置 `/plugins` 命令：一键查看已加载插件列表、来源路径、`reload` 热重载插件目录、以及 `init <name>` 在 `.mad/plugins/<name>/` 下生成带示例脚本的脚手架！

### 3.4 模块四：「呦，写BUG呢」纯经典 ASCII 启动动画与欢迎面板

1. **视觉布局（`internal/clitui/banner.go`）**：
   - 将用户提供的黑白蘑菇头表情包提炼为高辨识度、高对比度的**纯经典 ASCII 字符画**（仅使用标准可打印 ASCII 字符如 `@ # % * + = - : .`，100% 兼容 macOS Terminal、iTerm2、Ghostty、Alacritty、SSH 窗口等任何等宽字体）。
   - 采用主流 CLI（Claude Code / Crush 风格）的**左图右文双栏卡片布局**（当终端宽度 `< 72` 列或高度 `< 18` 行时自动切换为紧凑单栏横幅，绝不挤爆小窗口）：
     - **左栏**：蘑菇头经典魔性眼神 ASCII 头像 + 底部居中签名梗 **「 呦，写 BUG 呢 」**。
     - **右栏**：
       - 产品标题与当前工作目录 `~/Documents/gowork/multi-agent-desktop`
       - 当前就绪模型与审批模式（如 `● qoder/Auto · [auto]`，若未就绪则高亮提示 `按 Ctrl+K 选择模型`）
       - 已挂载能力摘要（内置工具数 · 已加载插件数 · 子 Agent 协作就绪）
       - 3 条最核心高频快捷键提示（`/ 指令与插件 · Ctrl+K 切换模型 · Ctrl+T 展开思考`）
2. **轻量非阻塞入场动效**：
   - 启动首屏利用 Bubble Tea 的帧定时器（40ms/帧，共约 12 帧 ≈ 0.5 秒）播放**自上而下扫描线显影 + 眼神闪烁一帧 +「呦，写BUG呢」打字机浮现**。
   - **零阻塞铁律**：动画播放期间输入框即刻处于可聚焦输入状态——用户只要敲下任意一个字符，立即直接上屏进输入框并完成 Banner 定格，绝不耽误老手敲命令的半秒钟。

---

## 4. 实施分期与验收标准

| 阶段 | 交付内容 | 关键验收单测 |
|---|---|---|
| **Stage 1：主会话心智 + Picker 选择器 + 底栏与连通性自检** | `internal/clitui/picker.go`、二级 `/model` 与 `Ctrl+K`、`/mode` 与 `/resume` Picker、本地端口未启动自动回退、子 Agent 实时协作树 `cellSubAgent`、双层清爽底栏 | `TestCLIPickerModelTwoStageSelection`、`TestCLILocalModelUnreachableFallback`、`TestCLISubAgentLiveTree`、`TestCLIStatusFooterNoTruncatedGarbage` |
| **Stage 2：可执行插件系统 (`internal/plugin`)** | 全局与 `.mad/plugins` 扫描、`plugin.json` 解析、JSON/纯文本双兼容 Stdio 执行器、动态 `/` 命令补全与执行、动态 `tool.Tool` 桥接、`/plugins` 查看/热重载/脚手架 | `TestPluginDiscoveryAndOverride`、`TestPluginSlashCommandOutputAndPromptMode`、`TestPluginToolBridgeExecution`、`TestPluginInitScaffold` |
| **Stage 3：纯 ASCII 蘑菇头欢迎动效 (`internal/clitui/banner.go`)** | 蘑菇头「呦，写BUG呢」ASCII 字符画、左右分栏环境自检概览、窄屏自适应降级、启动扫描打字机动效与按键零阻塞跳过 | `TestBannerRenderWideAndNarrow`、`TestBannerAnimationAdvancesAndSettles`、`TestViewHeightMatchesTerminal`（含 Banner 帧） |

---

## 5. 不在本次范围（YAGNI 边界）

- 不引入嵌入式 Lua / JS / WASM 虚拟机（统一走跨语言的操作系统子进程 `stdin/stdout` 协议，简单、隔离、崩溃不拖垮主进程）。
- 不建立远程中心化插件市场下载器（本期支持本地目录与 Git 仓库 `.mad/plugins` 直接放入即用，后续有需要只需加一行 `git clone` 子命令即可）。
- 不改动桌面端 React WebView 的现有多会话 UI 布局。
