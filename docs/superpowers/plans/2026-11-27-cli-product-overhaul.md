# CLI Product Overhaul Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade `multi-agent-desktop` CLI (`-headless` terminal TUI) into a production-ready developer tool featuring a Lead-Session + live Sub-Agent collaboration tree, interactive keyboard Pickers (`/model`, `/mode`, `/resume`, `Ctrl+K`) with local-port reachability fallback, an open executable plugin system (`plugin.json` + JSON-Stdio), and a classic ASCII "呦，写BUG呢" startup banner animation.

**Architecture:**
1. **Stage 1 (Core UX & Lead/Sub-agent Model)**: Add `internal/clitui/picker.go` for multi-level interactive list selection; add local endpoint probe + model catalog resolution in `headless.go` & `cli_tui.go`; emit `agent.subtask` progress events from `internal/agent/delegate.go` (`spawnTasks` & `runTransientLoop`) and render live collapsible sub-agent trees (`cellSubAgent`) in `cli_tui.go` alongside a clean status footer.
2. **Stage 2 (Executable Plugin Host)**: Create `internal/plugin/` (`manifest.go`, `runner.go`, `tool_bridge.go`, `scaffold.go`) scanning `~/.multi-agent-desktop/plugins/` and `<cwd>/.mad/plugins/`, exposing custom `/commands` (`output` and `prompt` modes) to `cli_tui.go` and `tool.Tool` bridges to `agent.Manager`.
3. **Stage 3 (ASCII Avatar Banner & Motion)**: Create `internal/clitui/banner.go` rendering the pure-ASCII "呦，写BUG呢" bowl-cut character + right-hand workspace/model/plugin summary card, with non-blocking scanline reveal animation on startup and narrow-terminal fallback.

**Tech Stack:** Go 1.24+, Bubble Tea (`github.com/charmbracelet/bubbletea`), Lipgloss (`github.com/charmbracelet/lipgloss`), `os/exec` JSON-Stdio subprocess protocol.

## Global Constraints

- Exact viewport height invariant: `strings.Count(m.View(), "\n") + 1 == m.height` across all terminal sizes, overlays, pickers, modals, and banner frames.
- Performance invariant: 1600-message session per-keystroke (`Update` + `View`) latency `< 2ms` (`TestCLIViewLargeHistoryUnder2ms`).
- Zero-blocking startup: Any printable keystroke during the startup banner animation immediately settles the banner and inserts into the editor.
- Pure ASCII art for the avatar (`@ # % * + = - : .` characters only) with graceful narrow-width (`width < 74`) single-column fallback.
- Format all modified Go files with `gofmt -w`.

---

### Task 1: Interactive Keyboard Picker Component (`internal/clitui/picker.go`)

**Files:**
- Create: `internal/clitui/picker.go`
- Modify: `internal/clitui/clitui_test.go`

**Interfaces:**
- Produces:
  - `type PickerItem struct { ID, Label, Badge, Hint string; Disabled bool; Data any }`
  - `type Picker struct`
  - `func NewPicker(width int, theme Theme) Picker`
  - `func (p Picker) Open(kind, title string, items []PickerItem) Picker`
  - `func (p Picker) Close() Picker`
  - `func (p Picker) Active() bool`
  - `func (p Picker) Kind() string`
  - `func (p Picker) SetWidth(w int) Picker`
  - `func (p Picker) Height() int`
  - `func (p Picker) HandleKey(msg tea.KeyMsg) (next Picker, chosen *PickerItem, cancelled bool, used bool)`
  - `func (p Picker) View() string`

- [ ] **Step 1: Write failing unit tests for `Picker` in `internal/clitui/clitui_test.go`**
  Test opening a picker, filtering via rune typing, navigating via Down/Up, selecting via Enter, and cancelling via Esc while verifying exact rendered line count matches `p.Height()`.
- [ ] **Step 2: Run test to verify failure**
  Run: `go test -v ./internal/clitui -run TestPickerFilterAndSelect`
