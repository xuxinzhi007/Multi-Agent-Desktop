# WebView2 内存与电源 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 只开主窗口时，任务管理器里不再有「Agent 协作」这块 WebView，并且主界面空闲时 GPU 进程不再因为氛围光斑一直占着。

**Architecture:** 启动只建主窗口。缩到悬浮岛时再 `NewWindow`，回到主窗口或从岛点进会话时先让主窗口可见，再 `Destroy` 岛。岛开着时继续收 `mini.status` 和频道消息，不再收逐字 `agent.delta`、`agent.reasoning`、完整 `agent.message`。主界面的两块光斑留着，但不再在带 `blur(90px)` 的元素上做无限位移动画。

**Tech Stack:** Go 1.27、`github.com/egoist/mygo` v0.9.0、WebView2、React。窗口在 `main.go` 的 `WhenReady`、`API.ToggleMiniWindow`、`API.FocusAgent`。氛围层在 `src/index.css` 的 `.c-ambient__blob`。

## Global Constraints

- 一块 WebView 不能同时画在两个原生窗口上。不要把岛画进主窗口，也不要把内置浏览器并进主 WebView。
- 不要把 loop、工具、会话拆成子进程。停止、检查点、`Registry.Invoke` 留在现在的 Go 进程。
- 不要把本地模型拉进应用进程。
- 不要改看屏幕的 1.5s 轮询，也不要改浏览器点击前的 160ms + 400ms 停顿。
- 不要新造事件名，不要改 `mygo.Bind` / `NewEvent`，不要换成 WebSocket。这次不改 `src/mygo.ts` 的签名，所以不要跑 `mygo generate`。
- 会话留在 Go 的 `Manager` 里。岛销毁之后历史还在。`FocusAgent` 继续把主窗口拉到对应会话。
- 两个窗口都不可见是故障。切换时先让目标窗口可见，再藏或销毁另一个。岛创建失败时主窗口必须仍在。
- `agents.json` 仍是完整记录。这次不改主窗口如何持有历史。
- 工作区里 `internal/browser/desk.go`、`internal/browser/op.go`、`internal/browser/nav.go`、`internal/tool/web/web.go` 的未提交改动与这次无关。不要改它们，也不要把它们带进这次提交。
- 证据基线是 2026-11-19 的任务管理器截图，分支 `fix/p0-correctness-stopgap`，提交 `b1166f1`。截图分组合计 348.5 MB、电源「非常高」。GPU 进程 71.9 MB、11% GPU、电源「高」。渲染进程「Agent 协作」45.8 MB、1.3% CPU，标题来自 `mini.html`。主窗口渲染进程 121.6 MB，标题来自 `index.html`。

## 明天做完的样子

- 只开主窗口、岛没收起过：进程列表里没有 `WebView2: Agent 协作`。
- 缩到岛上再展开：会话还在，点岛仍能回到对应会话，并且不会出现两个窗口都没了。
- 岛显示期间，主窗口的流式不受影响。岛上胶囊那一行仍来自 `mini.status`（`src/island/logic.ts` 的 `labelOf`）。岛不再因为逐字 delta 把回复全文再长一份。
- 主窗口停在前台、鼠标不动，过大约 30 秒后，WebView2 GPU 进程的电源不再停在「高」，GPU 占用不再维持在截图那种 11% 左右。
- `go test -count=1 -run 'TestIslandToggleOrder|TestMiniSkipsStreamEvents' .` 通过。
- `go test ./internal/agent/ ./internal/loop/` 仍通过。

## 明天不做

主窗口「非当前会话只留最后一句、点进去再向 Go 要一页」不在这次里。截图里主渲染进程 122 MB，是一块 WebView2 页面的正常体量，不是全文历史撑起来的。那一步要改 `AgentView`、`ListAgents` 和 `src/lib/useBackend.ts`，还会碰生成绑定。另开一次。

`WebView2: about:blank`（截图 12.9 MB、0% CPU）对不上启动的两个窗口。内置浏览器没地址时会先打开 `about:blank`，随后 `LoadHTML` 把文档标题改成「浏览器」（`internal/browser/desk.go`）。明天不要为了这 13 MB 去改浏览器。若做完下面两步之后它还在，再单独看有没有一个标题仍是空白页的窗口。

