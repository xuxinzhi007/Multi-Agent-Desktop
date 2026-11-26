# 浏览器直连桌面（Web 模式）

浏览器打开的就是**桌面同一套界面**：同一份前端、同一个 `globalThis.mygo` 签名、同一条事件流。数据来自桌面进程内嵌的网关，不再维护第二套网页。

## 怎么开

1. 桌面端先跑起来（网关跟着进程起，默认 `127.0.0.1:8780`）：

   ```bash
   npm run dev
   ```

   跨设备访问需要带 token（也可用 `MAD_GATEWAY_ADDR` 换绑定地址）：

   ```bash
   MAD_GATEWAY_TOKEN=你的token MAD_GATEWAY_ADDR=0.0.0.0:8780 ./multi-agent-desktop
   ```

2. 起浏览器入口：

   ```bash
   npm run dev:web -- --host 0.0.0.0
   ```

3. 打开：

   ```
   http://127.0.0.1:5173/?api=http://127.0.0.1:8780&token=你的token
   ```

   `api` 省略时默认 `http://127.0.0.1:8780`；token 会存进 localStorage。
   换设备（Tailscale / 局域网）把地址换成那台机器的 IP，并确保网关绑了 `0.0.0.0` 且配了 token。

## 启动判定（顺序不能调）

1. **原生窗口**：等宿主注入 `globalThis.mygo`；
2. **浏览器 + 桌面在跑**：探测 `/api/status`，装上同签名的 HTTP runtime——`call` 走 `POST /api/rpc`，`on/once` 走 `GET /api/events`；
3. **两者都没有**：才落回预览替身（假数据，标题带「预览 ·」）。

前两步先后有意义：桌面窗口里注入比 React 启动慢，不等就会把桌面误判成预览。

## 接口

| 用途 | 接口 |
|---|---|
| 方法调用（与 `mygo.call` 同签名） | `POST /api/rpc {method, args}`，反射路由到 `*API` 的导出方法 |
| 事件流 | `GET /api/events`（SSE，同一个 Broker） |
| 审批 | `GET /api/approvals`、`POST /api/approvals/{id}` |
| 入站 / 绑定（飞书等渠道用） | `POST /api/inbound`、`POST /api/bindings` |

`/api/rpc` 不是白名单，而是复用桌面同一份方法表——这样不会出现「桌面有、网页没有」的第二套 API。

## 安全边界

- 默认只绑 `127.0.0.1`；非回环必须配 token，否则 401。
- CORS 只回显**回环来源**或**与请求 Host 同源**的来源；`Origin` 不在允许范围直接 403，不再写 `Access-Control-Allow-Origin: *`。
- `/api/rpc` 对带 `Origin` 的浏览器请求强制要求 `X-MAD-Web: 1`。该自定义头会触发预检，配合来源白名单挡住「任意网页借浏览器打本机网关」的 CSRF 通道；命令行/桌面宿主无 `Origin`，不受影响。
- SSE 的 `EventSource` 带不了请求头，只对 `GET /api/events` 接受 `?token=`；其它接口仍只认 `Authorization: Bearer`。
- 写操作（建会话、改配置等）仍由界面自身的确认流程负责；审批裁决本身即确认。

## 已知边界

- 页面本身不由网关托管：需要 vite / 静态站点另外起（`npm run dev:web` 或 `npm run build:web` 后托管 `dist`）。
- 预览替身仍存在，但只在两种宿主都不可达时启用；远程连接失败不会静默落回假数据。
- SSE 不做断点续传；断线重连从新事件开始。
