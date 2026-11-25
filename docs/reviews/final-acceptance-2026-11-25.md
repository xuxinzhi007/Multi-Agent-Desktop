# 最终验收清单（2026-11-25）

> **2026-11-26 收尾更新**：网关已嵌入主进程并接真实 Session/Approval；记忆与计划调度已接入；全量 Playwright **133 passed**。
> 本文保留为命令清单与审计清单，当前状态与已知风险以 [`status-2026-11-26.md`](status-2026-11-26.md) 为准。

分支：`fix/p0-correctness-stopgap`。本轮**不 commit、不 push**，由主 Agent 审计后决定提交。
本清单覆盖本轮五条并行线的合并验收；每条线先各自跑构建/测试，全量验证在 C（前端）完成后统一执行。

---

## 1. 必跑命令

```powershell
go build ./...
go vet ./internal/agent/... ./internal/runtime/... ./internal/approval/... ./internal/feishu/... ./internal/gateway/... ./internal/memory/... ./internal/scheduler/...
go test ./internal/runtime/... ./internal/tool/... ./internal/agent/... ./internal/approval/... ./internal/feishu/... ./internal/config/... ./internal/llm/... -count=1
go test ./internal/gateway/... ./internal/memory/... ./internal/scheduler/... -count=1
go test . -count=1
npm run typecheck
npm run build:web
npx playwright test                     # C 完成后跑全量
```

说明：

- **Windows 不跑 `go test -race`**：需要 cgo 与 C 编译器；本轮以普通 `go test` 为准，race 留在 CI 的 macOS/Linux。
- `npm run build:web` 在 PowerShell 下可能因 Vite 的 chunk 体积警告显示 exit 1，**以输出出现 `✓ built` 为准**。
- `npx mygo generate` 需要 Bun 或 Node ≥ 22.6；本机 Node 20 会直接报错。`src/mygo.ts` 只能由生成器更新，禁止手改。

## 2. 通过标准

| 项 | 标准 | 状态 |
|---|---|---|
| native 默认路径 | 老 `agents.json` 读入即 native；UI 事件序列与改造前一致 | 待最终全量 |
| native 走统一协议 | 每轮经 `rt.Native` 的 Prompt → Events → `turn.finished` → Close | 已由单测覆盖 |
| ACP 会话 | 可选运行时可配置并落盘；未填命令是中文可读错误 | 已由单测覆盖 |
| ACP 流式与状态 | 文本/思考/工具开始结束映射到旧事件；`runtimeState` idle/error 正确 | 已由单测覆盖 |
| ACP 审批 | 允许/拒绝真实回传；超时/停止/关闭 fail-closed，绝不自动放行 | 已由单测覆盖 |
| ACP 进程回收 | 停止/删除/轮末 Close 杀进程组，无残留 | 待端到端自查 |
| 工具面 | grep/glob/apply_patch 已注册；grep 带行号与 include/exclude；apply_patch 陈旧锚点整批拒绝 | 已由单测覆盖 |
| 前端 IA | 状态条只显示真实数据；导航分组；三页面四态；无假数据 | 已完成；typecheck/build 通过，相关 e2e 48 通过 |
| 全量 e2e | `npx playwright test` 全绿 | **133 passed**（网关 5 + 记忆/计划 21 + 既有 107） |
| 前端运行时 UI | 新建/编辑可选 native/ACP；徽标与连接状态真实；审批复用 | 已完成：`CreateAgentModal` + `RuntimeChip` + `RuntimeBadge`；typecheck/build 通过，runtime e2e 6/6，全量 e2e 覆盖 |
| 飞书白名单 UI | ownerOpenId 只读、approverOpenIds 每行一个、保存回显 | 已完成：SettingsModal FeishuBoard；feishu-whitelist e2e 2/2 |
| 飞书审批 P0 | 陌生人不能消费/决定他人的卡；owner 自动补写 | 主 Agent 已验证 |
| 子会话审批卡 | 父会话可见、不误关；子会话结束不残留 | 主 Agent 已落盘：`feishu.Hooks.Root` + `Manager.RootID`；approval/feishu/agent 测试通过 |
| 审批超时 | 跟随会话生命周期，长任务不被固定 5 分钟误杀 | 主 Agent 已落盘：`defaultTimeout` 5m→30m，超时仍拒绝、ctx 取消优先；有 > Supervisor 10m 的测试 |
| 网关 M3 | 入站幂等、绑定、回执、出站、审批转发、SSE、token | **已完成并接入**：host 嵌入 main；`/api/status` 四项 wired true；`feishu` 入站 409；GatewayPage 真实数据；5 条 e2e |
| 记忆/调度 | SQLite FTS5 中文可搜、迁移幂等；静默/限频/退避有测试 | **已完成并接入**：`memory.db` 自动写入 + `memory_search`；`schedules.json` + 30s 心跳；Memory/Plan 页真实数据；21 条 e2e |

