# 网关 HTTP / SSE API 契约（嵌入式）

本文档定义网关 HTTP + SSE 接口，对应架构文档 `docs/assistant-architecture.md`
§7（API 边界）与 §8 的里程碑。

**当前形态：桌面内嵌（2026-11-26 起）。** `internal/gateway` 作为库编进桌面主进程
`main.go`，应用启动即启动 HTTP/SSE，应用退出即关闭，打包后随应用分发。
独立常驻的 `cmd/mad-gateway` 保留为后续形态（N100 / 云主机 / systemd），
本轮不启用，也不由它自己连飞书。

当前只接管 `web` 渠道：

- 桌面现有飞书链路继续走 `main.go` 的 `feishu.NewHub`，**不新增第二条飞书长连接**。
- 网关收到 `feishu` / `telegram` 入站一律拒绝（409 `CHANNEL_NOT_OWNED`），不绑定、不回执、不提交会话。
- 出站同理：只有绑定在网关接管渠道上的 Agent 回复才发；飞书绑定的 Agent 回复由 Hub 发，
  网关跳过并记 `gateway.reply.skipped`，避免重复回复。

## 1. 边界（先读这条）

- **桌面前端 ↔ Go 内核** 继续走 MyGo typed IPC。网关页读的是 MyGo 的
  `API.GatewayStatus` / `GatewayBindings` / `GatewayDecideApproval`，**不**在前端直连本 HTTP API。
  MyGo IPC 没有鉴权、没有速率限制、方法粒度是内部的，禁止暴露到公网（§7）。
- 本 HTTP API 只暴露四类：**会话、消息、审批、状态**。内部运行时细节（loop、工具注册表、
  工作树、消息历史）不出现在响应里。
- 网关通过端口接口向下依赖内核（`SessionPort` / `ApprovalPort` / `Sender` / `Binder`），
  由新增的嵌入式 host 层（`internal/gateway/host`）装配到真实实现：
  `agent.Manager`、`mgr.Approvals()` 的 `approval.Center`、本地/SSE Sender、桌面数据目录下的 FileBinder。
  网关包本身仍不直接 import `internal/agent` / `internal/runtime`。
- 装配失败或端口未接时返回 `NOT_WIRED`，**不伪造会话、不伪造审批数据**。
- 监听失败（端口占用等）只记录 `listenError` 并继续启动桌面，桌面窗口不因网关起不来而失败。

## 2. 通用约定

- 编码：UTF-8 JSON；时间戳为 Unix 秒（`int64`）。
- 鉴权二选一：
  - 配置了 `token`：所有接口要求请求头 `Authorization: Bearer <token>`。
  - 未配置 `token`：只允许回环地址（`127.0.0.1` / `::1` / `localhost`）访问。
- 错误响应信封：

```json
{
  "error": { "code": "NOT_BOUND", "message": "渠道会话未绑定 Agent" }
}
```

### 错误码

| HTTP | code | 含义 |
|---|---|---|
| 400 | `INVALID_BODY` | 请求体不是合法 JSON，或字段缺失/类型错误 |
| 400 | `INVALID_EVENT` | 入站事件缺少 `eventId` / `channel` / `externalChatId` |
| 400 | `APPROVAL_DECIDE_FAILED` | 审批决定转发失败（审批已失效、会话不存在等） |
| 401 | `UNAUTHORIZED` | 缺/错 Bearer token，或非回环地址访问未配置 token 的实例 |
| 404 | — | 路由不存在 |
| 409 | `CHANNEL_NOT_OWNED` | 该渠道不由本网关接管（当前只有 `web`；`feishu` 走桌面 Hub） |
| 422 | `NOT_BOUND` | 该渠道会话尚未绑定本地 Agent |
| 503 | `NOT_WIRED` | 对应端口还没接进真实内核（装配前状态） |
| 500 | `STREAM_UNSUPPORTED` | 响应通道不支持 SSE 流式 |
| 500 | `INTERNAL` | 其它内部错误 |

## 3. 端点一览

