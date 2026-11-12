# events（W3 待实现）

当前 W2 已交付多 agent 会话、流式 LLM、shell 执行。
本目录是 W3 的规划占位，尚无 Go 代码。

现状：事件由 `internal/server/hub.go` 直接广播。

规划内容：

- 把事件总线抽成独立包，与 WS 解耦
- 事件持久化与断线重放