WebView2 Manager、GPU 进程、Network、Storage、Crashpad 是引擎自带的，大约 168 MB。留着。

## 文件

- 改 `main.go`：启动不再建岛；抽出 `ensureMiniWindow`、`destroyMiniWindow`、`planIslandToggle`、`miniSkipsStreamEvent`；`ToggleMiniWindow` 和 `FocusAgent` 按这个顺序走；`emitToWindow` 不再把三条流式事件发给岛。
- 改 `main_test.go`：加两个不创建真实窗口的测试。
- 改 `src/mini.tsx`：去掉对 `agentDelta`、`agentReasoning`、`agentMessage` 的订阅。用户自己在岛上发出的那一行、看屏幕的建议卡留着。
- 改 `src/index.css`：去掉光斑的无限动画和 `will-change`。
- 不改 `src/island/logic.ts`、`src/mygo.ts`、`mini.html`、`internal/browser/`。

---

### Task 1: 光斑不再持续占用 GPU

**Files:**
- Modify: `src/index.css`（`.c-ambient__blob` 一段，约 604–678 行）

**Interfaces:**
- Consumes: 现有的 `--px` / `--py`。`src/components/AmbientBackground.tsx` 只在鼠标移动时写这两个变量，鼠标停下后 rAF 会停。这次不动那个组件。
- Produces: 无新符号。

光斑的 `filter: blur(90px)` 和 `animation: ambient-drift-* infinite` 写在同一个元素上，还有 `will-change: transform`。窗口开着就会一直重画大约半屏的模糊层。截图里电源「非常高」挂在整组上，细分只有 GPU 进程是「高」。

- [ ] **Step 1: 改样式**

把 `.c-ambient__blob` 的 `will-change: transform` 删掉。把 `.c-ambient__blob--a` 和 `.c-ambient__blob--b` 上的 `animation: ... infinite` 两行删掉。把 `@keyframes ambient-drift-a` 和 `@keyframes ambient-drift-b` 整段删掉。

`@media (prefers-reduced-motion: reduce)` 里 `.c-ambient__blob { animation: none; }` 可以留着，没有动画可关，无害。网格的 `transform: translate3d(...)` 留着，它只跟鼠标走。

改完后 `.c-ambient__blob--a` 应是：

```css
.c-ambient__blob--a {
  width: 54vw;
  height: 54vw;
  top: -16vw;
  left: -12vw;
  background: radial-gradient(
    circle at 50% 50%,
    color-mix(in srgb, var(--ambient-tint) 62%, transparent),
    transparent 70%
  );
  transform: translate3d(calc(var(--px) * 18px), calc(var(--py) * 18px), 0);
}
```

`.c-ambient__blob--b` 同样只删 `animation` 那一行，`transform` 留着。

- [ ] **Step 2: 确认没有残留引用**

在仓库里搜 `ambient-drift-a` 和 `ambient-drift-b`。期望只剩零处。`AmbientBackground.tsx` 仍渲染这两个光斑，不要删组件。

- [ ] **Step 3: 提交这一步**

只暂存 `src/index.css`。

```bash
git add src/index.css
git commit -m "$(cat <<'EOF'
fix: stop the ambient blobs from animating under a blur filter

The infinite translate on the blurred layers kept the WebView2 GPU process busy while the main window was idle.

EOF
)"
```

手动看 GPU 放在 Task 3 做完一起看。这一步单独提交，是为了明天如果窗口生命周期出问题，电源这项已经在历史里。

---

### Task 2: 岛用时才创建，回到主窗口就销毁

**Files:**
- Modify: `main.go`（`ToggleMiniWindow` 约 1769 行，`FocusAgent` 约 1840 行，`WhenReady` 约 2330–2379 行）
- Test: `main_test.go`

**Interfaces:**
- Consumes: 已有的 `miniWindowRef`、`islandW`、`islandH`、`applyMini`、`denyPagePermissions`、`pushMini`。
- Produces:
  - `type islandStep string`
  - `func planIslandToggle(show bool, created bool) []islandStep`
  - `func ensureMiniWindow(api *API) *mygo.Window`
  - `func destroyMiniWindow()`

