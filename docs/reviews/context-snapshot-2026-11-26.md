# 上下文快照 · 2026-11-26 13:05

> HEAD `4e8d63a`（12:52「feat: 增强审批与权限管理功能」）· 工作树**干净** · 分支 `fix/p0-correctness-stopgap`
> 本文件是"接手用"的状态快照：改了什么都列了证据行号，哪些文档已过时标了划。

## 0. 一句话

10:59 到现在，仓库被另一个会话推进了一大步：**并行只读工具、verify、审批记忆签名 + 默认 auto、
token 用量记账、网关 broker 修复、CI 全套**，全部已提交。
我刚验过：`go build ./...` 干净；`go test ./internal/loop ./internal/tool ./internal/approval` 全绿（1.9s / 0.8s / 2.2s）。

---

## 1. 这半天落地的（按能力）

| 能力 | 证据 | 说明 |
|---|---|---|
| **只读工具并发** | `loop.go:552-571` 按 `ConcurrencySafe` 切批，`:632` 用 `sync.WaitGroup` | 连续只读合成一批并发，写类独占。**这条之前是 P0-1，已做** |
| 工具接口加 `ConcurrencySafe()` | `tool.go:73`、`Registry.ConcurrencySafe` `:176-187` | 未显式实现默认 `false` —— 安全默认，新工具不会误入并发批 |
| **审批默认 `auto`** | `approval.go:73`（`ParseMode` 兜底返回 auto）、`:92` | auto 靠 `roots` 判断文件是否在工作区内，越界才拦 |
| **审批记忆细化到"命令签名"** | `approval/signature.go` | `git`/`go`/`npm`/`docker`… 取到第二个 token：记 `git status` 不会放过 `git push` |
| 允许记忆键改为「工具 + 命中根」 | `approval.go:89-90` | 不再是裸工具名，换目录要重新记 |
| **`verify` 工具** | `builtin/verify.go`（562 行）+ `verify_test.go`（329 行） | 自动推断 scope：有 `go.mod`→go，有 `package.json`→ts，两者都有→both |
| token 用量记账 | `llm/usage.go`（109 行）+ 209 行测试 | 含 `cached_tokens` / `cache_write`，只记上游给的真数字 |
| 网关 broker 并发修复 | `gateway/broker.go`（+38）、`broker_test.go`（60 行） | 之前 review 里的 #1（send-on-closed） |
| CI | `.github/workflows/ci.yml`（118 行） | build / vet / **指定包** test / 前端 typecheck |
| 我出的 5 份文档 | 同提交 | 被一起收进版本库 |

---

## 2. 之前几份文档，现在哪些条目已失效

| 文档 | 现状 |
|---|---|
| `todo-2026-11-26.md` | **P0-1（并发）已做、P0-2（verify）已做、P1-4（verify 瘦身）已做** → 划掉三项；剩 P1-1 摘要 epoch、P1-2 事实溯源、P1-3 截断回查、P2/P3 |
| `benchmark-cli-speed-2026-11-26.md` | 差距表：①串行已消 ②编辑已对齐 ⑤审批默认已消 ⑥子任务重、⑦目录钉死**仍在** ③压缩 ④缓存**仍在** |
| `direction-cli-first-2026-11-26.md` | 审批降噪部分兑现；**目录工作站、UI 统一、插话真打断**仍未动 |
| `borrow-from-claude-code-2026-11-26.md` | 四条机制里「并发分批」「权限分档」「verify」已对齐；「压缩分层」「缓存边界」未做 |
| `research-breakout-provodelivery-2026-11-26.md` | 分析成立，未开工（按业务判断推迟） |

---

## 3. 现在真正剩的（按业务价值排序）

1. **工作区根 + `bash` 可选 cwd**（跨项目；"收到链接做 review"和"A 项目问 B 项目"共用）
   `builtin.go:116` cwd 仍钉死主目录；`loop_backend.go:459` 提示只写一行 `工作目录：X`。
2. **飞书历史消息**（review 闭环的前提）
   现在只有 `im/v1/messages/{id}/reply` 与 `chats/{id}/members`，没有 list messages。
3. **摘要 epoch 冻结**（长会话每轮多一次模型调用）
   `loop.go:307/326` → `compact`；预算 `sys+8000+400`。
4. **缓存边界**（静态/动态分段 + Anthropic `cache_control`）
   `usage.go` 能统计命中，请求体还不会创造命中。
5. **UI 统一**（`AgentView` / `ChannelView` / `FeishuRoomView` 三套渲染）
6. P2 组：记忆保留策略、调度令牌泄漏（`scheduler.go:195-198`）、截断可回查、`list_dir`。

---

## 4. 需要留意的风险（新增的）

- **默认 `auto` 是安全姿态变化**：新会话默认放行工作区内写操作。要确认界面上"当前是自动模式"是显式的，
  否则用户不知道自己已经在自动放行。
- **命令签名粒度**：`git`/`npm` 取第二 token 是对的；但 `bash -c "…"`、`sh -c` 这类要不要拆开签名**尚未验证**。
- **并发批次的消息顺序**：`concurrency_test.go` 有 258 行覆盖，但我没逐条读；必须保证"结果按原 `Calls` 顺序回填"，
  否则 `assistant(tool_calls)` 与 `tool` 消息会错位。
- **CI 只跑指定包**（`ci.yml:77-78`）：`internal/agent`（单包 164s）这类慢包可能没进 CI，回归会漏。
- **网关 `main.go` 的 `gw` 读写竞态**（`var gw` 在 `:1490`，emit 闭包 `:1515` 读、`:1687` 才赋值）——这次改了 broker，`gw` 那条看着还没动。

---

## 5. 下一步（1.5 天，按业务）

打通"群里丢个链接 → 读 diff → 给 file:line 结论 → 回复"：

1. 工作区根 + cwd（~0.5–1 天）
2. 飞书历史消息（~0.5–1 天）
3. 并发只读**已就绪**，省下原本估的 0.5 天

做完再决定要不要投「可证明交付」（8–9 天，对外价值，非当务之急）。

---

## 6. 文档索引（都在 `docs/reviews/`）

| 文件 | 一句话 |
|---|---|
| `todo-2026-11-26.md` | 逐条核实的待办（部分已被本提交做掉） |
| `benchmark-cli-speed-2026-11-26.md` | 与主流五家的差距清单与速度账 |
| `direction-cli-first-2026-11-26.md` | 方向：工作台优先，不做舰队优先 |
| `borrow-from-claude-code-2026-11-26.md` | 抄哪四条机制、改到哪个文件 |
| `research-breakout-provodelivery-2026-11-26.md` | 市场+论文：可证明交付是空白位，但需 8–9 天 |
| `context-snapshot-2026-11-26.md`（本文） | 当前状态与接手口径 |
