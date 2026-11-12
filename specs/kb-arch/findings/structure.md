# 结构轴走查：群聊（channel）vs 单聊（agent DM）的 module 形状

只读走查，未修改任何源码。仓库 `multi-agent-desktop`，HEAD `f76a25a`。
所有路径相对仓库根目录。

---

## 1. channel 与 agent 是两套彻底独立的 module，`message` 有两份互不相同的 implementation

### 证据

**两个 struct、两个 Manager、两个落盘文件，零共享类型：**

- `internal/channel/channel.go:9` `type Message struct` — 字段 `ID / ChannelID / FromID / FromName / Text / At`
- `internal/channel/channel.go:20` `type Channel struct` — 字段 `ID / Name / Members []string / Messages []Message`
- `internal/channel/channel.go:28` `type Manager struct` — `mu / channels map[string]*Channel / order / store / saveMu / saveWG`
- `internal/agent/manager.go:27` `type Agent struct` — `Messages []llm.Message`；`internal/llm/llm.go:9` `type Message struct{Role, Content, Timestamp}`
- 两个 `Manager` 的形状**逐字段同构**（`mu` + `map` + `order` + `store` + `saveMu` + `saveWG` + 各自 `Save/Load/goSave/waitSaves`）：
  - `internal/channel/channel.go:42` `NewManager()`
  - `internal/agent/manager.go:72` `NewManager(cfg, emit)`
  - 落盘：`main.go:907` `mgr.SetStore(.../agents.json)` 与 `main.go:910` `channels.SetStore(.../channels.json)`——**两个文件**
- Go 侧不共享任何类型：`channel.Message` 与 `llm.Message` 字段名交集为 0；`channel.FromID` 是 agent id 或字面量 `"user"`（`internal/channel/channel.go:13` 注释），`llm.Message` 只有 `Role: system|user|assistant`。
- 前端同样是两套：`src/mygo.ts:30` `interface Channel` / `src/mygo.ts:37` `interface ChannelMessage` vs `src/mygo.ts:19` `messages: Message[]`（`AgentView`）。
- 两个视图的选中态是**互斥**的 `kind` 判别联合：`src/App.tsx:15` `type ActiveRef = { kind: 'agent' | 'channel'; id: string } | null`；渲染分支 `src/App.tsx:78` `{activeChannel ? <ChannelView .../> : <AgentView .../>}`。

**同一条用户发言在群里发出后存在 3 份副本 + 1 份纯日志：**

| # | 位置 | 写入点 | 内容形状 |
|---|---|---|---|
| 1 | 频道历史 `Channel.Messages[i]` | `main.go:319` `s.channels.Broadcast(...)` → `internal/channel/channel.go:208` `AddMessage` | `Message{FromID:"user", FromName:"你", Text}` |
| 2 | 用户在侧栏 ActivityFeed 的一条活动 | `src/lib/useSidecar.ts:163` `sub(events.agentMessage, ...)` 里 `addActivity` 逻辑（`useSidecar.ts:157` `activities: [...s.activities, activity]`） | `Activity{type:'prompt', fromName:'你'}` |
| 3 | **每个被选中的 agent 的 `Agent.Messages`**，且文本被改写 | `main.go:360` `prompt := fmt.Sprintf("[channel:%s] %s: %s", channelId, fromName, text)` → `internal/agent/manager.go:392` `Prompt` → `internal/agent/manager.go:460` `execute`（`a.Messages = append(..., llm.Message{Role:"user", Content: req.Text})`） | `Role:"user", Content:"[channel:ch-1] 你: hi"` |
| 4 | 前端 agent 气泡 | `internal/agent/manager.go:474` `m.emit("agent.message", ...)` → `src/lib/useSidecar.ts:127-160` 再 append 一次到 `agents[i].messages` | 同上，带 `[channel:…]` 前缀 |

前端内存里第 1 份与第 3/4 份**同时存在**且都不可能互相派生：`useSidecar.ts:203-211`（channel 只 patch `s.channels`）、`useSidecar.ts:155`（agent.message 只 patch `s.agents`）。

### deletion test

删掉 `internal/channel` 整个 module：群聊消失，但**第 3/4 份副本仍在**——agent 会话里会留下 `[channel:ch-1] 你: hi` 这条无从归属的 user message，`extractChannelId` 也无处回写。反过来删掉 `Agent.Messages` 的写入（`manager.go:461`）则频道历史和 LLM 上下文同时断。两份数据不是「冗余」，而是**各自被独立读取方消费**（频道视图 vs LLM history），删除任一份复杂度都会**搬进读取方**：要么 `ChannelView` 得去扫描所有 agent 的 Messages 并过滤 `[channel:…]` 前缀（把 seam 挪到前端），要么 LLM 得接受外部注入的 history。