现有 `WhenReady` 创建岛之后立刻 `miniWin.Hide()`。mygo 的 `Hide` 只调用 `native.Hide()`，WebView 进程还在。截图里的「Agent 协作」就是 `mini.html` 的 `<title>`，窗口标题其实是「Agent 舰队」。

- [ ] **Step 1: 先写会失败的顺序测试**

在 `main_test.go` 末尾加上。此时 `planIslandToggle` 还不存在，`go test` 应编译失败。

```go
func TestIslandToggleOrder(t *testing.T) {
	back := planIslandToggle(false, true)
	if len(back) < 2 || back[0] != stepShowMain || back[len(back)-1] != stepDestroyMini {
		t.Fatalf("back to main = %v, want show-main first and destroy-mini last", back)
	}
	for _, s := range back {
		if s == stepHideMain {
			t.Fatalf("back to main hides the main window: %v", back)
		}
	}

	forward := planIslandToggle(true, true)
	if len(forward) < 2 || forward[0] != stepEnsureMini || forward[len(forward)-1] != stepHideMain {
		t.Fatalf("to island = %v, want ensure-mini first and hide-main last", forward)
	}
	sawShow := false
	for _, s := range forward {
		if s == stepDestroyMini {
			t.Fatalf("opening the island destroys it: %v", forward)
		}
		if s == stepShowMini {
			sawShow = true
		}
		if s == stepHideMain && !sawShow {
			t.Fatalf("main hides before the island is shown: %v", forward)
		}
	}

	failed := planIslandToggle(true, false)
	for _, s := range failed {
		if s == stepHideMain || s == stepDestroyMini {
			t.Fatalf("create failure changes visibility the wrong way: %v", failed)
		}
	}
	if len(failed) == 0 || failed[0] != stepShowMain {
		t.Fatalf("create failure = %v, want main shown first", failed)
	}
}
```

- [ ] **Step 2: 跑测试，确认失败**

```bash
go test -count=1 -run TestIslandToggleOrder .
```

期望：编译错误，`undefined: planIslandToggle`。

- [ ] **Step 3: 加上顺序函数，并让切换按它执行**

把常量放在现有 `islandW` 旁边：

```go
type islandStep string

const (
	stepEnsureMini  islandStep = "ensure-mini"
	stepShowMini    islandStep = "show-mini"
	stepFocusMini   islandStep = "focus-mini"
	stepPushMini    islandStep = "push-mini"
	stepHideMain    islandStep = "hide-main"
	stepShowMain    islandStep = "show-main"
	stepFocusMain   islandStep = "focus-main"
	stepDestroyMini islandStep = "destroy-mini"
)

// planIslandToggle 只决定顺序，不碰窗口。
// 回到主窗口：先显示并聚焦主窗口，最后销毁岛。
// 打开岛：先建好并显示岛，最后才隐藏主窗口。
// 岛创建失败：只保证主窗口可见，不隐藏主窗口，也不销毁一个不存在的岛。
func planIslandToggle(show bool, created bool) []islandStep {
	if !show {
		return []islandStep{stepShowMain, stepFocusMain, stepDestroyMini}
	}
	if !created {
		return []islandStep{stepShowMain, stepFocusMain}
	}
	return []islandStep{
		stepEnsureMini,
		stepShowMini,
		stepFocusMini,
		stepPushMini,
		stepHideMain,
	}
}
```

`ensureMiniWindow` 使用现在 `WhenReady` 里岛的那组 `WindowOptions`，不要改尺寸和 `URL`：

```go
func ensureMiniWindow(api *API) *mygo.Window {
	if miniWindowRef != nil && !miniWindowRef.IsDestroyed() {
		return miniWindowRef
	}
	w := mygo.NewWindow(mygo.WindowOptions{
		Title:           "Agent 舰队",
		Width:           islandW,
		Height:          islandH,
		MinWidth:        islandW,
		MaxWidth:        islandW,
		MinHeight:       islandH,
		MaxHeight:       islandH,
		FitContent:      false,
		Panel:           true,
		Frameless:       true,
		AlwaysOnTop:     true,
		SkipTaskbar:     true,
		Transparent:     true,
		BackgroundColor: "#00000000",
		DisableMove:     false,
		StateKey:        "mini",
		URL:             "/mini.html",
	})
	if w == nil {
		return nil
	}
	miniWindowRef = w
	if api != nil {
		api.applyMini()
	}
	denyPagePermissions(w)
	return w
}

func destroyMiniWindow() {
	w := miniWindowRef
	miniWindowRef = nil
	if w != nil && !w.IsDestroyed() {
		w.Destroy()
	}
}
```

