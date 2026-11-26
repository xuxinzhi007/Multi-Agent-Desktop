# 已知问题清单

按「再次出问题时的排查价值」排序，不是按时间。每条结构见 [`README.md`](README.md#2-每条记录的结构五段缺一不可)。

格式约定：`文件:行` 指当前 HEAD 的位置；`git show <hash>` 是原始证据。**本文档所有根因均已对照提交 diff 与代码核实。**

| # | 问题 | 护栏层 |
|---|---|---|
| 1 | 事件契约断裂：后端 emit、前端无人订阅 | Go test |
| 2 | channel 包数据竞争（返回值泄漏内部指针） | Go test `-race` |
| 3 | 亮色主题发灰 | e2e |
| 4 | 停止 / 排队卡住：`http.Client Timeout:0` 打满 worker | Go test |
| 5 | 停止按钮失效（CLI 子进程不退出） | Go test |
| 6 | 群聊白屏：`TextareaAutosize` 不接受 `maxHeight` | e2e |
| 7 | 消息发送后界面无反馈（无乐观更新） | e2e |
| 8 | typecheck 空转：根 tsconfig 是 references-only | CI 脚本 |
| 9 | `[ch-xxx]` / `你: ` 等内部标记泄漏到界面 | 无（见待办） |
| 10 | 假审批状态：mock 伪造 `tool.start` 把 agent 锁在 blocked | Go test |
| 11 | `nil` 切片 → `null` → 前端 `.length` 崩溃 | e2e |
| 12 | Hooks 顺序不一致导致白屏（两次） | 无（见待办） |
| 13 | `sync.Mutex` 值拷贝（`go vet` 报 copies lock） | `go vet` |
| 19 | 聊天发出去没有任何回应：`Stream` 的 `defer cancel()` 在返回瞬间取消读流 | Go test |
| 21 | ACP 会话一启动就失败：配的命令根本不是 ACP agent（`pi`） | 启动前探针 |
| 22 | 浏览器打开只有假数据，还多出一套 `/remote` 页面 | Go test + 单页判定 |
| 23 | 桌面已处理完的审批，网页还挂着；点下去报「审批已失效或已完成」 | 对账（连上/聚焦/失效即收卡） |

---

## 1. 事件契约断裂：后端 emit、前端无人订阅

- **现象**：后端逻辑在跑，前端状态永远是空数组。README 宣称的 token 级流式渲染实际不工作 —— 流式文本区一直是空的。另外悬浮窗点 agent 切不回主窗。
- **根因**：`internal/agent/demo.go`（已删除，见 `git show 11d5b36`）每轮 mock 对话伪造 `todo.update` / `file.change` / `tool.result` / `tool.start`，**前端一个都没监听**；而真正需要的 `agent.delta` 反而从未被订阅。`mygo generate` 只保证 Go 侧事件名同步到 `src/mygo.ts`，不保证「前端真的 `sub()` 了」。
- **为什么没被及早发现**：类型系统在两侧各自成立 —— Go 有 `mygo.NewEvent[T]` 的强类型，TS 有生成的 `events.agentDelta`，但**没有任何东西把两者连起来**。`tsc` 不检查未使用的生成成员，`go test` 不跑前端，e2e 用 mock runtime 替代了真实事件流（`e2e/mock-runtime.ts` 里 `mygo.emit` 是测试自己 emit 的），所以真实契约断了也测不出来。
- **解决方案**（`git show 11d5b36`）：
  - 删除 `internal/agent/demo.go`，不再伪造事件；
  - 新增 `main_contract_test.go`：扫根目录 `*.go` 里所有 `mygo.NewEvent\[[^\]]*\]\("([^"]+)"\)`，转成 camelCase，断言 `src/lib/useBackend.ts` 或 `src/mini.tsx` 里出现 `events.<camelCase>`；
  - 护栏第一次跑就抓出 `agent.delta` 未订阅，补上；同时补 `agent.focused` 订阅。
- **预防措施与护栏**：
  - `main_contract_test.go` → `TestEveryBackendEventIsSubscribed`：新增事件不订阅直接红。
  - `main_contract_test.go` → `TestCamelCaseMatchesGeneratedNames`：Go 事件名 → TS 名字的转换规则本身也被固定，防止规则漂移导致误报漏报。
  - `README.md` 开发约定已写明：「新增事件必须同时有前端订阅，否则 `TestEveryBackendEventIsSubscribed` 会失败」。

护栏：`main_contract_test.go`（`TestEveryBackendEventIsSubscribed`、`TestCamelCaseMatchesGeneratedNames`）

**注意**：这条护栏是**静态子串匹配**，只防「完全没订阅」。防不了「订阅了但字段名接错」「订阅了但 handler 里判断错」。后者要靠 playbook 的人工排查（见 `playbooks/no-ui-feedback.md` 第 2 步）。

---

## 2. channel 包数据竞争（返回值泄漏内部指针）

- **现象**：`go test -race` 在 `SendToChannel` 路径上报数据竞争。接口看起来正常，但在并发下读到撕裂的数据；`SetDeliveries` 前身 `UpdateMessage` 会用旧快照整体覆盖消息，丢掉并发写入的新字段。
- **根因**（`git show 11d5b36`，`internal/channel/channel.go`）：
  - `Create/Get/List/AddMessage/Broadcast` 直接把 `*Channel` / `*Message` 交给锁外调用方。写路径的 `append` 会让底层数组 realloc，调用方持有的指针因此与存储共享内存。
  - `UpdateMessage(msg Message)` 整体覆盖消息，调用方快照过时就会覆盖别人的写入。
- **为什么没被及早发现**：`go test` 默认不开 race detector。功能测试全绿，因为竞争条件在单 goroutine 顺序执行下不显形；接口签名 `(*Channel, bool)` 看起来「正常」，没有人在 code review 时把「返回指针」当成缺陷。
- **解决方案**：
  - 新增 `(*Channel).clone()` / `(Message).clone()` 深拷贝，**所有对外方法返回副本**（`Deliveries` 切片也必须断开）；
  - `UpdateMessage` 改为 `SetDeliveries(channelId, msgID, deliveries)`，只写投递字段。
- **预防措施与护栏**：
  - `npm run verify` 里是 `go test -race ./...`（**不是 `go test`**）—— 竞争只在这条命令下才会红。
  - `internal/channel/channel_test.go` 的三个所有权用例：`TestReturnedMessageIsDetachedFromStore`、`TestGetReturnsCopy`、`TestBroadcastMessageSurvivesConcurrentUpdateAndAppend`。
  - `README.md` 开发约定：「跨 goroutine 的返回值必须是副本（`channel` 包的 `clone()`），不要泄漏内部指针」。

护栏：`internal/channel/channel_test.go`（3 条所有权用例）+ `go test -race`

---

## 3. 亮色主题发灰

- **现象**：切到亮色主题，整个界面像蒙了一层灰雾；侧栏明显发脏；卡片浮不起来没有层次。
- **根因**（`git show cd59bdd`，`src/index.css`）—— **三个问题叠加，每个单独看都不明显**：
  1. 底色本身是中性灰 `--bg-base: #f2f3f5`，而卡片是纯白 `#ffffff`。灰底上浮白卡片，既脏又没层次。
  2. 玻璃层 `--bg-glass` **只定义了暗色值** `rgba(16,19,24,.72)`，亮色主题下没有覆盖，于是继承了这层半透明黑 —— 等于在亮界面上盖了层黑纱。这是「发灰」最直接的来源。
  3. 氛围层暗角 `--vignette` 用 `var(--bg-base)` 往四周铺。暗色下是自然暗角，**亮色下就是在四周压出一圈灰雾**。网格线同理，亮色下用白线完全不可见。
- **为什么没被及早发现**：**没有任何测试看颜色。** 主题是 CSS 变量驱动（`src/index.css` 的 `:root` 与 `:root[data-theme='light']` 两套），缺一个变量不报错、不崩溃，只是"看起来不对"。人工看也不容易判断「灰」到什么程度算 bug —— 这是主观感受，没有阈值就没有断言。
- **解决方案**：
  - `--bg-base: #fafbfc`（冷调近白），`--bg-chrome: #ffffff`，靠边框分层；
  - 亮色单独定义 `--bg-glass: rgba(255,255,255,.76)`；
  - 把暗角拆成 `--ambient-vignette`（暗色 = 底色，亮色 = `rgba(226,232,240,.55)`），网格拆成 `--ambient-grid`（暗色白线 / 亮色黑线）。
- **预防措施与护栏**：把主观感受变成**可量的数值**，量实际渲染色而不是读 CSS 源码。
  - `e2e/theme.spec.ts` →「亮色主题底色是近白」：`getComputedStyle(document.body).backgroundColor` 的 r/g/b 必须 ≥ 245/246/247（中性灰特征是三通道接近且明显 < 250）。
  - 同文件 →「亮色主题下侧栏不是半透明黑」：`aside` 背景 r/g/b 都 > 200（继承暗色玻璃时数值会很低）。
  - 同文件 →「亮色主题氛围层暗角不是灰雾」：`--ambient-vignette` 不得包含 `var(--bg-base)`，且两个 ambient 变量非空。
  - 同文件 →「两套主题的文字对比度都达标」：亮/暗各算 WCAG 相对亮度比，必须 > 7:1。
  - 布局副作用也在 `e2e/fleet.spec.ts` 固定了（卡片不出视口、检查器默认不吃对话区空间）。

护栏：`e2e/theme.spec.ts`（4 条）+ `e2e/fleet.spec.ts`（2 条）

---

## 4. 停止 / 排队卡住：`http.Client Timeout:0` 让 worker 永久阻塞

- **现象**：发消息后一直显示「排队中」，永远不动；队列里后面的消息也一起僵死。上游（本地 ollama 挂起、或 provider 无响应）一次卡住，整个 agent 就废了。
- **根因**（`internal/llm/openai.go:113-120`）：`Stream` 用的是没有截止时间的 `ctx`（`internal/agent/manager.go:573` 的 `context.WithCancel(context.Background())` 只带取消、不带 deadline），HTTP 客户端也没有 `Timeout`。上游挂起时 `client.Do()` **永不返回**，`execute` goroutine 被永久占住，`workerLoop`（`internal/agent/manager.go:552`）再也取不到下一条队列消息。
- **为什么没被及早发现**：mock provider（`Provider: "mock"`）从不发网络请求，本地 ollama 正常情况下毫秒级返回。**只有「上游挂起」这个状态才会出事，而所有测试都没有扮演过一个「接受连接但永不响应」的服务器。** 没有超时断言，测试永远绿。
- **解决方案**（`internal/llm/openai.go`）：
  - `Stream` 里检测 `ctx.Deadline()`：调用方没给 deadline 就自己套 `context.WithTimeout(ctx, StreamTimeout)`（`StreamTimeout = 120s`，定义在 `internal/llm/llm.go:51`）；
  - 空 API Key 的非 ollama provider 立刻返回 `ErrMissingAPIKey`，不再把请求发出去等超时；
  - `ListModels` 单独用 `&http.Client{Timeout: 10 * time.Second}`（`internal/llm/openai.go:64`）。
- **预防措施与护栏**：
  - `internal/llm/openai_test.go` → `TestStreamTimesOutOnHangingUpstream`：`httptest.NewServer` 的 handler 里 `<-blocker` 永不响应，断言 `Stream` 在 `StreamTimeout + 3s` 内返回错误。「接受连接但永不响应」这个状态现在是测试里的第一类公民。
  - `internal/llm/openai_test.go` → `TestStreamRejectsMissingAPIKey`：缺 Key 要快速失败，并用 `called` 标记断言请求根本没发出去。
  - 排查顺序见 `playbooks/no-ui-feedback.md` 第 4 步。

护栏：`internal/llm/openai_test.go`（`TestStreamTimesOutOnHangingUpstream`、`TestStreamRejectsMissingAPIKey`）

---

## 5. 停止按钮失效（CLI 子进程不退出）

- **现象**：点停止没反应，agent 一直在跑；或者状态回到 idle 了但 `claude` 进程还在后台。悬浮窗点停止直接报错（`selected` 为 `null` 时 `API.stopAgent(selected)` 没有守卫，见 `git show e42b951`）。
- **根因**（`internal/agent/manager.go` + `internal/cli/runner.go`）：
  - 前端：`API.stopAgent(selected)` 里 `selected` 可能为 `null`，没有守卫。
  - 后端：`Stop` → `StopAll` → `a.stop()` 只 `cancel()` 掉 `runCtx`。`executeCLI`（`internal/agent/manager.go:664`）在 `for ev := range events` 上阻塞，事件流是否关闭取决于 runner。`exec.CommandContext` 默认只 kill 直接子进程，**孙进程（node 会 spawn 一堆）活着并继续持有 stdout**，scanner 读不到 EOF，`close(out)` 不发生，range 永不结束。
- **为什么没被及早发现**：停止语义只被「状态字段」验证过 —— 状态是 `idle` 就算过。`StopAll` 里甚至有一行直接把 `Status = StatusIdle`（`internal/agent/manager.go:861-866`），于是**即使 worker 还卡着，状态看起来也是对的**，测试和 UI 都显示「已停止」。真正的判据「事件流是否关闭 / 子进程是否退出」没人断言。
- **解决方案**：
  - 前端去掉没守卫的 `stopAgent(selected)` 调用与半成品 `pendingStop` 状态；
  - `PromptAgent` / `RunShell` 接受 `ctx`，去掉 `context.Background()`；
  - CLI 子进程按进程组取消（见 `internal/cli/runner.go` 的 `Run`）。
- **预防措施与护栏**：
  - `internal/agent/manager_test.go` → `TestStopReturnsToIdleAndStaysThere`：停止后 400ms 状态必须仍是 idle，**且事件序列最后一条必须是 idle**（防止被 worker 的新状态事件覆盖）。
  - `internal/agent/manager_test.go` → `TestPromptAfterStopWorks`：停止后立刻发新消息必须能跑完（防止取消泄漏到下一轮）。
  - `internal/agent/stopcli_test.go` → `TestStopCLIBackendTerminatesStream`：真的起一个 CLI backend，`Stop` 后 3 秒内必须回到 idle；否则说明事件流没结束、子进程还活着。
  - `internal/cli/proc_test.go` → `TestCancelKillsProcessGroup`：`cancel()` 后 8 秒内事件流必须关闭（否则 = 子进程仍持有 stdout），并 `pgrep` 检查残留进程。

护栏：`internal/agent/manager_test.go`（2 条）、`internal/agent/stopcli_test.go`、`internal/cli/proc_test.go`

---

## 6. 群聊白屏：`TextareaAutosize` 的 `maxHeight`

- **现象**：点进任何频道，整个窗口纯白，没有任何错误信息，窗口像是卡死了。
- **根因**（`git show 2e01338`）：`ChannelView` 给 `react-textarea-autosize` 的 `<TextareaAutosize>` 传了 `style.maxHeight: 120`（外加 `minHeight` / `overflowY`）。这个库**在渲染阶段直接 throw**：

  ```
  Using `style.maxHeight` for <TextareaAutosize/> is not supported.
    at ChannelView (ChannelView.tsx:11:39)
  ```

  React 里组件抛异常且没有 ErrorBoundary，**整棵 React 树被卸载** → 白屏。而它本来就有 `maxRows={6}`，`maxHeight` 完全多余。
- **为什么没被及早发现**：
  1. 群聊视图**没有任何 e2e 或单测**覆盖（当时只有 7 条 playwright 用例，smoke 只打开了主框架，没点进频道）。
  2. 没有 ErrorBoundary。React 生产模式下把错误吞进白屏，**连"哪里错了"都不给**。这次是靠事后新增的 ErrorBoundary 才一眼看到根因的——也就是说：**bug 已经存在，是诊断能力的缺失让它的生命周期变长了。**
- **解决方案**：
  - 删掉 `style.maxHeight`（以及多余的 `minHeight` / `overflowY`），只保留 `maxRows={6}`；
  - 新增 `src/components/ErrorBoundary.tsx`，兜住整棵树，显示错误信息、组件栈、「重试」「重新加载」，以及一句「你的数据没有丢失」；
  - 在 `src/main.tsx` 挂上。
- **预防措施与护栏**：
  - `e2e/channel.spec.ts` →「点进频道不白屏，且渲染出已有消息」：监听 `page.on('pageerror')`，断言错误数组为空 **且** 消息可见（白屏回归）。
  - `ErrorBoundary` 本身是**通用诊断护栏**：任何组件抛异常都会显示可读原因，而不是白屏。后续所有「白屏」问题都先看它。
  - `e2e/smoke.spec.ts` →「应用能渲染出主框架（不是白屏）」是兜底。

护栏：`e2e/channel.spec.ts`（白屏回归）、`e2e/smoke.spec.ts`、`src/components/ErrorBoundary.tsx`（诊断能力）

---

## 7. 消息发送后界面无反馈（完全依赖后端事件回推）

- **现象**：在输入框敲完消息按回车，屏幕上什么都没出现。要等后端 `agent.message` 事件回来才有反应；后端慢 / 卡 / 事件丢了，用户就以为「发出去没反应」，于是重复发送。
- **根因**：`useBackend` 的 `agent.prompt` 分支只 `return API.promptAgent(...)`，**没有任何本地状态写入**。消息进入 UI 的唯一路径是后端 emit `agent.message`（`internal/agent/manager.go:578` 的 `m.emit("agent.message", ...)` → 前端 `src/lib/useBackend.ts:109` 的 `sub(events.agentMessage, ...)`）。这条路径上的任何延迟都会直接表现为「输入框清空了但消息没了」。
- **为什么没被及早发现**：所有测试都在**有响应**的后端上跑 —— mock provider 毫秒级返回，`e2e/mock-runtime.ts` 的 `API.PromptAgent` 是同步桩。**从来没有人测过「用户按下回车后 0ms 的那一刻屏幕上有什么」**，因为「等后端回推」在快后端上和「乐观更新」看起来完全一样。
- **解决方案**（工作区未提交的 `src/lib/useBackend.ts` 改动）：新增 `optimisticMessage(agentId, text, role)`，在 `agent.prompt` 分支里**先本地插入消息并置 `pending[agentId] = true`**，再调 `API.promptAgent`；失败时清掉 pending 并抛出（不假装成功）。后端回推时按内容去重。
- **预防措施与护栏**：
  - `e2e/send-repro.spec.ts` →「发送后消息立刻出现在对话里」：`fill` 文本 + `press('Enter')`，然后断言该文本 **可见**。它不模拟后端回推，因此「只靠后端事件」的实现会红。
  - 注意：`e2e/mock-runtime.ts` 的 `API.PromptAgent` 是桩，不会 emit `agent.message` —— 这正是这条用例能生效的原因，**别把 mock 改成自动回推**。

护栏：`e2e/send-repro.spec.ts`

**相关缺口（未闭环）**：`pending` 标记目前只在失败时清除，成功时依赖后端事件；「输入框清空 + 消息未出现 + 无任何 loading」的竞态仍有残余。补护栏时优先考虑「发送后 500ms 内必须能看到自己的消息或 pending 指示」。

---

## 8. typecheck 空转：根 tsconfig 是 references-only

- **现象**：`npm run typecheck` 一直绿，但改代码时该报的类型错误一个都不报。修掉这条之后，**立刻暴露出 8 个一直被掩盖的真实类型错误**（`send` 返回联合类型、`ToolList` 缺少 `onApprove`、`WebkitBoxOrient` 字面量写错等）。
- **根因**（`git show e42b951`、当前 `tsconfig.json`）：

  ```json
  { "files": [], "references": [{ "path": "./tsconfig.app.json" }, { "path": "./tsconfig.node.json" }] }
  ```

  根 `tsconfig.json` 的 `files` 是空数组、只有 `references`。`tsc --noEmit` 读根配置 → **没有任何输入文件 → 什么都不检查 → 退出码 0**。真正的配置在 `tsconfig.app.json`（`include: ["src"]`）。
- **为什么没被及早发现**：**空转的检查比没有检查更危险。** 没有 typecheck 人会小心，有一个"通过"的 typecheck 会让人相信它覆盖了。它退出的方式是完全正常的绿灯，`npm run build:web` 里的 `tsc --noEmit && vite build` 也照样构建成功（vite 用 esbuild 转译，不做类型检查），两层都静默。
- **解决方案**（`package.json`）：

  ```json
  "typecheck": "tsc -p tsconfig.app.json --noEmit",
  "build:web": "npm run typecheck && vite build",
  "verify": "npm run typecheck && go vet ./... && go test -race ./... && npm run test:e2e"
  ```

  同一个提交里修掉了暴露出的 8 个真实类型错误。
- **预防措施与护栏**：**光有 CI 步骤不够 —— 它只会一直绿。** 需要定期证明检查器真的在看东西：
  - 每次新增目录（如 `src/` 之外的新代码根）时，确认它被 `tsconfig.app.json` 的 `include` 或某个 `references` 覆盖；
  - 冒烟验证方法：临时注入一个明显错误（例如 `const x: number = 'x'`）到 `src/` 下任意文件，跑 `npm run typecheck`，**必须失败**。绿灯就说明又空转了；
  - `npm run verify` 是唯一被认可的合并前检查。

护栏：`package.json` 的 `typecheck` 脚本（`tsc -p tsconfig.app.json --noEmit`）+ 手动「注入错误必须红」冒烟

---

## 9. `[ch-xxx]` / `你: ` 等内部标记泄漏到界面

- **现象**：消息气泡里出现 `[ch-1794486860541966000]`、`[from agent-x]`、`[user]`、`你: 在吗` 这类前缀，直接暴露给用户。
- **根因**（`git show e42b951`）：旧版把**频道 ID 和 speaker 前缀拼进了 prompt**，这些数据已经落到 `agents.json` 的历史消息里。后端 prompt 不再拼之后，**旧数据不会因为代码改进而消失**。
- **为什么没被及早发现**：只改了「新数据的产生路径」，没有处理「已存在的数据」。测试全部用新造的数据，历史数据形态没有出现在任何 fixture 里。
- **解决方案**：
  - 新增 `src/lib/message.ts`：`parseMessage()` 统一剥离 `[from|to x]`、`[ch-<digits>]`、`[user|assistant|system]`、`名字: ` 四类标记，最多循环剥 6 层（标记可能任意顺序叠在开头）；
  - 「名字: 」前缀只在剥完还有内容时剥，且前缀长度限制 1–24 字符，避免误伤 `注意: xxx` 这种正文；
  - 消息气泡与侧栏预览都走 `bodyOf()`；后端 `main.go` 的 `SendToChannel` 也不再往 prompt 里塞频道 ID（`main.go:407` 有注释「ID 是内部标识」）。
- **预防措施与护栏**：
  - **目前没有直接护栏**。`src/lib/message.ts` 只有 `src/components/AgentView.tsx` 一个调用方，没有单测。
  - 缺口已登记在第 10 节待办。补的时候最省事的形式是：在 `e2e/message-interaction.spec.ts` 里注入一条带 `[ch-179…]` 的历史消息（`mygo.emit('agent.message', ...)` 的写法已在该文件里有先例），断言页面不含 `[ch-` 子串。

护栏：无 —— 缺口已登记在下方待办

---

## 10. 假审批状态：mock 伪造 `tool.start` 把 agent 锁在 blocked

- **现象**：mock 对话结束后 agent 永远显示「在等人」（blocked），永远无人解除；界面上看起来「停止/继续」都失灵。
- **根因**（`git show 11d5b36`）：已删除的 `internal/agent/demo.go` 在每轮 mock 对话里伪造 `tool.start`，前端收到后调 `blockAgent` 把 agent 标成 blocked —— 而**真实审批流不存在**，没有任何路径会 `unblockAgent`。
- **为什么没被及早发现**：状态机只有一个「设置」方向被测试过，没有「不变量」测试（「mock 路径不应产生 blocked」）。`blocked` 是 `git show 9df2f19` 新增的状态，加状态时只加了 UI，没有加「谁能离开这个状态」的约束。
- **解决方案**：删除 `demo.go`；前端 `tool.start` 改为纯信息事件（`status=ok`），不再触发 `blockAgent`；`approveTool`/`rejectTool` 去掉基于 ID 正则反解析 `agentId` 的 `unblockAgent`。
- **预防措施与护栏**：
  - `main_test.go` → `TestMockPromptDoesNotLeaveAgentBlocked`：跑完一次 mock 对话，**只要过程中出现过 `blocked` 就失败**，并要求最终收尾为 idle 且消息数 ≥ 3。
  - `internal/agent/config_test.go` 里有 blocked 状态转移表（`queued 可以被阻塞` 等），新增状态时必须补全「谁能设置/清除」。

护栏：`main_test.go`（`TestMockPromptDoesNotLeaveAgentBlocked`）、`internal/agent/config_test.go`（状态转移表）

---

## 11. `nil` 切片 → `null` → 前端 `.length` 崩溃

- **现象**：进入频道白屏；侧栏「N 成员 · M 消息」直接崩。ChannelView 其他地方都写了 `?? []`，唯独侧栏这一处漏了。
- **根因**（`git show 2e01338`）：Go 序列化 `nil` 切片产生 JSON `null`（不是 `[]`）。`Sidebar` 直接读 `ch.members.length`，`null.length` 抛 TypeError → 整棵树卸载 → 白屏。
- **为什么没被及早发现**：前端 TS 类型由 `mygo generate` 从 Go 生成，生成类型里 `members: string[]` 是**非空数组**，TS 认为安全，所以 IDE 不会警告。而 `null` 只在 Go 侧真的持有 `nil` 切片时出现 —— 测试 fixture 里全是 `[]`。**类型系统在这里给了虚假的安全感。**
- **解决方案**：前后端都补空值防御（`?? []`），`Sidebar` 那处修正；`internal/channel/channel.go` 序列化时保证切片非 nil。
- **预防措施与护栏**：
  - `e2e/channel.spec.ts` →「频道里的成员被删除后，成员计数不崩溃」：mock 里显式给 `members: null, messages: null`，断言渲染出 `0 成员 · 0 消息` 且 `pageerror` 为空。**把 `null` 变成一类常规 fixture**，而不是意外。
  - 加新 Go 结构体字段时：切片/Vec 字段一律显式初始化，或前端一律 `?? []`。这是本仓库的既有约定（ChannelView 已如此）。

护栏：`e2e/channel.spec.ts`（`members: null` 用例）

---

## 12. Hooks 顺序不一致导致白屏（两次）

- **现象**：打开设置面板 / 选中 agent 时白屏。
- **根因**（`git show 6472757`、`git show 149e437`）：`if (!p.agent) return` / `if (!p.open) return null` **写在 `useState` 之前**，不同渲染路径下 Hooks 调用数量变化，React 报错并卸载整棵树。
- **为什么没被及早发现**：发生在 ErrorBoundary 之前，白屏无信息。且第一次修没形成规则，**12 天内在另一个组件里原样复发**（`6472757` 修 AgentView，`149e437` 修 SettingsModal）。这是本仓库「同类问题重复出现」的最典型样本。
- **解决方案**：把 `useState` 全部移到提前 `return` 之前，保证所有渲染路径 Hooks 顺序一致。
- **预防措施与护栏**：
  - 规则：**组件内提前 `return` 必须在所有 Hook 调用之后。**
  - `.oxlintrc.json` 里**已经配了** `"react/rules-of-hooks": "error"`，但 `package.json` **没有 `lint` 脚本**，`npm run verify` 也不调用它 —— 规则配了等于没启用。缺口见下方待办第 2 条。
  - 缓解：`ErrorBoundary` 现在会把这类错误原文显示出来（React 的 "Rendered fewer hooks than expected"），不再是白屏。

护栏：无 —— `.oxlintrc.json` 的 `react/rules-of-hooks` 规则存在但未被任何命令执行；缺口已登记在下方待办

---

## 13. `sync.Mutex` 值拷贝（`go vet` 报 copies lock）

- **现象**：`go vet ./...` 报警 `range var saved copies lock` / `return copies lock value`；潜在死锁与数据竞争。
- **根因**（`git show 8e690b2`）：`Agent` 结构体含 `sync.Mutex`，却被**按值传 / 按值返回**（`snapshot()` 返回 `Agent`、`Load()` 用 `[]Agent`、`agent.created` 事件直接 emit `agent.Agent`）。
- **为什么没被及早发现**：`go vet` 当时不在提交前检查链里；功能上值拷贝还能跑，只是不安全。
- **解决方案**：`snapshot()` 返回 `*Agent`；`main.go` 引入不含锁的 `AgentView` 供跨边界值拷贝；`ListAgents/CreateAgent/CreateCLIAgent` 返回 `AgentView`；`agent.created` emit `AgentView`；`Load()` 用 `[]*Agent`。现在前端拿到的都是 `Snapshot()` 副本（`internal/agent/manager.go:871`）。
- **预防措施与护栏**：`npm run verify` 里含 `go vet ./...`。含 `sync` 类型（`Mutex`/`Once`/`WaitGroup`/`RWMutex`）的结构体不允许出现在任何跨包导出签名里 —— 用 `Snapshot()` / `*View` 类型。

护栏：`go vet ./...`（在 `npm run verify` 中）

---

## 14. 灵动岛展开/收起「压缩后弹回」，内容被瞬间抽走

- **现象**：点击展开或收起时，窗口「啪」地一步跳到目标尺寸，CSS 过渡被打断；收起时内容还没开始动就被裁掉，看起来像「里面内容没来得及变化就直接切换了」。
- **根因**（三层叠加）：
  1. Go 侧 `Window.SetSize` 是**瞬时跳变**（mygo 只提供这一种），而 CSS 在跑 380ms 过渡 → 容器瞬间变小，内容被裁。
  2. 前端用单个布尔 `expanded` 控制，state 一翻转，内容和容器**同帧**改样式，没有「先退场再落位」的中间态。
  3. 卡片入场用的是 `animation: ... forwards`，它把 `opacity` 锁在 1。收起时只写 `animation: none` 会让它**立刻回落到基础值 `opacity: 0`** —— 于是内容被抽走而不是淡出。
- **为什么没被及早发现**：e2e 只断言「展开后能看到面板」「Esc 后回到折叠」，都是**终态**断言，测不出过程中间帧。手感问题必须采样中间帧才能量。
- **解决方案**：
  - Go 侧 `IslandResize` 改为逐帧推进（10ms 一帧，380ms），用 `easeSpring()` 复刻前端的 `cubic-bezier(.3,.9,.28,1.06)`，让容器和内容是同一个动作。
  - 前端引入四阶段状态机 `collapsed / opening / open / closing`，收起时先让内容退场，400ms 后才真正落回 `collapsed`。
  - 卡片退场改用独立的 `card-out` animation 接管（而不是 `animation: none`），并带 `calc(var(--i) * 26ms)` 的错峰延迟，形成「一层层收进去」。
  - 胶囊最后才出现：`.island--collapsed.island--phase-closing .island__pill` 组合选择器压过单类规则。
- **预防措施与护栏**：**终态断言不够，要采样中间帧**。
  ```ts
  // 每 20ms 采样宽度，断言出现 ≥3 个不同中间值
  const unique = [...new Set(widths)]
  expect(unique.length).toBeGreaterThanOrEqual(3)
  // 收起 80ms 后：胶囊还没出现、面板还在退场
  expect(Number(pillOpacity)).toBeLessThan(1)
  expect(Number(panelOpacity)).toBeGreaterThan(0)
  ```

护栏：`e2e/island.spec.ts` 三条（宽度渐变、卡片退场、「胶囊最后才出现」）

---

## 15. 工具超时被误报成「退出码 -1」，模型会重试同一个命令

- **现象**：`bash` 超时后返回 `status: ok` + `exit code -1`，而不是 `timeout`。集成测试 `TestBashTimeoutKillsProcessGroup` 断言 `StatusTimeout` 时第一次就红了。
- **根因**：被 SIGKILL 杀掉时 `cmd.Run()` 返回 `signal: killed`，它**同样满足** `*exec.ExitError`。代码先判断 `ExitError` 再判断 `ctx.Err()`，于是走了「非零退出」分支。
- **为什么危险**：`exit code -1` 会让模型以为命令写错了，于是重试同一个命令 —— 正好是「原地打转」的来源。控制点没被拿回来，只是换了个地方失效。
- **解决方案**：`Execute` 里**先查 ctx 再查 ExitError**。
  ```go
  if execCtx.Err() == context.DeadlineExceeded { /* StatusTimeout */ }
  if execCtx.Err() == context.Canceled { /* StatusTimeout */ }
  if ee, ok := runErr.(*exec.ExitError); ok { /* 真·非零退出 */ }
  ```
- **预防措施与护栏**：`TestBashTimeoutKillsProcessGroup` 直接跑 `sleep 30 & sleep 30`，断言 `StatusTimeout` + 5s 内返回。同时验证了进程组 kill 真的生效。

护栏：`internal/tool/builtin/builtin_test.go`

---

## 16. 停止时写了检查点，但写不进去

- **现象**：停止后 `store.Load` 拿到的 phase 还是 `thinking`，没有落盘。`TestIntegrationStopPersistsReason` 抓到的。
- **根因**：`checkpoint(ctx, st)` 传的是**已取消的 runCtx**，而 `FileStore.Save` 第一行就是 `if err := ctx.Err(); err != nil { return err }`，直接拒绝写入。**最需要写成功的时刻反而写不进去。**
- **为什么危险**：用户按了停止，界面上停了，但没有任何记录说明停在哪、为什么停。下次 resume 时预算和进度全丢。
- **解决方案**：`checkpoint` 不再接收运行时 ctx，内部用 `context.WithTimeout(context.Background(), 5s)`。落盘的生命周期与运行生命周期解耦。
- **附带修正**：`Stop(reason)` 记录的**用户原因优先**。之前循环自己的 "cancelled during model call" 会覆盖用户说的「我不想要这个改法」，而这两句话的含义完全不同。

护栏：`internal/loop/integration_test.go`（停止后断言 checkpoint 存在且 `StopReason` 含用户原因）

---

## 17. 发出去的消息一直「排队中」，永远没回复

- **现象**：桌面端发消息后状态停在 `queued`，不前进也不报错，看起来像卡死。
- **根因**（两层）：
  1. **worker goroutine 没有 recover**。`internal/cli/runner.go` 里写的是无检查的类型断言 `block["text"].(string)` —— CLI 输出格式一变（`text` 为 `null`、或换成数字）就 panic。
  2. panic 发生在**后台 goroutine** 里，没人 recover → **worker 永久死亡**。之后 `queue` 还在收消息但再没人消费，界面永远是「排队中」，且**没有任何错误提示**（panic 不在用户可见的路径上）。
- **为什么难发现**：mock backend 不经过 CLI 解析，走不到这条路径；而真实 CLI 的输出格式取决于外部工具版本，本地复现不了。
- **解决方案**：
  - 所有类型断言改成带 `ok` 的形式；
  - `workerLoop` 里加 `runSafely`，recover 后转成 `StatusError` 并把原因写进 `LastError`；
  - recover 后 `drainQueue()` 清空队列，避免后续消息重复触发同一个 panic。
- **预防措施与护栏**：**护栏的护栏** —— `TestWorkerSurvivesPanic` 在 `execute` 入口注入 panic，然后断言**第二条消息还能正常跑完**。我验证过：移除 recover 后这条测试立刻红。
  ```go
  // 第一轮 panic → 状态变 error
  // 第二轮必须能正常走到 idle —— 证明 worker 活着
  ```

护栏：`internal/agent/worker_recover_test.go`

**通用教训**：后台 goroutine 必须有 recover。它的 panic 用户看不见，症状会伪装成完全不同的问题（这里是"卡住"而不是"崩溃"）。

---

## 18. 群聊白屏：`Cannot read properties of undefined (reading 'slice')`

- **现象**：点进群聊直接白屏。报错位置 `ChannelBubble` 的 `name.slice(0, 1)`。
- **根因**：旧版本写入 `channels.json` 的消息缺 `fromName` / `fromId`。字段声明是 `string`，但 JSON 反序列化后是 `undefined`。渲染层照类型声明信了它。
- **为什么难发现**：类型声明说 `fromName: string`，TypeScript 编译期完全正确；只有真实旧数据才会触发。
- **解决方案**（两层，缺一不可）：
  - **渲染层兜底**：`msg.fromName || agent?.name || msg.fromId || '?'`。类型声明不代表运行时一定有值。
  - **加载层补齐**：`channel.Load` 时给缺失字段填默认值，让"缺字段"只在一个地方被处理。
- **预防措施与护栏**：`e2e/channel.spec.ts` 造一条只有 `text` 字段的 legacy 消息，断言页面不出现 `Cannot read properties` 且消息仍能显示。我验证过：回退渲染层兜底后这条立刻红，且报错与用户报告完全一致。

护栏：`e2e/channel.spec.ts`（旧数据缺 fromName / fromId 时不白屏）

---

## 待办：已知的护栏缺口

按「补起来最划算」排序。补完把对应条目从这一节删掉。

1. **`src/lib/message.ts` 没有单测**（第 9 条）。它是纯函数，最该有表驱动用例：`[ch-123] 你: 在吗` → `在吗`；`注意: 别忘了` 不能被剥成 `别忘了`；`[from a ][to b]` 多层叠加。
2. **Hooks 顺序护栏未接线**（第 12 条）。两次白屏都是这个原因。`.oxlintrc.json` 已经写了 `react/rules-of-hooks: error`，但仓库里没有 `oxlint` 依赖、没有 `lint` 脚本、`verify` 也不跑它。最省的修法：把 oxlint 加进 devDependencies，加 `"lint": "oxlint src"` 并串进 `verify`。
3. **事件契约护栏是静态子串匹配**（第 1 条）。防不住「订阅了但 payload 字段接错」。可在 `main_contract_test.go` 里再断言 `NewEvent[X]("...")` 的结构体字段与前端读取的 key 有一致性，或在 e2e 里用真实事件往返。
4. **乐观更新的 pending 收尾**（第 7 条）。成功路径依赖后端事件清 pending；需要一条「后端不回推时 UI 也不能永远 pending」的用例。
5. **动画时序只在 Web 环境验证**（第 14 条）。真机上 `Window.SetSize` 逐帧调用的平滑度、与 CSS 的帧同步没法在浏览器里复现，需要人工在 `npm run dev` 下确认。
6. **`e2e/mock-runtime.ts` 与真实后端行为漂移**。mock 是手写的（`API.ListAgents` 等都返回空数据），真实接口签名变了 mock 不会报错。建议定期用真实后端跑一遍关键路径。

---

## 19. 聊天发出去没有任何回应（空回复、无报错、状态还是 idle）

- **现象**：给 agent 发一条消息，自己的话显示出来了，然后**永远没有任何回应** —— 没有回复、没有报错、状态很快回到「空闲」。看起来像「模型没听见」，实际是回复被丢掉了。用 `loop` backend 时正常，只有普通聊天这条路坏，所以很容易被误判成「这个 provider 不行」。
- **根因**（`internal/llm/openai.go` 的 `Stream`）：调用方没给 deadline 时，函数内部套了 `context.WithTimeout(ctx, StreamTimeout)`，然后 **`defer cancel()`**。`defer` 是**函数返回时**执行，而 `Stream` 的返回值是一个 channel —— 它一返回就等于告诉调用方「流给你了」，此时后台 goroutine 才刚开始读 `resp.Body`。`cancel()` 立刻把请求 ctx 取消，`bufio.Scanner` 随之失败，一个 delta 都没读出来。
  结果是：`executeMock` 收到一条 delta 为空、只有 `Done` 的流 → 把空字符串追加进历史 → 状态正常回到 `idle`，**全程没有任何 error**。`internal/agent/manager.go:625` 传的正是 `context.WithCancel(context.Background())`，没有 deadline，必然走进这个分支。
- **为什么没被及早发现**：**所有已有测试都在用另一种 ctx。** `internal/llm/llamacpp_test.go` 的 `TestLlamaCppStream` 传的是 `context.WithTimeout(..., 60s)`，有 deadline，走的是 `else` 分支，`defer` 根本不存在；`TestStreamTimesOutOnHangingUpstream` 测的是超时，天然不关心内容。于是「测试全绿」与「真实使用全坏」同时成立 —— 真实 app 的入口条件（无 deadline）**从来没有被任何用例扮演过**。另外这条 bug 不崩溃、不报错、状态机自洽，所以状态机护栏和 e2e（走 mock runtime）也抓不到。
- **解决方案**（`internal/llm/openai.go` 的 `Stream`）：
  - `cancel` 交给读 body 的 goroutine 释放（`defer cancel()` 移进 goroutine），保证「流读完了才取消」，而不是「返回就取消」；
  - 调用方已给 deadline 时用一个空 cancel，两条路径保持一致；
  - `Do` 失败 / 非 200 时显式 `cancel()`，避免在没有 goroutine 的场景下泄漏。
- **预防措施与护栏**：
  - `internal/llm/openai_test.go` → `TestStreamCompletesWithoutDeadline`：用 httptest 造一个「先延迟 80ms 再吐两个 delta」的服务端，**传 `context.Background()`**，断言拼出来必须是 `收到`。延迟是为了让缺陷从偶发变成必现。
    已按「护栏的护栏」验证过：把修复回退后这条立刻红，报错正是 `流式内容丢失：得到 ""，期望 "收到"` —— 和用户看到的症状一致。
  - 规则：**测试一个流式接口时，必须至少有一条用例用「和生产完全一样的方式」构造 ctx**（这里是 `WithCancel(Background())`），不要所有用例都顺手加 deadline。

护栏：`internal/llm/openai_test.go`（`TestStreamCompletesWithoutDeadline`）

---

## 20. 偶发「发一条就没反应」：每次请求 new 一个 `http.Client`

- **现象**：多数消息正常，偶尔一条直接没反应（或要重发一次才好）。日志里是 `http: server closed idle connection` 或 `EOF`。
- **根因**（`internal/llm/openai.go` 的 `Stream`）：每次调用都 `httpClient := &http.Client{}`。`executeMock` 又是**每轮都 `llm.New(cfg)`**，于是连接池每轮重建：上一条连接的空闲池被丢在服务端，服务端超时关掉它，下一条请求恰好复用到这条已死的连接上就失败。Go 只对幂等的 GET 自动重试，**POST 不会重试**，所以这个抖动直接暴露给用户。
- **为什么没被及早发现**：单次请求几乎必然成功，只有「连续多轮 + 服务端刚好回收连接」才出现。功能测试每条都新建环境，e2e 走 mock（根本不发 HTTP），所以没人连续发过几十条。
- **解决方案**：
  - `llm.httpClient` 提成包级共享连接池，`openAICompat` 复用它（跨会话、跨轮次复用同一份连接）；
  - 新增 `(*openAICompat).do`：只对**连接层**错误（`EOF` / `ECONNRESET` / `server closed idle connection` / `connection reset by peer`）**重试一次**，并用 `req.GetBody` 重建请求体。流一旦建立就不再重试，避免重复输出。
  - 重试的安全性有实测依据：这些错误发生时服务端计数不增长（实测 60 并发请求里 1 次 `EOF`，服务端只处理了 59 次），即模型没有被真正调用，不会重复生成或重复计费。
- **预防措施与护栏**：`internal/llm/openai_test.go` → `TestStreamRetriesStaleConnection`：httptest 第一次请求 hijack 后直接 `Close()`，第二次正常返回，断言「不报错、内容完整、服务端恰好被请求 2 次」（既验证重试生效，也验证只重试一次）。

护栏：`internal/llm/openai_test.go`（`TestStreamRetriesStaleConnection`）

---

## 21. ACP 会话一启动就失败：配的命令根本不是 ACP agent

- **现象**：把某个会话的运行时切成 ACP、命令填 `pi`，点发送后立刻失败（或卡 30 秒后报 initialize 超时），
  界面上是「无法启动 ACP Agent（命令：pi）：…」；同一个会话切回 native 就正常。
- **根因**（`internal/runtime/acp/client.go:302-320` + 用户的 `agents.json`）：
  - 数据侧：`~/Library/Application Support/multi-agent-desktop/agents.json` 里「前端工程师」是
    `runtime=acp`、`acp={"name":"pi","command":"pi"}`。
  - 事实侧：`pi 1.4.0` **不是 ACP agent**。它的输出模式只有 `--mode text|json|rpc`（`pi --help`），
    `pi acp` 会退回打印帮助。实测把 `initialize` 的 JSON-RPC 帧喂给它的 stdin，它把这一行当成**用户提问**，
    回了一段中文散文（"在继续之前，我需要先澄清几件事…"），根本不存在 `result.protocolVersion`。
  - 因此客户端 `request(initCtx, "initialize", …)` 等的是 JSON-RPC 响应，拿到的是散文 → 解码失败或 30 秒超时。
    协议层没错，**配错程序**。
- **为什么没被及早发现**：ACP 的失败只表现为"界面上一句红字"，没有任何地方校验
  「这个命令是不是 ACP server」。`SetRuntime` 只要求 command 非空（`internal/agent/runtime_config.go:45-60`），
  空命令有中文报错、非 ACP 命令没有。测试用的是内存 transport（`client_test.go`）与自建假 agent，
  真进程只在 e2e 里出现过，所以"命令不是 ACP"这种最常见的错配完全落在测试缝里。
  （次要风险：从 Finder 启动的打包 app 继承的是 `/usr/bin:/bin:/usr/sbin:/sbin`，
  `npx`/`node` 在 `/opt/homebrew/bin` 下会直接 `executable file not found`；
  `StartProcess` 没有做 PATH 补全，见 `internal/runtime/acp/transport.go:112-122`。）
- **解决方案**：
  - 把命令换成真正的 ACP adapter（截至 2026-11-26 实测可用的版本）：
    - Claude Code：`npx -y @agentclientprotocol/claude-agent-acp`（0.112.0）
    - Codex：`npx -y @zed-industries/codex-acp`（0.16.0）
    - Gemini CLI：`gemini --acp`
    - Cursor：`cursor-agent acp`
  - 配置界面加「测试连接」：spawn 后发 `initialize`，**必须收到带 `protocolVersion` 的 JSON**才算通过，
    否则当场提示「这个命令不是 ACP agent，看起来像普通 CLI」，不落盘。
  - `StartProcess` 里把 `/opt/homebrew/bin`、`/usr/local/bin`、`~/.local/bin`、nvm 的 bin 追加进子进程 PATH，
    并在 stderr 里把 `executable file not found` 翻成中文可读提示。
- **预防措施与护栏**：`internal/runtime/acp/client_test.go` 新增
  `TestInitializeRejectsNonACPCommand` —— 用一个只把 stdin 原样 echo 成纯文本的假进程，断言
  `Start` 必须返回可读错误（"不是 ACP agent"），而不是挂到超时。

护栏：`internal/runtime/acp/client_test.go`（`TestInitializeRejectsNonACPCommand`）

---

## 22. 浏览器打开只有假数据，还多出一套 `/remote` 页面

- **现象**：用浏览器打开 `localhost:5173`，看到的是几个 demo agent（前端/后端、`~/code/app`），
  agent 名字、模型、消息全都和桌面不同；空状态还写着「这是预览」「浏览器里的会话是假的」。
  同时仓库里另有 `src/remote/RemoteWorkbench.tsx` + `/remote?api=…` 的只读页，等于**两套页面**。
- **根因**：
  - `src/main.tsx` 只有两级判定（宿主注入 / 否则预览），**没有第三条路**——
    浏览器里没有 `globalThis.mygo`，就直接装 `preview-runtime` 的假数据。桌面进程明明在跑、
    网关明明在 `127.0.0.1:8780` 上返回真实数据，前端却不去连。
  - 网关当时只有 7 个 read-ish 端点（`/api/agents`、`/api/events`、`/api/approvals`…），
    **没有通用方法入口**，所以"浏览器跑同一套界面"在协议层就不成立，只能另写一套只读页面来凑。
  - 结果：同一份 React 代码，在桌面和浏览器出现两套观感；用户判断成「网页是 mock 版」。
- **为什么没被及早发现**：预览替身在浏览器里"能跑、不报错"，验收只看「页面不白屏」，
  没有断言"浏览器拿到的 agent 名字必须和桌面一致"。`previewMode()` 的大量文案分支
  （`Sidebar.tsx:591-593`、`AgentView.tsx:1815`、`ChannelView.tsx:319`、`FleetView.tsx:99`）
  反而把这套降级当成正常状态写在了界面上。
- **解决方案**：
  - 网关新增 `POST /api/rpc {method, args}`，反射到 `*API`（`internal/gateway/rpc.go`）；
  - 前端新增 `src/lib/mygo-http.ts`：装一个与宿主同签名的 `globalThis.mygo`
    （`call` → `/api/rpc`，`on/once` → SSE），**组件代码零改动**；
  - `main.tsx` 改三级判定：宿主 → 网关 → 预览，并把 `/remote` 那一套（`RemoteWorkbench`、
    `remote-api.ts`、`docs/remote-workbench.md`）删掉，只留一套页面。
- **预防措施与护栏**：
  - `internal/gateway/rpc_test.go`：位置参数、`context.Context` 注入与**不占位**、`(值, error)`、
    只有 error 的返回值、未知/非导出方法拒绝、缺参给零值。其中 `TestRPCInvokerSkipsContext`
    抓到了真实缺陷——ctx 占位导致 `PromptAgent(ctx, req)` 收不到 req，网页端发消息会全挂；
  - 浏览器实拍核对：标题 `Multi-Agent Desktop · Web`、`dataset.web=1`（无 `preview`）、
    「共 14 / 13 个已配模型」与桌面一致、控制台 0 错误（截图见 `docs/reviews/web-parity-2026-11-26.png`）。

护栏：`internal/gateway/rpc_test.go`（6 条）

---

## 23. 桌面已处理完的审批，网页还挂着

- **现象**：在桌面点了「本会话允许」之后，浏览器里那张审批卡还在，还显示「还有 3 个在排队」；
  在网页上再点一次，卡片里飘红 `API.DecideApproval 失败：审批已失效或已完成`。
- **根因**：**第二个客户端出现后才有的状态一致性问题**，不是单端逻辑写错。
  - 收卡依赖事件：`useBackend` 收到 `tool.approval.done` 才把卡移除（`:595`）。
  - 该事件走 SSE；但当时服务端**不做事件缓冲、不发 `id:`、不支持 `Last-Event-ID` 续传**
    （`internal/gateway/server.go` 的 `handleEvents` 只写 `event:` + `data:`）。
    SSE 一断一重连，断线期间的决定事件就永久丢了。
    → **已补**：`Broker` 保留最近 256 条（`replayLimit`），`Publish` 带 `Seq`；
    `SubscribeSince(since)` 返回补发段；SSE 每帧带 `id:`，重连时浏览器自动带 `Last-Event-ID`，
    服务端先补发再进实时通道（`sseWriteSeq`）。
  - 审批队列只在启动时拉一次（`API.listApprovals()`，`useBackend.ts:263`），
    **没有任何对账**；于是丢了事件就等于永远显示死卡。
  - 后端对已处理/已失效的 ID 返回 `审批已失效或已完成`（`internal/approval/approval.go:434`），
    这是幂等语义，前端却把卡留着并显示错误——用户看到的是"两次都失败"。
- **为什么没被及早发现**：桌面只有一个客户端，"决定"和"收卡"发生在同一个页面里，
  `decideApproval` 本地先 filter 掉，事件丢不丢都看不出来。加了网页端之后才暴露。
  同类问题在所有「CLI + IDE + 手机」双端产品里都存在（Claude Code 的 CLI/IDE 同步也靠事件补发与快照重拉）。
- **解决方案**（事件续传 + 三条低成本对账，都不做定时轮询）：
  1. 网关每次连上（含重连）会发 `gateway.connected` → 前端全量覆盖 approvals + agents；
  2. 窗口重新获得焦点 / 从后台切回可见 → 全量覆盖（跨端操作后的主路径）；
  3. 决定失败且原因是「已失效/已完成」→ 按已收卡处理（幂等），再把原因抛给界面。
- **预防措施与护栏**：
  - **事件续传**（`id:` + `Last-Event-ID` + 256 条缓冲），覆盖断线几秒的窗口；
  - **快照对账**（连上 / 聚焦 / 失效即收卡），覆盖超出缓冲的缺口。
  合起来的纪律是：**状态类列表（审批、队列、agents）一律"事件 + 快照"双通道**。
  只把事件当唯一真相，是这次问题的本质。

护栏：`src/lib/useBackend.ts`（`resync` 三条路径 + `decideApproval` 幂等收卡）、
`internal/gateway/broker_test.go`（补发顺序 / 缓冲上限 / `Last-Event-ID` 解析 4 条）、
`internal/gateway/server_test.go` 的 `TestSSEReplayHTTP`（端到端：先补发、再进实时通道）