### 判定

**已证实**：两套 module 完全独立（类型、Manager、落盘文件、前端 store、ui 分支均无交集）；`message` 是两个不同的 implementation；一条群聊发言在 3 处持久状态（channel.Messages、每个 recipient 的 Agent.Messages、前端两份内存镜像）各存一份，且 agent 侧那份**经过改写**（加了 `[channel:id] 名字: ` 前缀，原文丢失）。

---

## 2. 回写路径的 seam 是字符串标记 `[channel:xxx]`，且**丢失频道上下文**

### 证据

**标记的生成（唯一一处）：**
`main.go:360`（`Sidecar.SendToChannel` 内）
```go
prompt := fmt.Sprintf("[channel:%s] %s: %s", channelId, fromName, text)
s.mgr.Prompt(context.Background(), agent.PromptRequest{AgentID: agentId, Text: prompt})
```

**标记的解析（唯一实现）：**
`internal/agent/manager.go:770` `extractChannelId(text string) string` —— 朴素 `strings.Index(text, "[channel:")` + 找 `]`，取 `text[start+9 : start+end]`。无转义、无嵌套处理、无长度/合法性校验。

**`extractChannelId` 的全部调用点（2 个，均在 agent module 内）：**
- `internal/agent/manager.go:545`（`executeMock` 尾部）
- `internal/agent/manager.go:632`（`executeCLI` 尾部）

两处都是同一段复制：
```go
if channelId := extractChannelId(text); channelId != "" {
    m.emit("agent.channel.reply", map[string]any{...})
}
```

**回写的落地：**
`main.go:866` `case "agent.channel.reply":` → `main.go:870` `chMgr.AddMessage(v.ChannelID, v.AgentID, v.AgentName, v.Text)` → `main.go:871` `ChannelMessage.Emit(win, *msg)`。
也就是说：`agent.Manager` **不持有 channel**，它只 `emit` 一个 `map[string]any` 事件，由 `main.go` 的 `emitToWindow` 把内容写回 `channel.Manager`。回写路径跨了 3 层：`agent.Manager.emit` → `main.emitToWindow` → `channel.Manager.AddMessage`。

**信息丢失清单（已证实）：**

1. **agent 看不到群里别的 agent 的发言。** `main.go:352-365` 只对 `recipients` 逐个 `Prompt`，每个 prompt 只含「当前这条用户消息」。频道历史 `Channel.Messages` 从不被读进任何 prompt：`rg "ch.Messages"` 在 `channel.go` 之外无调用方；`internal/agent/manager.go:494-499` `executeMock` 的 `history` 完全来自 `a.Messages`。→ 两个 agent 并行回答同一条消息，彼此不可见，也无法就对方的答案补充/反驳。
2. **agent 不知道自己在哪个频道叫什么。** prompt 里只有 id 与原始发送者名，没有频道名、没有成员表（`main.go:360`）。
3. **agent 侧历史里混入标记文本。** `[channel:ch-1] 你: hi` 作为 `llm.Message.Content` 永久进入 LLM history（`manager.go:461`）与前端气泡（`manager.go:474`）。频道 id 变成 **user 内容的一部分**，而不是消息的元数据字段。
4. **标记被伪造的可能。** 用户在频道里输入 `[channel:other] hi` 会原样进入 prompt；`extractChannelId` 取第一个 `[channel:`，于是回写目标由**用户可控文本**决定。而 `main.go:870` 的 `AddMessage` 对不存在的 channelId 返回 error 后被静默吞掉（`if msg, err := ...; err == nil`），→ 消息丢失无日志。
5. **一次回复只能回一个频道。** 标记是单值字符串，agent 若同时在两个频道被提及无法表达。
6. **纯文本跨 seam 无类型。** 生成侧 `fmt.Sprintf`（main.go）与解析侧 `strings.Index`（agent 包）之间无共享常量、无共享类型；两侧各自硬编码字面量 `"[channel:"`。

**附带的第二个「形状重复」：** `main.go:379` `parseMentions` 与 `src/components/AgentView.tsx:121` `parseMentions` 是同一职责的两份 implementation——后端 `strings.Fields` 逐词切（`main.go:381`）、去掉标点（`main.go:387`），前端用正则 `/@(\S+)/g` 并在同一函数里**顺手剥掉 @ 片段的正文**（`AgentView.tsx:127` `text.replace(mentionRegex, "")`）。两侧对「什么是提及」的定义已经分叉；`main.go:396` `isRelevantToRole` 是唯一实现但零单测（`rg "isRelevantToRole" main_test.go` 无命中）。

### deletion test

