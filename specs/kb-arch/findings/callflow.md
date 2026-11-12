# 群聊 / 单聊：调用轴与现实行为走查（只读）

仓库：`/Users/xuxinzhi/Documents/gowork/multi-agent-desktop`（branch `main`）
方法：只读源码，沿 Sidecar → agent.Manager → channel.Manager → emitter → 前端 state 逐跳核对。
用词严格限定在 module / interface / implementation / depth / deep / shallow / seam / adapter / leverage / locality。
标注约定：**已证实**=源码逐行可复核；**推断**=源码可推出但未运行验证；**待确认**=缺运行证据或依赖外部组件行为。

---

## F1 — 用户说一句话：触发一个 fan-out，N 个成员各自独立跑，彼此完全看不见

**证据（文件:符号 行号）**
- `src/App.tsx:ChannelView onSend`（`App.tsx` 第二处 `s.send('channel.send', ...)`）→ 只传 `channelId/fromId/fromName/text`，一行一个 IPC 调用。
- `src/lib/useSidecar.ts:send` `case 'channel.send'` → `Sidecar.sendToChannel(...)`，`.catch` 后 `return null`（错误被吞）。
- `main.go:Sidecar.SendToChannel`，行 **318-376**：
  - `319` `s.channels.Broadcast(...)` 拿到 `recipients`
  - `332` `for _, agentId := range recipients`（**同步 for 循环，逐个 `Prompt`**）
  - `360` `prompt := fmt.Sprintf("[channel:%s] %s: %s", channelId, fromName, text)`
  - `361` `s.mgr.Prompt(context.Background(), agent.PromptRequest{...})`
- `internal/channel/channel.go:Broadcast` 行 **233-252**：纯成员表求差集（排除 `fromId`），**不排队、不并发、不等待**。
- `internal/agent/manager.go:Manager.Prompt` 行 **392-417**：把请求塞进**该 agent 自己的** `a.queue`（`make(chan PromptRequest, 32)`，见 `manager.go:229` 与 `:317`），**非阻塞**，满了返回 `queue full`。
- `internal/agent/manager.go:Agent.workerLoop` / `execute` 行 **445-485**：每 agent **一个 goroutine**，串行消费自己的队列；`execute` 内 `a.Messages = append(..., Role:"user")` 然后同步跑完 `executeMock`/`executeCLI`。

**实际后果**
1. 一次发送 = 在 `SendToChannel` 的同一个 goroutine 里对 N 个成员**顺序**调用 `Prompt`；`Prompt` 只是入队，所以用户侧「立即返回」，N 个 agent 各自 goroutine **并行**流式输出。这就是「5 个成员同时跑」的真实形态：5 个独立 goroutine、5 条独立 SSE/CLI 流、5 份独立 token 消耗。
2. 并发上限**只有队列容量 32/agent**，没有全局并发控制、没有速率限制、没有「群内 turn」概念。`executeMock` 里若 `cfg.Provider == "mock"` 还会额外 `m.demoTrail(a.ID, a.Name)`（`manager.go:501-503`，`internal/agent/demo.go:demoTrail`）——**每个成员都会注入一整套假的 todo/tool/file.change 事件**，5 个成员就是 5 套假事件打同一个 Inspector。
3. 「彼此看不见对方回复」是**必然**的：`executeMock` 只把**自己的** `a.Messages` 快照送给模型（`manager.go:493-496`），频道历史从不进入任何 agent 的 `Messages`（见 F3）。
4. 由于 `ChannelView` 的 `onSend` 不 await 也不展示错误（`useSidecar.ts:293` 吞异常），**用户对「有成员没收到」零感知**。

**判定**：已证实（1/2/3 为逐行可复核；5 个成员的并行度与 token 倍数为直接推论）。

---

## F2 — agent 回复回频道**不会**再触发任何 agent；判断条件是「`SendToChannel` 是唯一 fan-out 入口」