- [ ] **Step 3: Implement `internal/clitui/picker.go`**
  Implement bordered compact floating picker (max 8 visible rows + title/filter header + footer hint `↑/↓ 选择 · 输入过滤 · Enter 确认 · Esc 取消`).
- [ ] **Step 4: Run `internal/clitui` tests to verify pass**
  Run: `go test -v ./internal/clitui/...`

---

### Task 2: Two-Stage Model Picker, Reachability Fallback & Clean Footer (`headless.go`, `cli_tui.go`)

**Files:**
- Modify: `headless.go`
- Modify: `cli_tui.go`
- Test: `cli_tui_ux_test.go`

**Interfaces:**
- Consumes: `clitui.Picker`, `llm.ListModelMetas`, `llm.QoderModelNames`, `llm.IsLocalProvider`, `llm.ApplyDefaults`
- Produces:
  - `probeLocalEndpoint(baseURL string, timeout time.Duration) bool` in `headless.go`
  - Smart fallback in `ensureCLIAgent`: skips unreachable local provider (`127.0.0.1:8080` / `11434`) when a ready cloud provider (`qoder`, `deepseek`, etc.) or another reachable provider exists.
  - Interactive `/model` & `Ctrl+K` two-stage picker (Provider -> Model list with presets & live catalog fetch), interactive `/mode` picker, interactive `/resume` picker (filtering out child sub-agents `ParentID != ""`).
  - Human-friendly `cellError` diagnostic hint when connection is refused or API key/model is missing, plus clean non-truncated `statusLine()`.

- [ ] **Step 1: Write failing tests in `cli_tui_ux_test.go`**
  Add `TestCLIPickerModelTwoStageSelection`, `TestCLILocalModelUnreachableFallback`, and `TestCLIStatusFooterNoTruncatedGarbage`.
- [ ] **Step 2: Run targeted tests to verify failure**
  Run: `go test -v . -run "TestCLIPickerModelTwoStageSelection|TestCLILocalModelUnreachableFallback|TestCLIStatusFooterNoTruncatedGarbage"`
- [ ] **Step 3: Implement reachability fallback in `headless.go` and Picker wiring + clean footer in `cli_tui.go`**
- [ ] **Step 4: Run tests to verify pass**
  Run: `go test -v . -run "TestCLI|TestViewHeight|TestHeadless|TestEnsureCLIAgent"`

---

### Task 3: Lead Session Mental Model & Live Sub-Agent Collaboration Tree (`internal/agent/delegate.go`, `cli_tui.go`)

**Files:**
- Modify: `internal/agent/delegate.go`
- Modify: `cli_tui.go`
- Test: `cli_tui_ux_test.go`

**Interfaces:**
- Produces:
  - `agent.subtask` events emitted from `delegate.go` (`phase: "batch_start" | "step" | "child_done" | "batch_done"`, carrying `parentId`, `childId`, `name`, `role`, `mode`, `activity`, `status`, `durationMs`, `branch`).
  - `OnEvent` hook wired in `runTransientLoop` so read-only sub-agents stream live tool steps (`Search`, `Read`, `Glob`) to their parent session's event stream!
  - `cellSubAgent` rendering in `cli_tui.go`: renders a live tree card (`⏺ 子智能体协作 · 2 运行中`) showing each sub-agent's name, current tool/status, branch, and elapsed time; `/agents` slash command summarizing active/completed sub-agents; workspace-centric lead session naming (`Main` / repo folder name instead of `Agent 25`).

- [ ] **Step 1: Write failing test `TestCLISubAgentLiveTree` in `cli_tui_ux_test.go`**
- [ ] **Step 2: Run test to verify failure**
  Run: `go test -v . -run TestCLISubAgentLiveTree`
- [ ] **Step 3: Implement `agent.subtask` event emission in `internal/agent/delegate.go` and `cellSubAgent` + `/agents` in `cli_tui.go`**
- [ ] **Step 4: Run agent & CLI tests to verify pass**
  Run: `go test -v ./internal/agent -run "TestDelegate|TestSpawn|TestSubagent" && go test -v . -run "TestCLI|TestViewHeight"`