删掉 `extractChannelId` 与两处调用：`agent.channel.reply` 事件从无人发，`main.go:866` 分支成为死码，`chMgr.AddMessage` 只剩测试调用方。复杂度**不消失**——它会整块搬到 `main.sendToChannel` 的调用方：需要自己记住「哪个 agent 是在哪个频道里被唤起的」，即一张 `agentId → channelId` 的在途表，且要处理 agent 排队/并发（`agent.Manager` 里 queue 缓冲 32，`manager.go:392` `Prompt`），这张表必须在 agent module 内部维护才正确。→ 所以 marker 方案虽然脆，但确实把「归属」这件事的复杂度**收敛在产生它的那个 goroutine 里**。

但**丢失频道上下文**这件事的复杂度无处收敛：现在谁都不做，于是它消失了（以功能缺失的形式）。

### 判定

**已证实**：回写路径 = `fmt.Sprintf` 造标记 → prompt 入队 → agent 执行 → `extractChannelId` 解析 → `emit"agent.channel.reply"` → `main.emitToWindow` → `channel.Manager.AddMessage`。`extractChannelId` 仅 2 个调用点，均在 agent 包内。
**已证实**：agent 看不到频道历史、看不到其他 agent 发言、不知道频道名；频道 id 被塞进 user content。
**推断**：`[channel:…]` 前缀是「让 agent 的 user message 自己携带路由信息」的经典 shallow seam——它把 channel module 的知识（id 格式）泄露进了 user 可见内容。改成 `PromptRequest{ChannelID string}` 字段是低风险、高 leverage 的收口（`main.go:360` 生成、`manager.go:545/632` 消费，共 3 处）。
**待确认**：`[channel:…]` 是否本意就要透给用户看（设计文档 `docs/design.md:229-232` 只描述了 `[from X]` 前缀语义，未提 channel 前缀的可见性契约）。

---

## 3. channel 的 interface：11 个导出方法，8 个只有单一调用方

### 证据

`internal/channel/channel.go` 的全部导出符号与调用方清点（非测试调用方）：

| 方法 | 定义行 | 生产调用方（文件:符号） | 数量 |
|---|---|---|---|
| `NewManager` | 42 | `main.go:901` `main()`；`main_test.go:29` `newTestSidecar` | 1 (+1 测试) |
| `SetStore` | 49 | `main.go:910` `main()` | **1** |
| `Save` | 70 | `goSave`（channel.go:57，module 内部）；`channel_test.go:167/200/244` | **0 生产外部** |
| `Load` | 107 | `SetStore`（channel.go:53，module 内部） | **0 生产外部** |
| `Create` | 149 | `main.go:300` `Sidecar.CreateChannel` | **1** |
| `Get` | 170 | 仅 `main_test.go:208/251/287/443` | **0 生产** |
| `List` | 178 | `main.go:307` `Sidecar.ListChannels` | **1** |
| `RemoveMember` | 191 | `main.go:283` `Sidecar.DeleteAgent` | **1** |
| `AddMessage` | 208 | `main.go:870` `emitToWindow`；`Broadcast`（channel.go:234，module 内部） | 1 (+内部) |
| `Broadcast` | 233 | `main.go:319` `Sidecar.SendToChannel` | **1** |
| `waitSaves` | 66 | 仅 `channel_test.go:17`（未导出，测试 seam） | 0 生产 |
| `goSave` | 57 | 内部 3 处（Create/RemoveMember/AddMessage） | — |

**8 个方法只有 1 个生产调用方**：`SetStore / Create / List / RemoveMember / AddMessage / Broadcast / Get(0) / Save+Load(0 外部)`。

**缺席的方法同样说明问题**：
- 无 `Delete(channelId)`：`Sidecar` 没有对应方法（`rg -in "deletechannel|channel.delete"` 零命中），但 `src/components/ChannelView.tsx:148-156` **渲染了一个删除按钮并 `confirm("删除频道「…」？消息将一并清除。")`**，`src/App.tsx:78-92` 传 `ChannelView` 时**没有传 `onDelete`** → `onDelete && (...)` 恒假，按钮永不出现。频道一旦创建无法删除（`channels.json` 只增不减），Sidebar 也无删除入口（`Sidebar.tsx:16 onDelete` 只给 agent 用，见 `Sidebar.tsx:308`）。
- 无 `Rename`、无 `AddMember / RemoveOneMember`（只有全局 `RemoveMember(agentId)` 横扫所有频道）。

### deletion test（逐方法）