**证据**
- 写回路径：`internal/agent/manager.go:executeMock` 行 **545-552** 与 `executeCLI` 行 **632-639**，条件都是：
  `if channelId := extractChannelId(text); channelId != "" { m.emit("agent.channel.reply", ...) }`
  —— `text` 是**入队时的 `req.Text`**，只有 `SendToChannel` 在 `main.go:360` 拼过 `[channel:...]` 前缀。
- `extractChannelId` 行 **770-779**：纯字符串 `strings.Index(text, "[channel:")` + 第一个 `]`，即 `[channel:ch-xxx]`。**无转义、无长度校验、无「是否是会员/是否仍是成员」校验**。
- 消费端：`main.go:emitToWindow` 行 **870-878** `case "agent.channel.reply"` → `chMgr.AddMessage(...)` + `ChannelMessage.Emit(win, *msg)`。**它只写历史 + 推前端，从不调用 `SendToChannel`/`Broadcast`/`Prompt`。**
- 前端：`src/lib/useSidecar.ts:events.channelMessage` 只 `setState` 追加消息，无任何回调后端。

**实际后果**
- 不存在「A 回复 → 触发 B → 触发 A」的回声风暴：**回声风险被实现层面的「少一条边」规避，而不是被某条显式规则规避**。群聊实际是「单轮扇出（fan-out only）」。
- 副作用（真问题）：`docs/reviews/channel-design.md` 里规划的「agentA @agentB → agentB 回复」**完全没有落地**；agent 的回复即使文本里写了 `@B`，也只会躺在频道历史里，没有任何 module 去解析它。**「群聊」在行为上等于「并行单聊 + 一个公共黑板」。**
- 另一个副作用：`extractChannelId` 只看前缀标记，**频道已被删除**时 `AddMessage` 返回 `channel xxx not found`，`emitToWindow` 里 `if ... err == nil` 静默丢弃（`main.go:874`）。回复凭空消失，无日志、无前端提示。

**判定**：已证实（无回声为已证实；`@B` 不触发、删除频道静默丢回复为已证实路径 + 推断后果）。

---

## F3 — 群里一个 agent 看不到别人说过什么：prompt 里只有「频道标记 + 本条发言人 + 本条正文」

**证据**
- 构造 prompt 的**唯一那一行**：`main.go:360`
  `prompt := fmt.Sprintf("[channel:%s] %s: %s", channelId, fromName, text)`
  —— 三个插值：频道 ID、**本条**的 `fromName`、**本条**的 `text`。**没有频道历史，没有成员列表，没有其他成员的任何消息。**
- 进 prompt 的东西：`execute` 行 **461-463** 把这条字符串 append 成 `user` 消息；`executeMock` 行 **493-496** 把整个 `a.Messages`（system + 该 agent 自己的历次 user/assistant）作为 `history` 交给 `client.Stream`。
- `executeCLI` 行 **573** `runner.Run(runCtx, text)` —— 只把**本条 text** 交给 CLI，私有 history 连传都没传（`internal/cli` 侧待确认语义，但入参就是单条 `text`）。
- 频道历史在 `channel.Channel.Messages`，被读的唯一地方是 `main.go:ListChannels` 与 `emitToWindow` 的写回；**没有任何代码把 `ch.Messages` 送进 agent 的 `Messages`/prompt**。

**实际后果**
- 群聊的「共享上下文」是**假的**：每个成员只知道自己被问过什么。同一句「同意上一条」在 5 个 agent 眼里是 5 个不同的世界。
- 私有 history 反而会**累积并串味**：`a.Messages` 同时装着单聊内容和带 `[channel:...]` 前缀的群聊内容（同一个 `Manager.Prompt` 通道，无 channel 维度），所以「单聊」与「群聊」在 agent 内部是**一条历史**（见 F6）。
- mock 的回复模板 `internal/llm/mock.go:mockReply` 直接把 `user` 原文回显（`[%s] 收到：%s`），所以 UI 上看起来「每个人都在回同一句话」——这是 **implementation 的坦诚**，不是 bug 掩盖，但确实让「看不见彼此」在首屏就可见：5 条回复内容一模一样。

