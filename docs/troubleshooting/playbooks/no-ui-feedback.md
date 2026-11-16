# Playbook：用户操作后界面没反应

适用症状：点了按钮 / 敲了回车 / 点了停止，**界面没有任何变化**。不白屏、不报错、不转圈，就是「像没点一样」。

这个症状在本仓库有 4 个已确认的根因（见 [`../known-issues.md`](../known-issues.md) 第 1、4、5、7 条），分布在完全不同的层。**按下面的顺序排查，不要跳步** —— 顺序就是「改动成本从低到高 + 覆盖概率从高到低」。

---

## 第 0 步：先确认是不是「假无反应」

30 秒，避免白查一小时。

| 检查 | 方法 | 结论 |
|---|---|---|
| 白屏了吗 | 打开 `src/components/ErrorBoundary.tsx` 是否显示错误信息 | 有错误信息 → 去 `known-issues.md` 第 6/11/12 条，走「白屏」路径 |
| 后端活着吗 | DevTools Console 里 `window.mygo` 存在？`window.mygo.API.ListAgents()` 能返回？ | 不存在 → IPC 没注入，是壳的问题，不是业务代码 |
| 有异常吗 | Console 有无红色；e2e 里可加 `page.on('pageerror')` 断言 | 有 → 先读异常，别猜 |
| 是"慢"还是"没" | 等 5 秒再看 | 5 秒后才出现 → 不是无反馈，是**无即时反馈**，走第 1 步 |

> 已知陷阱：状态字段会骗人。`Agent.StopAll()`（`internal/agent/manager.go:850-867`）在 worker 仍卡在 `for ev := range events` 时就直接把 `Status` 写成 `StatusIdle`。**「状态已经是 idle」不能证明停止成功**，见第 4 步。

---

## 第 1 步：前端 state —— 请求真的发出去了吗？本地有没有立刻更新？

目标：区分「没发出去」和「发了但没渲染」。

```js
// DevTools Console
window.__mockCalls                    // e2e/浏览器 mock 环境：每个 API 调用的记录
```

阅读顺序：

1. **点按钮的 handler 有没有被调用？** 在 handler 第一行打 `console.log`。
   - 没有 → 是 UI 层问题：按钮被遮挡 / `pointer-events: none` / 事件被父元素拦截 / 条件渲染没挂上。**不要往下查后端。**
   - 有 → 继续。
2. **`send(action, data)` 的 action 字符串对得上吗？** 打开 `src/lib/useBackend.ts` 的 `sendImpl` switch，逐字比对。**拼错的 action 会静默走 default**，看起来完全像"没反应"。
3. **本地有没有乐观更新？**
   - 打开 `src/lib/useBackend.ts` 搜 `optimisticMessage`。`agent.prompt` 分支应当**先本地插入消息 + 置 `pending[agentId]`**，再调 `API.promptAgent`。
   - 如果某条路径只是 `return API.xxx(...)`，那它的 UI 表现**完全依赖后端事件回推** —— 这就是 `known-issues.md` 第 7 条的形状。后端慢 / 事件丢 → 用户看到零反馈。
   - 判据：`e2e/send-repro.spec.ts`（「发送后消息立刻出现在对话里」）必须是绿的。它故意让 mock 的 `API.PromptAgent` 不回推任何事件，所以「只靠后端回推」的实现会红。

**如果这一步就找到了原因** → 修完补/扩 `e2e/send-repro.spec.ts`，然后回去看第 2 步是否也需要。

---

## 第 2 步：事件订阅 —— 后端 emit 的事件，前端有人在听吗？

目标：确认契约。这是本仓库**出过问题的地方**（`known-issues.md` 第 1 条：`agent.delta` 从未被订阅，README 宣称的流式渲染实际不工作）。

最快的判据：跑契约测试。

```bash
go test ./ -run 'TestEveryBackendEventIsSubscribed|TestCamelCaseMatchesGeneratedNames' -v
```

它做的是：扫根目录所有 `*.go` 里的 `mygo.NewEvent[T]("x.y")`，转成 camelCase，断言 `src/lib/useBackend.ts` 或 `src/mini.tsx` 里出现 `events.<camelCase>`。