| 方法 | 删掉的后果 | 复杂度去向 | 判定 |
|---|---|---|---|
| `Broadcast` | 调用方 `SendToChannel` 必须自己做 `AddMessage` + 自己遍历 `Members` 排 sender | **散入唯一调用方**，且需要 `Get` 拿 Members（于是 `Get` 从 0 生产调用方变 1） | **shallow**（约 20 行，单一调用方，无独立策略） |
| `AddMessage` | `emitToWindow` 那 1 行需要直接 `Get` + 改 `ch.Messages` + 自己加锁 | 复杂度**散出 module**，破坏 `channel.Manager` 的封装（调用方得拿 `*Channel` 裸指针写切片） | **中 deep**（是唯一的 append 入口 + 唯一 id/时间戳生成点 + 唯一 `goSave` 触发点） |
| `RemoveMember` | `Sidecar.DeleteAgent` 需遍历 `List()`、在每个 `*Channel` 上原地改 `Members` | **散入调用方**，并且丢锁（`List` 返回裸 `*Channel`，见 Finding 4）→ 与 `Save` 的 RLock 竞争 | **中 deep**（跨频道不变量：agent 删除后不得残留成员，测试 `TestRemoveMember` 守住） |
| `Create` | `Sidecar.CreateChannel` 需自己造 id（`ch-%d`）、初始化 `Messages: []Message{}` 防 null、append `order`、触发落盘 | **散入调用方**，且 `order` 排序是 module 私有不变量 | **中 deep** |
| `List` | `Sidecar.ListChannels` 遍历 map → **丢掉 `order` 保序**（前端侧栏顺序会随机） | 复杂度散出但**语义会坏** | **中 deep**（保序是隐藏契约） |
| `Get` | 生产无调用方 → 删除零影响（除测试） | **消失** | **shallow**（纯 map 读取，且把内部指针直接发出，见 Finding 4） |
| `SetStore` | `main` 需自己 `Load`；`Save` 需自己去读 store 路径 | **散入 2 处** | **中 deep**（把「持久化开关 = 空路径」这个状态藏起来） |
| `Save` | 内部 3 个 mutator 都得自己拼 `[]Channel` + 原子写 | **散入 3 处** | **deep**（`Save` 里的 copy-on-write（channel.go:80-95）+ `saveMu` + tmp+rename 是真实实现深度） |
| `Load` | `SetStore` 需内联 40 行 JSON 恢复 + 去重 + `Messages` 非 nil 防御（channel.go:136-140） | **散入 1 处** | **deep**（防御与去重逻辑） |
| `waitSaves` | 测试无法确定性等待异步落盘 → 3 个测试会 flaky | **消失（但引入不稳定）** | **测试 seam，leverage 高** |

### 判定

**已证实**：channel 的 interface 有 11 个导出方法，其中 `Get` 生产 0 调用方、`Save/Load` 0 外部生产调用方、6 个方法恰好 1 个调用方。`Broadcast` 是唯一一层可在调用方 inline 掉的薄包装；`Save/Load/AddMessage/Create/List/RemoveMember` 各自承载了调用方不该知道的实现细节。

---

## 4. `Members` 与 `Messages` 是两种职责；没有「会话」概念，只有「广播列表 + 消息数组」

### 证据

`internal/channel/channel.go:20-27`
```go
type Channel struct {
    ID       string    `json:"id"`
    Name     string    `json:"name"`
    Members  []string  `json:"members"`  // agent IDs
    Messages []Message `json:"messages"`
}
```

- **`Members` 是 agent 侧的镜像外键**：它不持有指向 `agent.Agent` 的引用，只是 `[]string`。一致性靠**外部一次性修补**：`main.go:283` `s.channels.RemoveMember(id)`（仅在 `DeleteAgent` 成功时调用）。测试 `main_test.go:225-234` 显式构造了「成员表与 agent 表不一致」的状态并**断言它能存活**（注释：`模拟"成员表与 agent 表不一致"`）——即不一致是被承认的常态，而非待修的 bug。
- **`Messages` 是独立事实源**，且与 `Members` 无结构关联：`internal/channel/channel.go:233-252` `Broadcast` 里两者被同时读取但彼此不校验——`fromId` 可以不在 `Members` 里（测试 `channel_test.go:56-60` 的 `fromID: "user"` 就断言 `recipients` 保留全部成员）；回写路径 `main.go:870` 写入的 `FromID` 是 agent id，也从不校验该 agent 是否仍在 `Members`。
- **无「会话」概念**：`Channel` 上没有 `Owner`、`CreatedAt`、`Topic`、`lastReadAt`、`pinned`、无 per-member 状态（如已读位置、静音）。它是 `{名字, 一串 id, 一串消息}`。
- **无 per-recipient 投递状态**：`main.go:318-376` `SendToChannel` 对每个 recipient 独立判断 `shouldReply`（`main.go:340-357`），判断结果**不落任何地方**——不在 `Channel.Messages` 里记录「谁被跳过、为什么」，也不在 `Message` 上记录 `recipients`。前端 `ChannelView.tsx` 因此无法显示「A 已回复 / B 被判定不相关」。
- **消息不区分类型**：`channel.Message` 无 `Kind` 字段，用户发言与 agent 回复同构，只靠 `FromID == "user"` 字面量区分（`src/components/ChannelView.tsx:307` `const isUser = msg.fromId === 'user'`）。
- **`*Channel` 指针裸奔**：`Get`（channel.go:170）与 `List`（channel.go:178）返回 `*Channel`，调用方在锁外读 `ch.Members` / `ch.Messages`（如 `main.go:308-312` 直接 `*ch` 拷贝、`main_test.go:213` 读 `got.Messages`），与 `AddMessage` 的 `m.mu.Lock()` 写入竞争。对比 agent module 有 `Snapshot()`（`internal/agent/manager.go:732`）专门解决同一问题——**channel 缺这个 seam**。