用下面的实现替换 `ToggleMiniWindow`。不要再调用 `mini.Hide()`。

```go
func (s *API) ToggleMiniWindow(show bool) bool {
	var mini *mygo.Window
	if show {
		mini = ensureMiniWindow(s)
	}
	for _, step := range planIslandToggle(show, mini != nil) {
		switch step {
		case stepEnsureMini:
			// ensureMiniWindow 已经在循环前调用。
		case stepShowMini:
			mini.Show()
		case stepFocusMini:
			mini.Focus()
		case stepPushMini:
			s.pushMini()
		case stepHideMain:
			if s.win != nil && !s.win.IsDestroyed() {
				s.win.Hide()
			}
		case stepShowMain:
			if s.win != nil && !s.win.IsDestroyed() {
				s.win.Show()
			}
		case stepFocusMain:
			if s.win != nil && !s.win.IsDestroyed() {
				s.win.Focus()
			}
		case stepDestroyMini:
			destroyMiniWindow()
		}
	}
	return show && mini != nil
}
```

`FocusAgent` 同样先露出主窗口，再销毁岛：

```go
func (s *API) FocusAgent(agentId string) {
	if s.win != nil && !s.win.IsDestroyed() {
		s.win.Show()
		s.win.Focus()
	}
	destroyMiniWindow()
	if s.win != nil && !s.win.IsDestroyed() {
		AgentFocused.Emit(s.win, AgentFocusedEvent{AgentID: agentId})
	}
}
```

`WhenReady` 里删掉整段 `miniWin := mygo.NewWindow(...)`、`miniWindowRef = miniWin`、`api.applyMini()`、`denyPagePermissions(miniWin)`、`miniWin.Hide()`。主窗口的 `NewWindow` 和 `denyPagePermissions(win)` 留着。启动结束后 `miniWindowRef` 必须仍是 nil。

`IslandResize`、`applyMini`、`LayoutMini`、`pushMini` 已经在 `miniWindowRef == nil` 或已销毁时返回。不要为了岛不存在去改它们。

- [ ] **Step 4: 跑测试**

```bash
go test -count=1 -run TestIslandToggleOrder .
```

期望：PASS。

- [ ] **Step 5: 提交这一步**

```bash
git add main.go main_test.go
git commit -m "$(cat <<'EOF'
fix: create the island window only while it is showing

Hiding the island at startup left its WebView2 renderer alive. Show the main window before destroying the island so both windows cannot be invisible together.

EOF
)"
```

---

### Task 3: 岛不再收逐字流

**Files:**
- Modify: `main.go` 的 `emitToWindow`（约 2151–2194 行）
- Modify: `src/mini.tsx`（约 97–127 行那个 `useEffect`）
- Test: `main_test.go`

**Interfaces:**
- Consumes: 现有事件名 `agent.delta`、`agent.reasoning`、`agent.message`。
- Produces: `func miniSkipsStreamEvent(event string) bool`

主窗口继续收这三条，流式不要动。岛已经在 `agent.created`、`agent.deleted`、`agent.status`、`agent.tool`、`agent.loop` 时收 `mini.status`。频道消息仍走 `emitChannelMessage`，岛上的信封靠它，不要停。

`src/mini.tsx` 里用户自己输入的那条 `kind: 'user'`，以及看屏幕建议卡，都不是模型逐字流。留着。

- [ ] **Step 1: 写会失败的测试**

```go
func TestMiniSkipsStreamEvents(t *testing.T) {
	for _, ev := range []string{"agent.delta", "agent.reasoning", "agent.message"} {
		if !miniSkipsStreamEvent(ev) {
			t.Fatalf("miniSkipsStreamEvent(%q) = false, want true", ev)
		}
	}
	for _, ev := range []string{"agent.status", "agent.tool", "agent.loop", "agent.created", "agent.deleted", "agent.error"} {
		if miniSkipsStreamEvent(ev) {
			t.Fatalf("miniSkipsStreamEvent(%q) = true, want false", ev)
		}
	}
}
```

- [ ] **Step 2: 跑测试，确认失败**