红灯 → 直接照报错补订阅（或删掉没人用的事件声明）。

绿灯但症状还在 → **注意护栏的盲区**：它是静态子串匹配，测不出下面三种。手动查：

1. **订阅了但字段名接错**。前端 `sub(events.agentMessage, ({ id, message }) => ...)`，后端 emit 的 map key 是 `agentId` / `text` → 拿到 `undefined`，state 更新成一个空对象，界面「没反应」。
   ```bash
   # 后端 emit 的实际 key
   grep -n 'emit("agent.message"' internal/agent/manager.go
   # 前端解构的 key
   grep -n 'agentMessage' -A6 src/lib/useBackend.ts
   ```
   也看 `main.go` 的 `NewEvent[...]` 结构体定义（`main.go:60-79`）—— 那是 TS 类型来源。
2. **订阅确实跑了但 setState 没触发重渲染**。state 存在但组件读的是另一份（例如读的是 prop 快照而非 store）。在 `sub()` 的 handler 里 `console.log` + 在渲染函数里 `console.log`，两次日志对不上就是这个问题。
3. **订阅有条件**。所有 `sub()` 都注册在一个 `useEffect` 里，`sub` 的实现是 `unsubs.push(evt.on((p) => alive && cb(p)))`（`src/lib/useBackend.ts:73-75`），**依赖数组是 `[]`**（`:229`）。两个容易踩的坑：
   - `alive` 被 cleanup 置 `false` 后，所有回调**静默丢弃**。React 18 StrictMode 下 effect 会执行两次（mount→unmount→mount），如果 cleanup 先跑了而第二次 effect 没重建订阅（依赖数组写错成 `[something]` 且 `something` 不变），就会永久静默。表现：**开发模式下所有事件都收不到**。
   - `alive` 是闭包变量，若在 `sub()` 里被捕获成真值后 cleanup 没生效，会向已卸载组件 `setState`（React 会警告但旧版会静默）。

---

## 第 3 步：后端 emit —— 事件真的发出去了吗？还是根本没走到 emit？

在 emit 之前打日志，或直接用现有的 http/CLI 路径观察。

```bash
# 列出所有 emit 点，并数一下有没有对应的前端订阅
 grep -rn '\.Emit(\|m\.emit(' main.go internal/agent/manager.go
```

关键判断：**这行 emit 所在的代码路径，在用户的场景里能走到吗？**

本仓库踩过的坑：

- `internal/agent/demo.go`（已删）在 mock 路径伪造 `todo.update` / `file.change` / `tool.start` —— 看起来"后端有事件"，实际没有任何真实功能在跑。
- `internal/agent/manager.go:766`（`if channelID != ""` 才 emit `agent.channel.reply`）—— 单聊场景下这个事件**永远不会发**。如果 UI 靠它更新，就是「单聊没反应、群聊正常」。同文件 `:651` 是 mock 路径的同一判断。
- 早退分支：`if runCtx.Err() != nil { a.setIdleOrQueued(m); return }`（`internal/agent/manager.go:749-752`）。取消后直接 return，**后面的完成态 emit（`agent.message` 的 assistant 那条）全被跳过**。如果 UI 靠完成态事件收尾，取消场景就会永远停在中间态。

诊断手段：所有 agent 事件都经 `main.go:1042` 的 `emitToWindow` 转发到窗口。**在这个函数第一行加日志**，就能拿到完整事件序列（`channel` 包的 `ChannelMessage.Emit` 是另一条路径，需要单独在 `main.go` 的 `SendToChannel` 里看）：

```go
// main.go  emitToWindow 第一行，临时
log.Printf("emit: %s %+v", event, data)
```

跑一次用户的操作，对照预期看事件序列。

---

## 第 4 步：后端阻塞点 —— worker 是不是被挂死了？

症状特征：**第一次操作就没反应，之后所有操作都没反应**；`queueLen` 一直涨；状态停在 `running` 或 `queued`。

worker 模型：`internal/agent/manager.go:552` 的 `workerLoop`，每个 agent 一个 goroutine，`for { select { <-a.done / req := <-a.queue } }`，**串行**执行。所以**任何一个 `execute` 卡住 = 该 agent 永久瘫痪**，队列里的消息全部排队。这就是「停止/排队卡住」。