### deletion test

拆掉 `Members`（改成从 agent 侧反查「谁属于哪个频道」）：`Broadcast` 的 recipients 计算（channel.go:236-247）、`ChannelView.tsx:47` 的 `agents.filter(a => channel.members.includes(a.id))`、`Sidebar.tsx:174` 的 `ch.members.length` 三处都得改成反向索引，且 agent 侧需要新增「我属于哪些频道」字段并同步维护 4 处（`Save/Load/Snapshot/viewMap`，见 `docs/design.md:270` 的持久化白名单警告）。→ 反向索引的复杂度**不会消失**，只是换了宿主。

拆掉 `Messages`（频道历史改为「扫所有 agent 的 `[channel:id]` 前缀消息」）：丢失顺序（agent 各自 `Timestamp` 秒级精度，同秒并发无法排序）、丢失用户自己发的消息（第 3 份副本存在但只有 1 个 recipient 有）、并且把前缀解析复杂度从 1 处（`extractChannelId`）扩散到 N 处读取点。

→ 两者都是**真实的职责**，不该合并。但它们的**组合方式**缺少一层：没有「投递/会话」这个概念把 `Members`（谁该收到）与 `Messages`（说了什么）缝起来。

### 判定

**已证实**：`Members` 与 `Messages` 承担两种完全不同的职责（外键镜像 vs 事实源），二者之间无共享不变量、无一致性校验。`Channel` 没有会话语义，只是「广播列表 + 消息数组」。
**推断**：正因为缺少「投递」这一层，`shouldReply` 的判断逻辑（`main.go:340-357`）只能活在 `Sidecar.SendToChannel` 里，成为一个既不属于 channel 也不属于 agent 的孤儿逻辑。
**待确认**：`main.go:299` `CreateChannel(name, members)` 的 `members` 是否允许后续增删——目前无任何接口可做到，故「群成员管理」这个能力是缺失而非隐藏。

---

## 5. 单聊（agent）的 interface 与群聊形状重合度约 60%，本质差异只有 2 处

### 证据

**逐方法对照表**（channel 见 Finding 3；agent 见 `internal/agent/manager.go`）：

| 职责 | channel (internal/channel) | agent (internal/agent) | 形状 |
|---|---|---|---|
| 构造函数 | `NewManager()` :42 | `NewManager(cfg, emit)` :72 | 同构（agent 多 2 个依赖） |
| 建实例 | `Create(id,name,members)` :149 | `Create(id,name,role)` :289 | **同构**（都是 id 可空 + 默认值 + 注册 order + 异步落盘） |
| 读单个 | `Get(id) (*Channel, bool)` :170 | `Get(id) (*Agent, bool)` :341 | **逐字同构** |
| 列全部 | `List() []*Channel` :178 | `List() []*Agent` :348 | **逐字同构**（都保 `order`） |
| 删 | **无** | `Delete(id) bool` :360 | **缺失** |
| 发消息 | `Broadcast(ch,from,fromName,text) (*Message,[]string,error)` :233 | `Prompt(ctx, PromptRequest{AgentID,Text}) error` :392 | 半同构（都返回「投递结果」，channel 返回 recipients 列表，agent 返回 queue-full 错误） |
| 投递语义 | 同步写历史 + 返回接收者 | 入队（chan 32）+ 异步 worker | **本质差异** |
| 执行 | 无（调用方 main.go 编排） | `execute` :460 → `executeMock` :489 / `executeCLI` :558 | **本质差异** |
| 停止 | 无 | `Stop` :415 / `StopAll` :711 | **缺失** |
| 成员维护 | `RemoveMember(agentId)` :191 | — | channel 独有（因为 agent 是 host 侧） |
| 单例配置 | `SetStore` :49 | `SetStore` :117 | **逐字同构** |
| 持久化 | `Save` :70 / `Load` :107 / `goSave` :57 / `waitSaves` :66 | `Save` :126 / `Load` :190 / `goSave` :169 / `waitSaves` :178 | **逐字同构**（`writeFileAtomic` :186 在 agent 侧，channel 侧内联在 `Save` :85-99） |
| 读一致快照 | **无** | `Snapshot()` :732 / `viewMap()` :751 | **缺失** |
| 事件 | 无 emitter，靠 `main` 侧 typed `Event.Emit`（`main.go:71-72`） | `Emitter func(event string, data any)` 注入（`manager.go:52`） | **本质差异**（两种 seam 风格并存） |