```bash
go test -count=1 -run TestMiniSkipsStreamEvents .
```

期望：编译错误，`undefined: miniSkipsStreamEvent`。

- [ ] **Step 3: 实现，并接到 emitToWindow**

```go
func miniSkipsStreamEvent(event string) bool {
	switch event {
	case "agent.delta", "agent.reasoning", "agent.message":
		return true
	default:
		return false
	}
}
```

在 `emitToWindow` 里，下面三处删掉发给 `miniWin` 的 `Emit`，主窗口的 `Emit` 留着：

- `case "agent.delta":` 里的 `AgentDelta.Emit(miniWin, v)`
- `case "agent.message":` 里的 `AgentMessage.Emit(miniWin, v)`
- `case "agent.reasoning":` 里的 `AgentReasoning.Emit(miniWin, v)`

每处改成先问这个函数，避免以后又把全文接回去：

```go
case "agent.delta":
	if v, ok := toAgentDelta(data); ok {
		AgentDelta.Emit(win, v)
		if miniWin != nil && !miniWin.IsDestroyed() && !miniSkipsStreamEvent(event) {
			AgentDelta.Emit(miniWin, v)
		}
	}
```

`agent.message`、`agent.reasoning` 按同样的形状改。`agent.error` 和 `agent.tool` 的岛分支不要用这个函数挡掉。函数对它们返回 false。末尾那个 `MiniStatusEvent.Emit` 的 switch 不要动。

- [ ] **Step 4: 岛的页面不再把这三条累进 rows**

删掉 `src/mini.tsx` 里订阅 `events.agentDelta`、`events.agentReasoning`、`events.agentMessage` 的那个 `useEffect`（现在大约在 97–127 行）。不要删 `events.miniStatus`、`events.channelMessage`、`events.screenHint`。不要删 `send` 里为本机输入追加的 user row。

胶囊上的字继续来自 `select(model)` 的 `label`。不要新造一个「当前句」事件去把全文接回岛上。

- [ ] **Step 5: 跑测试**

```bash
go test -count=1 -run 'TestIslandToggleOrder|TestMiniSkipsStreamEvents' .
go test ./internal/agent/ ./internal/loop/
```

期望：都 PASS。

- [ ] **Step 6: 提交这一步**

```bash
git add main.go main_test.go src/mini.tsx
git commit -m "$(cat <<'EOF'
fix: keep streamed agent text on the main window only

The island already renders mini.status. Forwarding each delta gave it a second copy of the reply while its WebView was alive.

EOF
)"
```

---

### Task 4: 按任务管理器核对

这一步不改代码。用安装后的程序或 `go run` 起来的开发进程都可以，但看的是 WebView2 子进程，不是 Go 测试进程。

- [ ] **Step 1: 只开主窗口**

冷启动，不要点缩小。等大约 30 秒，鼠标不要停在窗口里晃。任务管理器展开「WebView2 Manager」这一组。

期望：

- 有 `WebView2: Multi-Agent Desktop`（标题可能被截成 `Multi-Agen…`）。
- 没有 `WebView2: Agent 协作`。
- GPU 进程的电源不是「高」，GPU 列不再停在 11% 这一档。允许刚启动的几秒里有一小段占用。

- [ ] **Step 2: 缩到岛上，再回来**

点主窗口的缩小。岛出现，主窗口消失。再从岛回到主窗口。

期望：

- 岛在的时候，进程里出现 `WebView2: Agent 协作`。
- 回到主窗口后，这个进程消失，主窗口仍在。
- 原来的会话还在。从岛点进某个会话，主窗口打开的是那个会话。
- 任何一步都不会变成两个窗口都看不到。如果岛创建失败，主窗口必须还在。

- [ ] **Step 3: 岛开着时让一个会话吐字**

主窗口先发起一轮回复，再缩到岛上，或者就在岛上发一句。

期望：主窗口回到前台后，回复仍是流式出现的。岛上胶囊是「某 Agent 思考中」这类 `labelOf` 的句子，不会跟着 token 把整段回复铺开。频道信封还在。

- [ ] **Step 4: 对不上就停**

进程还在、电源仍高、或回来之后会话没了，不要接着做「主窗口历史分页」。先记下任务管理器里剩下的进程标题，再改。