**判定**：已证实（prompt 组成、频道历史不入 prompt）；`executeCLI` 侧为推断。

---

## F4 — 队列满：消息**已经**写进频道且已推前端，但仍返回「部分成员未能接收」；错误又被前端吞掉

**证据**
- `main.go:SendToChannel` 行 **319-326**：**先** `Broadcast`（`AddMessage` 落库）→ **先** `ChannelMessage.Emit(s.win, *msg)` → 然后才逐个 `Prompt`。
- 行 **361-367**：`Prompt` 出错只 `errs = append(errs, err.Error())`，**不回滚消息、不标记消息状态**。
- 行 **372-375**：`return fmt.Errorf("部分成员未能接收: %s", strings.Join(errs, "; "))` —— 一个**字符串拼接的聚合错误**，丢失「谁失败/为什么失败」的结构（仅靠 `err.Error()` 里嵌的 agent id 可辨）。
- `internal/agent/manager.go:Prompt` 行 **414**：`return fmt.Errorf("agent %s queue full", req.AgentID)`。
- 前端吞错：`src/lib/useSidecar.ts` `case 'channel.send'` → `.catch((e) => { console.error(...); return null })`；`src/App.tsx:ChannelView onSend` 不 await、不 toast。
- `channel.Message` 结构（`channel.go:12-20`）**没有 `status` / `pending` / `failed` 字段**，所以「部分成功」这个状态在数据模型里**无处安放**。

**实际后果**
1. 频道消息与实际投递**必然不一致**：消息在，5 个成员里 2 个队列满 → UI 看到「一条用户消息 + 3 条回复」，另外 2 个成员**永远不知道**这句话存在过（不是延迟，是丢失：`Prompt` 走 `default` 分支直接返回，不重试、不落待办队列）。
2. 用户看不到这句错误。`console.error` 在打包后的桌面壳里等于不存在。
3. 触发概率不低：mock 每 token 28ms（`internal/llm/mock.go:tokenize` + `time.Sleep(28ms)`）、单 agent 队列 32；`@all` + 反复回车即可把某些成员推满。
4. 同一个 `SendToChannel` 同时承担「记录消息」「选择接收者」「投递」「错误聚合」四件事，错误**没有结构化落点**（没有 message-level delivery state），这是 shallow interface 的直接后果。

**判定**：已证实。

---

## F5 — 删除 agent：`RemoveMember` 只清 `members`，历史消息里的 `fromId` / 前端选择态 / 正在跑的 prompt 都是悬挂引用

**证据（每一处仍引用 agent ID 的位置）**
| 位置 | 引用形态 | 是否随 `DeleteAgent` 清理 |
|---|---|---|
| `internal/channel/channel.go:RemoveMember` **191-206** | `ch.Members` | ✅ 唯一被清的地方（`main.go:DeleteAgent` **280-287** 调它） |
| `internal/channel/channel.go:Message.FromID` **15** | 已存在消息的 `fromId` | ❌ 不清（`main.go:DeleteAgent` 只调 `RemoveMember`） |
| `internal/channel/channel.go:Message.FromName` **16** | 已存在消息的 `fromName` | ❌ 不清（所以历史仍显示名字，但头像颜色丢了） |
| `src/components/ChannelView.tsx:ChannelBubble` **~300-320** | `memberById(msg.fromId)` 返回 `undefined` → `color` 回落 `var(--text-tertiary)` | ❌ 已删成员的历史气泡**永远失去颜色**，`name` 靠 `msg.fromName` 兜住 |
| `src/components/Sidebar.tsx:CreateChannelModal` | `agents` 列表 | ✅ 天然不含 |
| `internal/agent/manager.go:Agent.Messages` | `[channel:...]` 前缀 + 私有历史 | ❌ 随 agent 一起删（无引用问题） |
| `internal/agent/manager.go:Delete` **360-385** | `close(a.done)` + `a.stop()` | ⚠️ 两者之间有窗口：`workerLoop` 的 `select` 可能已取到 `req` 并进入 `execute`，`a.stop()` 里 `a.cancel == nil` 时返回 false → **该轮照跑并在结束后 `m.emit` 一个已删 agent 的 `agent.message` / `agent.channel.reply`（`finish`/`executeMock` 均不校验 agent 是否还在）** |
| `src/App.tsx` 选中态回落 **22-36** | `active.kind === 'agent' && !s.agents.some(...)` → 回落 | ✅ 有回落；但 **`active.kind === 'channel'` 的回落只在频道消失时触发**，删的是 agent 时不触发（不影响） |
| `src/lib/useSidecar.ts:events.agentDeleted` **102-108** | 只 `filter` + `omit(streaming)` | ⚠️ **不清 `errors[id]` / `tokenUsage[id]` / `activities[*]`**，残留内存与活动流幽灵条目 |

