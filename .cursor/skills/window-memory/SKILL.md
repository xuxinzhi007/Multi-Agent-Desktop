---
name: window-memory
description: >-
  降低 multi-agent-desktop 长时间使用的内存。悬浮岛按需创建并销毁，岛只收摘要，主窗口不全量持有历史。
  在用户说内存、变卡、悬浮岛、迷你窗、WebView、WebContent、和主窗口联动、子进程时使用。
---

# 窗口内存

Go 主进程不是大头。每个 `mygo.Window` 是一块独立的 WKWebView，系统给它单独的 WebContent 进程。一块 WebView 不能同时画在两个原生窗口上。数据联动已经有了，不能靠「合成一个进程」省掉第二块 WebView。

## 先核对

改之前读这些符号。和代码不符就停，不要按旧结论硬改。

- `main.go` `WhenReady`：启动时 `NewWindow` 主窗口和 `miniWin`，随后 `miniWin.Hide()`。
- `API.ToggleMiniWindow`：先显示目标窗口，再隐藏另一个。两个都不可见时用户出不去。
- `API.FocusAgent`：岛点下去，主窗口聚焦到该会话。
- `emitToWindow`：`agent.delta`、`agent.message`、`agent.reasoning`、`agent.tool` 在主窗口之外再 `Emit` 给 `miniWin`。
- `src/mini.tsx`：岛订阅 `mini.status`，同时也订阅 delta / reasoning / message，把字累进 `rows`。
- `src/lib/useBackend.ts`：主窗口把每个 agent 的 `messages` 放在同一份 React state。
- `internal/browser/desk.go` `Desk.ensure`：内置浏览器是另一块 WebView，页面不可信。
- 工具输出上限是 `tool.DefaultMaxResultBytes`（8KB）。看屏幕的 JPEG 写在临时文件，不进会话。

## 做这三步，按这个顺序

### 1. 岛用时才创建，收起就销毁

启动只建主窗口。用户缩到岛上时再 `NewWindow`（`mini.html`，尺寸仍用 `islandW` / `islandH`）。回到主窗口时 `Destroy` 岛，不要 `Hide`。

顺序保持：先让主窗口可见并聚焦，再销毁岛。岛创建失败时主窗口必须仍在。

会话留在 Go 的 `Manager` 里。窗口没了，历史还在。`FocusAgent` 继续把主窗口拉到对应会话。

### 2. 岛只收摘要

岛开着时，不要把 `agent.delta`、`agent.reasoning`、完整 `agent.message` 再 `Emit` 给岛。岛用已有的 `mini.status`，上面那一行是截断后的当前句。

主窗口继续用 `agent.delta` 做流式。事件名不要新造，不要改 `mygo.Bind` / `NewEvent`，不要换成 WebSocket。`src/mygo.ts` 只有事件签名变了才需要 `mygo generate`。

### 3. 主窗口不要常驻全部历史

非当前会话在 React state 里只留 id、名字、状态、最后一句。点进某个会话，再向 Go 要最近一页。列表虚拟化。`agents.json` 仍是完整记录，不要为了显示去截断磁盘上的历史。

每个 delta 不要写 `agents.json`。检查点仍在发起模型请求之前，不要按 token 落盘。

## 不要做

- 不要把 loop、工具、会话拆成子进程。停止、检查点、`Registry.Invoke` 留在现在的 Go 进程。
- 不要把本地模型拉进应用进程。llama-server 继续在进程外。
- 不要把内置浏览器并进主 WebView。它继续单独窗口；关闭时销毁，不要只 Hide。
- 不要把悬浮岛画进主窗口里冒充省内存。主窗口在前台时，岛的价值是另一块屏幕上的原生窗口；省内存的办法是它不显示时不要有这块 WebView。
- 不要改看屏幕的 1.5s 轮询，也不要改浏览器点击前的 160ms + 400ms 停顿。

## 怎样算做完

- 只开主窗口时，没有悬浮岛的 WebContent。
- 缩到岛上再展开，会话还在，点岛仍能回到对应会话。
- 岛显示期间，主窗口的流式不受影响；岛上不会因为逐字 delta 把全文再长一份。
- 打开多个长会话后，切走的会话不在主窗口 state 里留着全文。
- `go test ./internal/agent/ ./internal/loop/` 仍通过。岛的切换补一个测试：销毁之后 `ToggleMiniWindow` 能再创建，且不会出现两个窗口都不可见。