| 方法 | 路径 | 说明 | 端口依赖 |
|---|---|---|---|
| POST | `/api/inbound` | 渠道入站事件（幂等去重，立即 ack，异步处理） | Binder, Session, Sender |
| GET | `/api/status` | 网关只读状态汇总 | 全部（可选） |
| GET | `/api/agents` | 会话快照列表 | SessionPort |
| GET | `/api/approvals` | 待审批列表 | ApprovalPort |
| POST | `/api/approvals/{id}` | 转发一次审批决定 | ApprovalPort |
| GET | `/api/bindings` | 渠道 ↔ Agent 绑定列表（只读） | Binder |
| GET | `/api/events` | SSE 事件流 | Broker |

> 入站默认只接收 `web`；`feishu` 入站走桌面 `feishu.Hub`，不经本 HTTP 路由。
> 绑定写入仍不暴露公开 HTTP 路由：绑定由装配/管理入口经 `Binder` 端口落盘。

---

## 4. 入站消息

### POST `/api/inbound`

入站适配器（当前只有网页/本地 `web`）把归一化后的消息投到这里。
同一外部事件 ID 只处理一次；收到后**立即**返回，提交会话是异步的，
回执由 `Sender` 端口发回渠道，结果通过 SSE 可见。

> 渠道归属在入口先判：默认只接管 `web`。`feishu` 入站的唯一入口是桌面
> `feishu.Hub`，投到网关会得到 409 `CHANNEL_NOT_OWNED`，且不产生绑定、回执或会话提交。

**请求体**

