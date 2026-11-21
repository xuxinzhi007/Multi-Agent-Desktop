---
name: window-memory
description: >-
  降低 multi-agent-desktop 长时间使用的内存。悬浮岛已经去掉，不要再加第二块 WebView。
  在用户说内存、变卡、悬浮岛、迷你窗、WebView、WebContent、和主窗口联动、子进程时使用。
---

# 窗口内存

悬浮岛已去掉。飞书机器人负责人不在主窗口时交代事情。不要把岛加回来，也不要在主窗口里再画一块胶囊冒充它。

Go 主进程不是大头。每个 `mygo.Window` 是一块独立的 WebView，系统给它单独的渲染进程。一块 WebView 不能同时画在两个原生窗口上。

## 先核对

改之前读这些符号。和代码不符就停，不要按旧结论硬改。

- `main.go` `WhenReady`：启动时只 `NewWindow` 主窗口。没有 `miniWin`，没有 `ToggleMiniWindow`。
- `internal/browser/desk.go` `Desk.ensure`：内置浏览器是另一块 WebView，页面不可信。关闭时销毁，不要只 Hide。
- `src/lib/useBackend.ts`：非当前会话在 React state 里只留摘要。点进某个会话，再向 Go 要最近一页。
- 工具输出上限是 `tool.DefaultMaxResultBytes`（8KB）。看屏幕的 JPEG 不进会话。屏幕协助随悬浮岛一起去掉了，不要接回 `internal/screen`。

## 不要做

- 不要新建悬浮岛、`mini.html`，或启动时 `Hide` 的第二扇窗口。
- 不要把 loop、工具、会话拆成子进程。停止、检查点、`Registry.Invoke` 留在现在的 Go 进程。
- 不要把本地模型拉进应用进程。llama-server 继续在进程外。
- 不要把内置浏览器并进主 WebView。
- 不要为了省内存去改主界面里和这次无关的交互停顿。

## 怎样算没有退回去

- 任务管理器里没有标题为「Agent 协作」的 WebView2 渲染进程。那是旧 `mini.html` 的页面标题。
- 只开主窗口时，这个应用只有一块页面渲染进程，加上 WebView2 自带的 Manager、GPU、Network、Storage、Crashpad。
- 打开内置浏览器时才多一块页面渲染进程，标题不是「Agent 协作」。关掉浏览器后那一块消失。