### 4.1 先定位卡在哪

```bash
# 运行中抓 goroutine 栈（这是最快的定位手段）
# 在 main.go 临时注册一个 debug 端点，或直接给进程发 SIGQUIT 拿完整栈
kill -QUIT <pid>          # 栈会打到 stderr
```

看 `execute` / `executeMock` / `executeCLI` 那几帧停在哪。

已确认的阻塞点清单（按历史发生顺序）：

| 位置 | 阻塞形态 | 对应问题 |
|---|---|---|
| `internal/llm/openai.go` 的 `httpClient.Do(req)` | 上游接受连接但永不响应 → `Do()` 永不返回 | 第 4 条（`Timeout:0`） |
| `internal/agent/manager.go:707` `for ev := range events` | runner 的 `close(out)` 不发生 → range 永不结束 | 第 5 条 |
| `internal/cli/runner.go` 的 `scanner.Scan()` | 子进程被 kill 了但**孙进程**还持有 stdout → 读不到 EOF | 第 5 条 |

### 4.2 逐个检查「超时必须存在」

```bash
# 1. HTTP 客户端是否显式设置了超时？Stream 必须有 deadline 兜底
sed -n '105,130p' internal/llm/openai.go
```

判据（`internal/llm/openai.go:113-120`）：`Stream` 必须检查 `ctx.Deadline()`，调用方没给就自己套 `context.WithTimeout(ctx, StreamTimeout)`（`StreamTimeout = 120s`，在 `internal/llm/llm.go:51`）。
**出现 `&http.Client{}` 且配一个没有 deadline 的 ctx = 就是第 4 条 bug 回来了。**
注意 `internal/llm/openai.go:64` 的 `ListModels` 用的是 `&http.Client{Timeout: 10 * time.Second}`，两处写法不同是**有意的**：一个靠 ctx、一个靠 client，别「统一」掉。

```bash
# 2. 取消是否能穿透到子进程？
sed -n '60,75p'  internal/cli/runner.go   # ClaudeRunner.Run
sed -n '200,215p' internal/cli/runner.go  # CodexRunner.Run
```

判据：`exec.CommandContext` **默认只 kill 直接子进程**。CLI（claude/codex 都是 node）会 fork 一堆孙进程，它们继承并持有 stdout，scanner 因此读不到 EOF，`close(out)` 不发生。必须有进程组级别的 kill。

回归护栏（红灯就说明取消没穿透）：
```bash
go test -race ./internal/cli/ -run TestCancelKillsProcessGroup -v
go test -race ./internal/agent/ -run TestStopCLIBackendTerminatesStream -v
go test -race ./internal/llm/   -run TestStreamTimesOutOnHangingUpstream -v
```

### 4.3 停止语义的三条不变量

`Stop` 正确当且仅当同时满足：

1. 状态回到 `idle` **并且不被 worker 的迟到事件覆盖** → `internal/agent/manager_test.go` 的 `TestStopReturnsToIdleAndStaysThere`（断言事件序列**最后一条**是 idle）。
2. 事件流关闭 / 子进程退出 → `internal/agent/stopcli_test.go`、`internal/cli/proc_test.go`。
3. 取消不泄漏到下一轮 → `internal/agent/manager_test.go` 的 `TestPromptAfterStopWorks`。

只有第 1 条绿、第 2 条红，就是本仓库**最容易误判的状态**：`StopAll()` 会把 `Status` 直接写成 `StatusIdle`（`internal/agent/manager.go:861-866`），所以**光看状态是看不出问题的**。

---

## 修完之后（强制）

1. 在 [`../known-issues.md`](../known-issues.md) 记一条五段式记录；
2. 把「这次是靠什么手段定位的」写进 `为什么没被及早发现`（例如：没有扮演"上游挂起"这个状态 / 只在状态字段上断言 / mock 自动回推事件）；
3. 补护栏，并且**先证明护栏能红**：把修复回退，跑护栏，必须失败。不红的护栏不算护栏。
4. `npm run verify` 全绿再提交。