## 3. diff 审计（提交前逐条过）

1. `git status --short` 只包含预期文件；没有无关模块被顺手改掉。
2. **没有 CRLF 伪改动**：`git diff --check` 干净；`gofmt -l` 只对改动文件，未对整目录 `-w`。
3. 工作区没有 `*.syso`、`dist/`、截图、临时 spec、调试打印或 `.orig`/`.rej` 残留。
4. `src/mygo.ts` 只由 `npx mygo generate` 更新；字段与 Go 结构体逐项对得上，没有手写补丁。
5. `go.mod` 变化只有 `modernc.org/sqlite` 及其间接依赖；没有其它新依赖。
6. `main.go` 里**飞书 owner/白名单 hunk 与 runtime hunk 同时存在**，没有被任何一条线回退。
7. 文件所有权无交叉：
   - C：只 `src/**`、`e2e/**`
   - A：`internal/approval/**`、`internal/feishu/**`、`internal/agent/**`（审批部分）、`api_feishu.go`
   - D1：`internal/gateway/**`、`cmd/mad-gateway/**`、`docs/assistant-gateway-api.md`
   - D2：`internal/memory/**`、`internal/scheduler/**`、`go.mod`/`go.sum`（仅 SQLite）
   - E（本清单）：`docs/**`
8. 运行 `git diff --stat HEAD` 人工过一遍增删行数，确认没有大段无关改动。

## 4. 已知延后（本轮不做，交付时明确写）

- 独立常驻网关；当前是桌面内嵌形态。
- 网关/记忆/计划页已经接真实数据，不再是禁用占位。
- ACP 附件/图片、ACP `plan` / `usage` 的 UI 呈现。
- 回合时间线折叠、失败恢复三动作（`§6.4`）。
- 记忆摘要/遗忘曲线、计划编辑表单与 UI 静默配置。
- Windows 本地 `go test -race`。

## 5. 风险

- `modernc.org/sqlite` 首次编译体积/耗时明显高于纯手写包；若后续要瘦身可换外部服务，但当前不引入 cgo。
- 网关已进主进程，但评审指出的 Broker 关闭通道 panic 与 `main.go` gw 竞态仍未修；稳定使用前优先处理，详见 `status-2026-11-26.md`。
- 前端 e2e 依赖 mock 运行时的字段扩展；契约变更时要同步 `e2e/mock-runtime.ts`。

## 6. `docs/ccc..md` 结论

- 内容确认：521 行，标题 `# Code Review Summary`，是本轮改动的英文代码评审汇总（runtime 协议、ACP 客户端、三工具、飞书 P0、前端 IA），含 mermaid 图与逐文件点评。
- 现状：已被改名为 `docs/reviews/code-review-2026-11-25.md`（521 行、34,450 字节，内容完整）；旧路径 `docs/ccc..md` 已不存在。改名不是本清单执行，未回退。
- 注意：正文含历史编码乱码（如 `鈥?`），后续不要顺手重写正文，避免引入无关 diff。