**量化：** channel 11 个导出方法；agent `Manager` 21 个导出方法。**逐字或近逐字同构的 10 个**（`NewManager/SetStore/Save/Load/goSave/waitSaves/Create/Get/List/goSave`）——重合率约 10/16 ≈ 62%（按职责去重后的槽位计）。**channel 少了 4 个 agent 有的**（`Delete/Stop/Snapshot/Emitter` 注入）。

**本质差异（2 处，其余皆为遗留）：**

1. **投递是否异步 + 是否有执行体。** `agent.Prompt`（manager.go:392）只是 `select { case a.queue <- req: ... default: return "queue full" }`；真正的工作在 `workerLoop`（manager.go:448）→ `execute`（manager.go:460）。channel 侧无队列、无 worker、无 cancel——`Broadcast` 返回的 recipients 由 `main.go:340` 的调用方循环 `Prompt`。这是**职责分配**的本质差异：群聊是「N 个 agent 各跑各的」，单聊是「1 个 agent 串行排队」。
2. **是否持有 agent 的存在性。** channel 的 `Members` 是 id 字符串，靠 `main.go:283` 修补；agent 的 existence 由它自己的 map 唯一决定。这是「引用 vs 拥有」的本质差异。

**历史遗留差异（可统一，无本质理由）：**

- `writeFileAtomic` 在 agent 包（`manager.go:186`），channel 内联同一逻辑（`channel.go:85-99`）→ 纯复制粘贴。
- `goSave/waitSaves/saveMu/saveWG` 逐字两份。
- `Snapshot()` 只有 agent 有 → 导致 channel 的 `Get/List` 返回裸指针（Finding 4）。
- 事件 seam 两套：agent 用 `map[string]any` + `emitToWindow` 里 10 个 `toXxx(data)` 解析函数（`main.go:700-833`），channel 用 typed `mygo.Event.Emit`（`main.go:299-303`）。`docs/design.md` 已自认这是历史包袱（"新子系统不走 `map[string]any` 协议"）。→ 这是**两份 emitter seam**，`agent.channel.reply` 恰好横跨两者：agent 侧发 `map[string]any`（manager.go:546），main 侧解析成 `AgentChannelReply`（main.go:820），再转 typed `ChannelMessage.Emit`（main.go:871）。**一次回写穿两次 seam。**
- `Create` 的默认值策略（id 生成格式 `agent-%d` / `ch-%d`、颜色调色板 / 无）本质不同但都叫 `Create`。

### deletion test

把 channel 的 10 个同构方法抽成一个共享的实现：会得到一个 `store[T]{ mu, map[string]T, order, saveMu, saveWG }`。删除它（即在两处各留一份）：复杂度**不消失**，只是**已存在的重复**不再被命名。抽出来的 leverage 在于：`writeFileAtomic` 的原子写 + `saveMu` 串行化 + `order` 保序 + `waitSaves` 测试 seam 是**两处各自实现、四份拷贝、只有其中一份有独立测试覆盖**（`channel_test.go:233` `TestChannelConcurrentSaveKeepsFileValid` 只测 channel 侧）。反过来，把 `Broadcast` 和 `Prompt` 硬塞进同一 interface：**会失败**——投递语义不同（同步返回 recipients vs 异步入队返回 queue-full），`Bundle` 起来的 interface 会让两个实现都暴露用不上的方法。

### 判定

**已证实**：两者 interface 形状重合约 62%，重合部分全部是「内存集合 + 保序 + 异步原子落盘」这一件事的重复实现；不同部分中，投递语义与 existence 归属是 2 处本质差异。
**推断**：一个共享的 collection/persistence 实现（`internal/store` 之类）能删掉约 120 行重复（channel.go:28-146 与 manager.go:55-236 的对应段），且能顺手把 `Snapshot()` 语义带给 channel → 消掉 Finding 4 的裸指针竞争。这属于**把两个 shallow 的重复实现合并成一个 deep 实现**，不是把 channel 合并进 agent。
**待确认**：`Delete` / `Stop` / `Snapshot` 三个缺失方法是「有意不做」还是「尚未做」——`ChannelView.tsx:148-156` 已经有删除按钮的完整 UI 与 confirm 文案，倾向后者（遗留而非设计）。