**实际后果**
- 无悬挂**指针**（Go 侧 `Get` 返回 `!ok` 会被 `SendToChannel:334-337` `continue` 跳过），但有**悬挂引用语义**：频道历史保留 `fromId` 却查不到人 → `ChannelView` 的成员色/成员徽章失配，且**没有任何「已离开成员」的呈现**。
- 更隐蔽的是「删除中」竞态：`Delete` 先 `delete(map)` 再 `close(done)` 再 `a.stop()`，而 `stop()` 在 `cancel == nil`（刚 `setIdleOrQueued` 后）时**不取消任何东西**；这一轮结束后 `m.emit("agent.channel.reply")` 仍会被 `emitToWindow` 写进频道历史 —— 于是频道里出现一条**来自已删 agent** 的消息，`FromID` 永远 `memberById === undefined`。
- `DeleteAgent` 的清理职责被**劈成两半**（`agent.Manager.Delete` 管 agent 表，`Sidecar` 手动补一刀 `channel.Manager.RemoveMember`）——**没有单一 module 拥有「删除一个参与者」**。任何新的引用点（未来的 thread、mention、worktree、pty 会话）都必须记得在 `main.go:280` 那一处手工补行，**locality 很差**。

**判定**：已证实（表格行均为源码可查）；「删除中竞态」为已证实路径 + 推断触发概率。

---

## F6 — 单聊 ↔ 群聊切换：切换的是**视图**，不是**会话**；两套输入框是两份 parallel implementation

**证据**
- `src/App.tsx` 行 **40-52**：`activeAgent` / `activeChannel` 由 `active.kind` **二选一**，渲染分支 `activeChannel ? <ChannelView/> : <AgentView/>`（**完整 unmount/remount**，不是并排）。
- `src/components/ChannelView.tsx` 行 **14** `useState('')` + `taRef`；`src/components/AgentView.tsx` 行 **27** `useState('')` + `taRef`。切换即 **remount → `input` 状态清零**。半个句子在切换时**直接丢失**（无 draft 提升到 `App`/`useSidecar`）。
- 滚动位置：`ChannelView.tsx:22-26` 依赖 `channel?.messages.length`，remount 后 `useEffect` 强制 `scrollTop = scrollHeight`（永远回到底部）；`AgentView.tsx` 有 `pinnedToBottom` ref 的「智能 pin」，**两套策略**。
- @ 提及：`ChannelView.tsx:handleInputChange/insertMention/filtered` 与 `AgentView.tsx:handleInputChange/insertMention/filteredAgents/parseMentions` 是**两份独立实现**，语义还不同：
  - `AgentView.parseMentions` 用 `/@(\S+)/g` 并**剥掉 @ 再路由**；
  - `main.go:parseMentions`（379-393）用 `strings.Fields` + 去尾标点，**且 `SendToChannel` 不剥 @，原文连 @ 一起进 prompt**（`main.go:360` 用的是原始 `text`）。
  - `ChannelView` 的 `insertMention` 拼的是 `input.slice(0, atIndex) + '@' + name + ' '`，`AgentView` 同形但目标集合不同（`members` vs `agents`）——**两份复制粘贴的 mention 逻辑**，第三份在 Go 侧。