---

### Task 4: Executable Plugin System (`internal/plugin/` + `agent.Manager` + `cli_tui.go`)

**Files:**
- Create: `internal/plugin/plugin.go`
- Create: `internal/plugin/scaffold.go`
- Create: `internal/plugin/plugin_test.go`
- Modify: `internal/agent/manager.go`
- Modify: `internal/agent/loop_backend.go`
- Modify: `cli_tui.go`
- Test: `cli_tui_ux_test.go`

**Interfaces:**
- Produces in `internal/plugin`:
  - `type Manifest struct { Name, Version, Description string; TimeoutSec int; Commands []CommandSpec; Tools []ToolSpec }`
  - `type Manager struct` with `Load(globalDir, workspaceDir string) error`, `List() []LoadedPlugin`, `Commands() []BoundCommand`, `Tools() []tool.Tool`, `ExecCommand(ctx context.Context, cmdName, rawArgs, cwd, agentID string) (CommandOutput, error)`
  - `func InitScaffold(rootDir, name string) (string, error)` generating `.mad/plugins/<name>/plugin.json` + executable `./bin/hello.sh`
- Consumes in `agent.Manager` & `cli_tui.go`:
  - `mgr.SetPluginTools(fn func(workDir string) []tool.Tool)` registered in `prepareNativeLoop`
  - Dynamic `/命令` registration into `Suggest` autocomplete, `/plugins` (`list` / `reload` / `init <name>`), and execution supporting both `"output"` and `"prompt"` modes.

- [ ] **Step 1: Write failing unit tests in `internal/plugin/plugin_test.go` and `cli_tui_ux_test.go`**
  Cover `TestPluginDiscoveryAndOverride`, `TestPluginSlashCommandOutputAndPromptMode`, `TestPluginToolBridgeExecution`, and `TestPluginInitScaffold`.
- [ ] **Step 2: Run tests to verify failure**
  Run: `go test -v ./internal/plugin/...`
- [ ] **Step 3: Implement `internal/plugin/` package and wire into `agent.Manager` + `cli_tui.go`**
- [ ] **Step 4: Run plugin, agent, and CLI test suites**
  Run: `go test -v ./internal/plugin/... && go test -v . -run "TestCLI|TestPlugin"`

---

### Task 5: Pure-ASCII "呦，写BUG呢" Startup Banner & Non-Blocking Scanline Animation (`internal/clitui/banner.go`, `cli_tui.go`)

**Files:**
- Create: `internal/clitui/banner.go`
- Modify: `internal/clitui/clitui_test.go`
- Modify: `cli_tui.go`
- Test: `cli_tui_ux_test.go`

**Interfaces:**
- Produces:
  - `type BannerInfo struct { AppName, WorkDir, Model, Mode string; PluginCount, SkillCount int; Warning string }`
  - `const BannerMaxFrame = 12`
  - `func RenderBanner(info BannerInfo, width, frame int, theme Theme) string`
  - Pure-ASCII bowl-cut meme avatar + `"呦，写BUG呢"` caption + right-hand workspace/model/plugin status card + scanline/typewriter entrance frames (`frame 0..12`) + narrow width (`< 74` cols) compact fallback + immediate settle on first user keystroke.

- [ ] **Step 1: Write failing tests `TestBannerRenderWideAndNarrow` and `TestBannerAnimationAdvancesAndSettles`**
- [ ] **Step 2: Run tests to verify failure**
  Run: `go test -v ./internal/clitui -run TestBanner && go test -v . -run TestBanner`
- [ ] **Step 3: Implement `internal/clitui/banner.go` and integrate welcome cell animation into `cli_tui.go`**
- [ ] **Step 4: Run full verification suite & benchmark**
  Run: `gofmt -w . && go test -v ./internal/clitui/... ./internal/plugin/... && go test -v . -run "TestCLI|TestBanner|TestViewHeight|TestHeadless|TestEnsureCLIAgent" && go build -o /tmp/mad-cli-verify . && rm -f /tmp/mad-cli-verify`