---

## 6. 前端侧的 module 形状：`ChannelView` 是 shallow 壳，`useSidecar` 是两个 store 的缝合点

### 证据

- `src/components/ChannelView.tsx:15` 只有 4 个 prop：`{channel, agents, onSend, onDelete}`。**`onDelete` 从未被 `App.tsx` 传入**（`src/App.tsx:78-92` 的 `<ChannelView>` 只有 `channel / agents / onSend`）→ `ChannelView.tsx:148` `{onDelete && ...}` 死分支。
- `ChannelView.tsx:47` `const members = agents.filter((a) => channel.members.includes(a.id))` → 每次 render 做 `O(|agents| × |members|)` 的集合求交，**这是 `Channel.Members` 与 `agents` 两个 store 的 join**，且 join 只在视图里发生（后端没有 join 后的形状）。`ChannelView.tsx:48` 再为每条消息 `agents.find(...)` → `O(|messages| × |agents|)`。
- `ChannelView.tsx:307` `const isUser = msg.fromId === 'user'` — 字面量跨层耦合（同 `main.go` 侧 `main.go:328` 无此判别，但 `main_test.go` 多处硬编码 `"user"`）。
- `src/lib/useSidecar.ts:203-211` `sub(events.channelMessage, ...)` 是 channel 的唯一写入路径，做的是「按 id 去重后 append」；`useSidecar.ts:66` `Sidecar.listChannels()` 初始化。两次数据源（初始全量 + 增量事件）**各自独立打补丁到同一个 `channels` 数组**。
- 结构上 `useSidecar` 是**单一 store**：`src/lib/useSidecar.ts:23-40` `type State` 里 `agents` 与 `channels` 平级，`send()` 的 switch（`useSidecar.ts:240-303`）把 14 个 action 名映射到 typed call，`channel.create` / `channel.send` 只是其中 2 个 case。
- `src/App.tsx:15` `ActiveRef` 的 `kind` 判别联合是**唯一把两个体系缝到一起的 seam**；`App.tsx:26-37` 的失效回落逻辑对两个 kind 各写一遍（同形状，2 份）。

### deletion test

删掉 `ChannelView` 换成在 `AgentView` 里用 `kind` 分支：会得到**一个 500 行的视图**，且 `AgentView` 现有的 `parseMentions` / `quoteToAgent` / `disableRoute`（`AgentView.tsx:19`）在群聊语义下全部失效或需重定义。→ 拆分是对的（locality 正确），但 `ChannelView` 内部承担的只是「渲染 + 一个 input」，没有隐藏任何 channel 特有的复杂度（members join 是调用方数据形状的后果，不是被封装的东西）。**shallow。**

### 判定

**已证实**：`ChannelView` 的 `onDelete` 是死 prop（与后端缺失 `Delete` 一致，见 Finding 3/4）；`useSidecar` 是双 store 的唯一缝合点；`App.ActiveRef.kind` 是两个 module 在前端唯一的 seam。
**推断**：`Message.fromId === "user"` 这个 sentinel 是 channel module 缺 `Kind`/`senderType` 字段的直接后果，前端只是被迫接受这个 shape。

---

## 候选清单

### 真正的 deep module（在挣饭钱）

| module | 证据 | 为什么 deep |
|---|---|---|
| `channel.Manager.Save/Load`（channel.go:70/107） | copy-on-write 快照、`saveMu` 串行化、tmp+rename 原子写、`Messages` 非 nil 防御、已存在不覆盖去重 | 11 个导出方法里唯一把「并发 + 崩溃安全 + 幂等加载」三种复杂度藏在 interface 后面的实现；测试 `TestChannelConcurrentSaveKeepsFileValid`（channel_test.go:228）是唯一守住它的证据 |
| `channel.Manager.AddMessage`（channel.go:208） | 唯一 append 入口 + 唯一 id/时间戳生成 + 唯一 `goSave` 触发 + 唯一 channelId 校验点 | 3 种不变量收在 20 行里；删掉它调用方必须持裸指针写切片 |
| `channel.Manager.RemoveMember`（channel.go:191） | 跨频道不变量的唯一维护点（agent 删除 ⇒ 成员表清理），测试 `TestRemoveMember` 含幂等断言 | 复杂度在「跨 channel」而非「单 channel」，无法下放到调用方而不破坏锁 |
| `agent.Manager.execute*`（manager.go:460/489/558） | backend 分派、ctx 取消、流式聚合、toolCalls 拼装、状态机（idle/queued/running/error）、CLI/mock 双实现 | 两个 backend 的差异被一个 switch 完全吸收；`runCtx.Err()` 的三处检查点是 cancel 语义的唯一实现 |
| `agent.Agent.Snapshot/viewMap`（manager.go:732/751） | 锁内一致拷贝，前端与测试共用同一 seam | 消除了「读历史 vs worker append」的竞争；**channel 侧缺这个，正是 Finding 4 的裸指针问题的解** |
| `agent.Manager.Prompt` + `workerLoop`（manager.go:392/448） | 队列/背压（32 缓冲）/queue-full 错误/状态联动 | 是「单聊 = 串行排队」这个本质差异的载体，删掉它就得在调用方重建队列 |