- 单聊里 `App.tsx` 传 `disableRoute`（行 **~110**），但 `onRoute` **照样传入**；`AgentView.submit`（行 **132-155**）只有在 `!p.disableRoute` 时才路由，所以路由能力**在单聊被关掉**——但 `quoteToAgent`（行 **58-65**）**不检查** `disableRoute`，右键「引用给某 agent」仍能发出 `agent.route`。**同一组件内两种态度**。
- 状态层面：`s.streaming` / `s.errors` / `s.tokenUsage` **按 agentId 索引**，`ChannelView` **完全不消费**它们（props 只有 `channel/agents/onSend/onDelete`）→ **群聊里没有任何流式可见性**：用户看到的是 5 个 agent 各自在一个看不见的 Tab 里流，频道界面全程静止直到整条回复落库。
- 消息权威源是**双份**：agent 侧 `a.Messages`，频道侧 `ch.Messages`。群聊回复同时进两处（`manager.go:537` append + `main.go:874` `AddMessage`），但频道里**只有最终全文**，agent 侧没有频道的其他成员消息 —— 两份状态**永不收敛**。

**实际后果**
- 切换代价 = 输入框内容清零 + 滚动位置重置 + 群聊中看不到「谁在想」。没有丢**已落库**的消息（频道历史在 `s.channels` 里，AgentView 消息在 `s.agents` 里），丢的是**未提交的输入**与**在途流式片段**（`ChannelView` 不显示 `streaming`，切走再切回最多看到整条）。
- 「群聊」与「单聊」是两个 **shallow adapter** 贴在同一份后端 API（`Prompt` / `SendToChannel`）上：UI 层 90% 重复（textarea 自动高度、Enter 提交、mention 弹层、mention 插入），仅发言人身份与目标集合不同。**任何输入体验改进都要改两遍**，这是 leverage 最差的位置。

**判定**：已证实（双份实现、draft 丢失、mention 第三份实现）；「在途片段丢失」为推断（取决于流式事件是否落在已 unmount 的订阅上——订阅在 `useSidecar`，是全局的，故 `streaming` 状态**不丢**，只是 `ChannelView` 不显示）。

---

## F7 — 测试覆盖：频道 module 只测「存取 + 落盘」，群聊**行为**几乎零覆盖

**证据（`internal/channel/channel_test.go` 全部 6 个测试）**

| 测试 | 断言内容 | 覆盖的语义 |
|---|---|---|
| `TestBroadcastExcludesSenderAndKeepsOrder` | 4 张表：三成员排除发送者 / 用户不在成员中 / 空成员 / 发送者是唯一成员；断言 `msg` 非 nil、`FromID/Text/ChannelID/FromName` 回填、`recipients` **保序**、`len(Messages)==1` | **纯数据**：差集 + 保序 + 消息落库 |
| `TestBroadcastMissingChannel` | `err` 含 `channel chX not found`；`msg/recipients` 均 nil；`List()` 长度 0 | 错误路径 |
| `TestRemoveMember` | 3 频道矩阵 `{a2}/{} /{}`；**重复移除幂等** | 成员表清理 |
| `TestPersistenceRoundTrip` | `Name/Members` 顺序、`Messages[0]` 的 `Text/FromName/ChannelID` 往返 | 落盘往返 |
| `TestSaveWritesValidJSONWithoutTempFile` | `json.Unmarshal` 成功、`len==1`、无残留 `.tmp` | 落盘原子性 |
| `TestChannelConcurrentSaveKeepsFileValid` | 20 并发 `Save` 后仍是合法 JSON | 落盘并发 |