```json
{
  "eventId": "web_7d2f…",
  "channel": "web",
  "externalChatId": "web-session-1",
  "senderId": "ou_abc",
  "senderName": "小新",
  "text": "帮我看下最近的日志",
  "mentioned": true,
  "kind": "message"
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `eventId` | 是 | 渠道侧外部消息 ID，幂等去重键 |
| `channel` | 是 | `web`（当前唯一接管）/ `feishu` / `telegram`（非接管时 409） |
| `externalChatId` | 是 | 渠道会话 ID（群 chat_id 或私聊 id） |
| `senderId` / `senderName` | 否 | 发送人身份，审批与白名单校验用 |
| `text` | 否 | 归一化后的纯文本；不可信输入，不做系统指令拼接 |
| `mentioned` | 否 | 是否 @ 机器人；`mentionOnly` 策略下据此判断 |
| `kind` | 否 | `message`（默认）/ `system` |

**响应**

| 状态 | 场景 | 响应体 |
|---|---|---|
| 202 | 已绑定并交给会话排队 | `{"status":"queued","eventId":"…","agentId":"…"}` |
| 200 | 重复事件（已处理过） | `{"status":"duplicate","eventId":"…","agentId":"…"}` |
| 200 | 策略过滤（如未 @ 机器人） | `{"status":"ignored","eventId":"…","agentId":"…"}` |
| 409 | 渠道不由本网关接管（当前 `feishu` / `telegram`） | 错误信封 `CHANNEL_NOT_OWNED` |
| 422 | 未绑定会话 | 错误信封 `NOT_BOUND` |
| 503 | 会话端口未接线 | 错误信封 `NOT_WIRED` |
| 400 | 事件字段缺失 / JSON 非法 | `INVALID_EVENT` / `INVALID_BODY` |

**处理顺序**（服务端）

1. 归一化并校验字段；
2. 渠道归属：`channel` 不在接管集合（默认仅 `web`）→ 发 `inbound.rejected`，返回 409 `CHANNEL_NOT_OWNED`；
3. 按 `channel + ":" + eventId` 幂等去重，重复直接返回；
4. 解析绑定；未绑定 → 发 `inbound.unbound` 事件，返回 `NOT_BOUND`；
5. 策略过滤：`mentionOnly` 且未 `mentioned` 的普通消息 → `ignored`；
6. 经 `Sender` 端口发出即时回执（`ack`）；
7. 异步调用 `SessionPort.Prompt(agentID, text)`；失败进 `inbound.error` 事件流。

---

## 5. 状态与会话

### GET `/api/status`

**200 响应体**

```json
{
  "gateway": true,
  "uptimeSec": 3612,
  "wired": { "session": true, "approval": true, "sender": true, "binder": true },
  "channels": ["web"],
  "bindings": 2,
  "agents": 3,
  "pendingApprovals": 1,
  "sseSubscribers": 1
}
```

| 字段 | 说明 |
|---|---|
| `uptimeSec` | 网关本次运行秒数 |
| `wired` | 各端口是否已接真实实现（桌面装配后四项均为 `true`） |
| `channels` | 网关当前接管的渠道，默认 `["web"]` |
| `bindings` | 绑定条数；Binder 未接线时为 0 |
| `agents` | 会话数；Session 未接线时为 0 |
| `pendingApprovals` | 待审批数；Approval 未接线时为 0 |
| `sseSubscribers` | 当前 SSE 订阅数 |

> 桌面里的「网关」页不直接访问这个 HTTP 路由，而是走 MyGo IPC：
> `API.GatewayStatus` 在以上字段之外再带 `running` / `addr` / `listenError` / `agents`（列表）/
> `pendingApprovals`（列表）/ `bindings`（计数）。这样端口占用时页面能显示原因，而不是一片空白。

### GET `/api/agents`

会话快照列表。Session 端口未接线时返回 503 `NOT_WIRED`；接线后空列表返回 `[]`，不返回 `null`。

```json
{
  "agents": [
    {
      "id": "agent-1",
      "name": "助手",
      "status": "idle",
      "queueLen": 0,
      "lastError": ""
    }
  ]
}
```

`status` 取值：`idle` / `queued` / `running` / `blocked` / `done` / `error`。

### GET `/api/bindings`

```json
{
  "bindings": [
    {
      "channel": "web",
      "externalChatId": "web-session-1",
      "agentId": "agent-1",
      "policy": "mentionOnly",
      "createdAt": 1732500000
    }
  ]
}
```

`policy`：`mentionOnly`（群里只有 @ 才回应）/ `allowAll`（全量回应，默认）。

---

## 6. 审批

### GET `/api/approvals`

待审批卡片列表，按到达先后排序。Approval 端口未接线时 503 `NOT_WIRED`。

```json
{
  "approvals": [
    {
      "id": "ap-1",
      "agentId": "agent-1",
      "agentName": "助手",
      "tool": "apply_patch",
      "title": "修改文件",
      "detail": "patch 说明",
      "diff": "--- a/…",
      "createdAt": 1732500000
    }
  ]
}
```

### POST `/api/approvals/{id}`

转发一次审批决定。决定真实下发给 `ApprovalPort`；审批已失效或已完成时返回
`APPROVAL_DECIDE_FAILED`。

**请求体**

```json
{ "allow": true, "remember": true, "reason": "放行" }
```

| 字段 | 说明 |
|---|---|
| `allow` | 批准为 `true`，拒绝为 `false` |
| `remember` | `true` 表示本会话以后对这个工具不再询问 |
| `reason` | 决定备注，进审计 |

**200 响应体**

```json
{ "ok": true, "id": "ap-1" }
```

> 身份校验（仅 owner / 白名单可批）在渠道适配器与装配层完成；
> 网关端口本身不做点击人身份判定。桌面「网关」页的审批操作走 MyGo
> `API.GatewayDecideApproval`，与桌面弹窗写同一个 `approval.Center`，先到先得。

---

## 7. SSE 事件流

### GET `/api/events`

`Content-Type: text/event-stream`。消息格式：

```
event: inbound.received
data: {"eventId":"web_…","channel":"web","chatId":"web-session-1","agentId":"agent-1","status":"queued"}