### shallow module（可 inline / 可合并）

| module | 证据 | 判定理由 |
|---|---|---|
| `channel.Manager.Broadcast`（channel.go:233） | 唯一生产调用方 `main.go:319`；20 行，无策略 | 只是 `AddMessage` + 一个 members 过滤循环；删掉后调用方用 `Get` + 遍历即可（`Get` 因此从 0 调用方变 1）。真正的深度在它组合的两个原语里 |
| `channel.Manager.Get`（channel.go:170） | 生产 0 调用方；纯 `map` 读取；且发出裸指针 | 既没有 leverage 也没有隐藏；删掉零影响（除 4 处测试） |
| `[channel:xxx]` marker seam（main.go:360 ↔ manager.go:770） | 生成侧 `fmt.Sprintf`、解析侧 `strings.Index`，无共享常量/类型；把 channel id 塞进 user content；用户可伪造；只支持单频道；一次回写穿两次 emitter seam | 典型的 shallow seam：interface（一个字符串）比 implementation（「哪个 agent 在哪个频道被唤起并需要回写」）**还窄**，导致信息在 seam 两侧丢失（Finding 2 的 6 条丢失清单）。改 `PromptRequest{ChannelID}` 可在 3 处收敛 |
| `parseMentions` ×2（main.go:379 / AgentView.tsx:121） | 同一职责两份实现，语义已分叉：后端按空白切词+剥标点、返回 `[]string`；前端正则 `/@(\S+)/g`、同时返回 `{targets, message}`（顺带从正文里删掉提及） | 没有单一事实源；`parseMentions` 在 ChannelView 里**根本不存在**（`ChannelView.tsx` 自带一套 `lastIndexOf("@")` 补全逻辑，`ChannelView.tsx:66-77`）→ 同一概念三份实现 |
| `isRelevantToRole`（main.go:396） | 生产 1 调用方，零单测 | 硬编码关键词表；它是在 channel 与 agent 之间的孤儿逻辑（不属于任何一方的 interface），且 `ROLE_PRESETS`（`src/lib/presets.ts:1`）的 role 是长句，与关键词表的短 key（"后端" 等）不匹配 → 实际几乎恒返回 false。**待确认**：是否已被 `main.go:344` 的 `len(mentions) == 0` 条件架空 |
| `ChannelView` 的 `onDelete` 死分支（ChannelView.tsx:148-156） | `src/App.tsx:78` 的 `<ChannelView>` 只传 `channel / agents / onSend`，`onDelete` 恒为 `undefined` → `{onDelete && ...}` 永不渲染 | 一整套 UI（confirm 文案「消息将一并清除」+ danger 按钮样式）挂在永不满足的条件上；与后端缺 `Delete` 同源（Finding 3/4），是「设计有、实现无」的残留 |
| channel 的 `Get/List` 返回裸 `*Channel`（channel.go:170/178） | 调用方在锁外读切片（main.go:308、main_test.go:213）；与 `AddMessage` 的 `Lock()` 竞争 | 与 `agent.Snapshot` 形成对照：同一复杂度在一个 module 里被解决、在另一个里被泄露 |
| 双份 `goSave/saveMu/saveWG/waitSaves` + 双份原子写（channel.go:36-67,85-99 / manager.go:66-178,186-188） | 逐字重复 | 抽成共享实现可删约 120 行，且把有测试覆盖的那一份的保证带给另一份 |

### 一句话结论

channel 是**在 agent 旁边重新搭了一遍的 mini-manager**：集合 + 保序 + 异步落盘的部分逐字重复（可合并，62% 形状重合），投递与执行的部分是真空缺（不是重复，是没做）。两个体系之间唯一的 seam 是一个**放在 user message 文本里的字符串标记**——这个 seam 比它连接的 implementation 窄得多，因此频道上下文、其他 agent 的发言、投递状态、消息类型全部在缝合处丢失。群聊当前的 module 深度集中在「消息进数组并落盘」，不集中在「多个 agent 共享一个会话」。