**`main_test.go` 里的频道相关**（在 Sidecar 层，不在 channel 包）：
- `TestSendToChannelNoMembers`（TC-024）：0 成员仍记录消息、不报错。
- `TestSendToChannelPartialFailureKeepsOthers`（TC-026）：**用「已删 agent 留在 members 里」构造失败**，断言合并错误含 `部分成员未能接收` 与 agent id、消息仍落库、健康成员**最终**收到 prompt。
- `TestDeleteAgentRemovesChannelMember`（TC-028）：`members` 变为 `[a2]`、`ListAgents` 不再含。
- `TestSendToChannelDoesNotDuplicateChannel`（TC-029）：不重复建频道、`Messages` 只有用户那条、成员回复进**自己**历史（注释明确写「成员的回复进入它自己的对话历史，而不是频道」）。

**完全没测的群聊行为（逐条列）**
1. **多成员并行**：从未有一个测试让 2 个以上成员同时收到并确认都跑起来（TC-026 断言「healthy 收到」是**单个**健康成员）。5 成员并发、交叉影响、乱序落库 —— **零覆盖**。
2. **`@` 提及语义**：`parseMentions` 与 `isRelevantToRole` **完全没有测试**（`rg` 无命中）。`@all`、被 @ 者必回、未被 @ 者跳过、mention==name vs ==id、`@` 带中文标点 —— 全靠肉眼。
3. **回复回流**：`agent.channel.reply` → `emitToWindow` → `AddMessage` → `ChannelMessage.Emit` 这条链**没有测试**（`toAgentChannelReply` 只在 `TestEventPayloadDecoders` 里被列进「畸形负载」黑名单，**从未测过合法负载**）。TC-029 的注释甚至**把它当成「回复不进频道」**来断言（只断言 `Messages` 长度为 1），与 `main.go:874` 的写回路径**语义相反**——测试在为一个已过时的行为作保。
4. **回声/风暴**：无任何「回复是否触发其他 agent」的测试（因为不存在这条边，所以也没人测「不存在」）。
5. **群历史进 prompt**：无测试断言 prompt 内容（`main.go:360` 那行**零测试**）。`TestRoutePrefix` 只测 `[from X]` 前缀，没有对应的 `[channel:X]` 版本。
6. **队列满**：`Prompt` 的 `queue full` 分支（`manager.go:414`）**零测试**；TC-026 的注释明确说「队列是 32 缓冲且 worker 持续消费，灌满本身是有竞态的」，于是**用已删 agent 绕过**——**最难的行为被测试主动回避**。
7. **删除竞态**：`Delete` 的 `close(done)`/`stop()` 窗口、删后仍 `emit` —— 零覆盖。
8. **前端两套输入框**：仓库里**没有任何前端测试**（`find` 无 `*.test.tsx`），`ChannelView`/`AgentView` 的 mention/draft/滚动行为零覆盖。

**判定**：已证实（测试清单与断言逐字核对）。

---

## 候选清单

### A. shallow interface 造成的问题（interface 深度不足，调用方被迫承担实现细节）