```

- 新连接建立后先收到一条 `gateway.connected`。
- 每 15 秒一条 `: ping` 心跳注释。
- 事件 `data` 为 JSON 对象；总线缓冲满时丢最旧事件，不阻塞发布方。
- 关闭客户端连接即取消订阅。

### 事件清单

| 事件 | 负载 | 说明 |
|---|---|---|
| `gateway.connected` | `{at, subscribers}` | 订阅建立 |
| `inbound.received` | `{eventId, channel, chatId, agentId, status}` | 入站已受理；`status`：`queued` / `duplicate` |
| `inbound.rejected` | `{eventId, channel, reason}` | 渠道不由本网关接管（`reason=CHANNEL_NOT_OWNED`） |
| `inbound.unbound` | `{eventId, channel, chatId}` | 未绑定会话 |
| `inbound.ignored` | `{eventId, channel, chatId, agentId, reason}` | 策略过滤；`reason`：`mentionOnly` |
| `inbound.error` | `{eventId, agentId, error}` | 提交会话或回执失败 |
| `inbound.not_wired` | `{eventId, agentId}` | 会话端口未接线 |
| `gateway.reply` | `{channel, chatId, agentId, text, inReplyTo}` | 已发回渠道的最终回复（仅接管渠道） |
| `gateway.reply.skipped` | `{channel, chatId, agentId, reason}` | 目标渠道不归网关管（如飞书由 Hub 发），已跳过，避免重复回复 |
| `agent.*` | 透传内核事件 | `agent.message` / `agent.status` / `agent.delta` / `agent.channel.reply` / `agent.error` 等，形状沿用内核 Emitter 约定 |
| `tool.approval` / `tool.approval.done` | 透传审批事件 | 新审批到达 / 审批已结束 |

> 内核事件由装配层（`internal/gateway/host`）在桌面 `emit` 里逐条灌入
> `Service.HandleAgentEvent(event, data)`，飞书事件也在其中；出站回复只对网关接管渠道执行，
> 飞书绑定的回复直接跳过并记 `gateway.reply.skipped`，因此不会和 Hub 重复发送。

---

## 8. 持久化

绑定关系落在 **桌面应用数据目录** 下的 `gateway-bindings.json`（即
`mustDataDir()` 返回的目录，与 `agents.json` 同级），单写者原则，
写入走「同目录临时文件 + rename」原子替换：

```json
{
  "bindings": [
    {
      "channel": "web",
      "externalChatId": "web-session-1",
      "agentId": "agent-1",
      "policy": "allowAll",
      "createdAt": 1732500000
    }
  ]
}
```

进程内去重表（已处理事件 ID）是内存态，重启后清空；不做跨进程持久化。

> 不再使用 `~/.multi-agent-desktop/gateway` 这个与桌面分离的目录。
> 独立常驻形态（后续）才会另配数据目录，且需先解决与桌面的单写者互斥。

## 9. 启动配置（嵌入式）

桌面进程启动网关时只认两个环境变量：

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `MAD_GATEWAY_ADDR` | `127.0.0.1:8780` | HTTP 监听地址 |
| `MAD_GATEWAY_TOKEN` | 空 | Bearer token；空则仅允许回环地址访问 |

- 数据目录固定跟随桌面应用数据目录，不读 `MAD_GATEWAY_DATA_DIR`。
- 启动时机：`Manager`、`approval.Center`、`feishu.Hub` 初始化完成后；退出时先关网关再 `Manager.Shutdown`。
- 端口占用或监听失败：写日志 + 在 `GatewayStatus.listenError` 里展示，**不阻断桌面启动**。

独立 `cmd/mad-gateway` 仍保留 `-addr` / `-data-dir` / `-token` 三个 flag（优先级 flag > 环境变量 > 默认值），
但它是**后续形态**，本轮不参与桌面运行，也不自行连接飞书。

## 10. 当前未包含（留给后续里程碑）

- 把 `feishu` 渠道迁进网关（需要先决定 Hub 与网关二选一，避免双长连接/双回复）；
- 绑定 / 解绑的管理 HTTP 接口与界面；
- Telegram 长轮询渠道（M4）；
- 渠道白名单、频率限制、点击人身份校验；
- 主动任务 / 定时调度（M6）、记忆召回（M5）；
- 独立常驻服务形态与公网暴露前的沙箱、审计补全、密钥托管（M7）。