1. **`channel.Manager.Broadcast` 只返回 `[]string`（recipients），不返回投递状态**（`channel.go:233`）。它把「谁收到/谁没收到/为什么」全部丢给调用方 `SendToChannel`（`main.go:318-376`）用局部 `errs []string` 拼字符串。→ F4 的直接成因；`Message` 无 `status` 字段是同一处 shallow 的另一半。
2. **`agent.Manager.Prompt` 的失败只是一个 `fmt.Errorf("queue full")`**（`manager.go:414`），无类型/无重试/无背压信号。→ 上层只能字符串聚合，无法区分「可重试的忙」与「不可恢复的错」。
3. **频道与会话之间没有 interface，只有一条字符串约定 `[channel:xxx]`**（`manager.go:770-779` ↔ `main.go:360`）。`extractChannelId` 是**最浅的 seam**：无转义、无长度、无成员校验、无「这条已过期/频道已删」判定。→ F2 的回声规避靠「少一条边」而非规则；F3 的 prompt 组成不可扩展（想加群历史，得改这一行字符串协议）。
4. **`Emitter func(event string, data any)`**（`manager.go:57`）是**无类型的宽 interface**：`data any` 迫使 `main.go` 写 10+ 个 `toXxx(map[string]any)` 防御式 decoder（`main.go:628-833`），并在 `emitToWindow` 里做 `switch event`。→ 任何新事件（如 `agent.channel.reply` 的投递状态）都要在 3 处同步；`toAgentChannelReply` 的合法负载从没被测过（F7-3）。
5. **`AgentView` / `ChannelView` 是两份 shallow adapter**：同一 textarea/Enter/mention 逻辑各写一遍（F6），且 Go 侧还有第三份 `parseMentions`。→ leverage 最差的位置：输入体验改进成本 ×3。
6. **`DeleteAgent` 的清理职责劈成两半**（`main.go:280` + `channel.go:191`）。删除参与者这个概念的 interface 根本不存在，只有一个「记得手动补一行」的 seam。→ F5 的悬挂 `FromID` 与前端残留 state。

### B. 缺一个 module 造成的问题（没有归属地的知识/状态）

1. **缺一个「频道投递 / fan-out」module**。现在 fan-out 逻辑住在 `main.go:SendToChannel`（Sidecar 的 method 里），混合了：@ 解析、角色相关性、目标筛选（`internal/channel/manager.Broadcast` 的差集）、prompt 拼装、错误聚合。→ 这段**没有任何 module 拥有它**，所以：不能被单测（F7-2/6）、不能加策略（重试/限流/轮次）、不能观测（F4 的错误只进 `console.error`）。这是 **deep module 缺失**，也是最该补的一个（`leverage` 最大：一处补齐 F1/F2/F4/F7）。
2. **缺一个「群聊上下文 / 共享 transcript」module**。当前群历史的唯一读者是前端；agent 侧永远只看见自己。→ F3 的「假共享上下文」无法在 `implementation` 层修正，因为**没有地方算「这一轮群聊的可见上下文」**；`Channel.Messages` 与 `Agent.Messages` 是两份永不收敛的权威源（F6 末）。
3. **缺一个「参与者生命周期」module**。`Delete` 只知道 agent 表，不知道频道、mention、前端 cache、在途 prompt。→ F5 的所有悬挂引用、以及「删后仍 emit」的竞态，本质是**没有单一 module 拥有「参与者离开」这个事件**。
4. **缺一个「输入 / composer」module（前端）**。`ChannelView` 与 `AgentView` 的输入、mention、draft、滚动是平行 implementation。→ F6 的 draft 丢失与双份维护。
5. **缺一个「群内并行执行的观测面」**。`streaming/errors/tokenUsage` 按 agentId 索引，`ChannelView` 不消费；没有任何 module 把「N 个成员这一轮的状态」聚合成一个群聊级视图。→ 5 个成员同时跑时，用户在频道里**看不见任何在途活动**（F1-4 / F6）。

---

## 附：走查中发现但不在 7 问内的两点（供下游 specialist 参考）

- `internal/agent/manager.go:Delete` 行 **377-378**：`close(a.done)` 在 `m.mu` **释放之后**执行；`workerLoop` 若已选出 `req` 正进 `execute`，`a.stop()`（`manager.go:709-717`）在 `a.cancel == nil` 时**不取消**。→ 删后仍可能 `emit agent.message / agent.channel.reply`（F5 已并入）。判定：已证实路径，触发概率待确认。
- `main.go:parseMentions`（379-393）**不剥除** `@`，而 `AgentView.parseMentions`（`AgentView.tsx:132-155`）**剥除**。同一句 `@后端 看下 API` 在群聊里进 prompt 为 `[channel:ch] 你: @后端 看下 API`（含 @），在单聊路由里为 `看下 API`（不含 @）。→ 两处 mention 语义不一致。判定：已证实。
