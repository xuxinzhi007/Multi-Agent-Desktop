# HANDOVER — 多 Agent 桌面程序项目交接文档

> 这份文档自包含所有上下文。换机器 / 换会话 / 让别人接手，照这份文档就能继续。
> 最后更新：2026-11-11（W2 已完成，可完整体验）

---

## 1. 项目一句话

做一个**跨平台（macOS / Windows）的多 Agent 开发工具桌面程序**，形态参考 Grok Bot / Claude Code Agent Teams：N 个 AI agent 并行干活，每个隔离在独立 git worktree，桌面端能看到每个 agent 在干嘛、能派发任务。吉祥物是一只蓝色小鲸鱼。

## 2. 为什么不是直接用 DSH 插件

原项目 `/Users/xuxinzhi/.dsh/plugins/dsh-deepseek-pet/` 是 DSH（DeepSeek Harness）的 Web UI 插件，**不是独立桌面程序**。用户想要一个脱离 DSH、自己能跑、能跨机器分发的独立 App。原插件保留不动，本项目只复用它的素材和人设。

## 3. 最终架构（已决策）

融合两个开源项目的优点：
- **Tempest**（github.com/tempestai-dev/tempest, Apache 2.0）→ 多 agent 并行 + git worktree 隔离
- **Berd**（github.com/block/berd, Apache 2.0）→ 壳 / sidecar 分离架构

```
┌─ Tauri 2 Shell (Rust) ──────────────────────────┐
│  窗口/托盘/自动更新/拉起 sidecar                  │
├─ React 19 + Vite 前端 ──────────────────────────┤
│  Agent 舰队面板 / worktree UI / 小鲸鱼挂件        │
├─ Go Sidecar (127.0.0.1:17389) ─────────────────┤
│  supervisor / worktree / pty / llm / events      │
│  LLM Router: Ollama(本地) + Claude + DeepSeek    │
└──────────────────────────────────────────────────┘
```

通信：Tauri 壳通过 localhost WebSocket ↔ Go sidecar；前端通过 WebSocket 收事件流。

## 4. 技术栈（已决策）

| 层 | 选型 | 理由 |
|---|---|---|
| 桌面壳 | Tauri 2 | 30MB 体积，sidecar 一等公民 |
| 前端 | React 19 + Vite + Tailwind + shadcn/ui | Berd/Tempest 同款 |
| Sidecar | Go 1.22+（本机是 1.25.8） | 作者主场，交叉编译方便 |
| PTY | `creack/pty` | 跨平台事实标准 |
| Git | `os/exec` 调系统 git | go-git 的 worktree 支持不全 |
| LLM | OpenAI 兼容 client | Ollama 本身就是 OpenAI 兼容 |
| 包管理 | npm（不用 pnpm） | 本机已有；`npm create vite` 默认 |

**明确不选**：Electron（太重）、Flutter Desktop（前端生态不如 React）、AGPL/BSL 协议项目（Golutra/MyAgents 排除）。

## 5. 目录结构

```
~/Documents/gowork/multi-agent-desktop/
├── HANDOVER.md             ← 本文档
├── README.md
├── docs/
│   ├── research.md         ← 候选开源项目调研
│   └── architecture.md     ← 详细架构 + 跨平台坑
├── src-tauri/              ← Rust 壳（已建，自动拉起 sidecar）
├── src/                    ← React 前端（已建，舰队面板）
├── sidecar/                ← Go 后端
│   ├── main.go             ← HTTP + WS 入口
│   ├── internal/
│   │   ├── llm/            ← mock + openai 兼容流式
│   │   ├── agent/          ← 多会话管理
│   │   ├── pty/            ← 跨平台 shell
│   │   └── server/         ← WS hub + 命令分发
│   └── go.mod
├── assets/pet/             ← 小鲸鱼素材（14 张 png，从 dsh-pet 复制）
└── tools/
    └── build-sidecar.sh    ← 三端交叉编译脚本
```

## 6. 环境准备

### 本机（macOS, Apple Silicon）现状

| 工具 | 状态 | 路径 / 备注 |
|---|---|---|
| Go 1.25.8 | ✅ | `~/sdk/go1.25.8/bin/go`，PATH 已在 `~/.zshrc` 配置 |
| Node 26.13 + npm 11.20 | ✅ | 系统已有 |
| Rust / cargo | ✅ 1.99.1 | `~/.cargo/bin`，toolchain `stable-aarch64-apple-darwin` |
| git | ✅ | 2.50.1 |
| Ollama | ❌ 本机未装 | 用 N100 小主机跑，sidecar 配远端地址即可 |

### Rust 已就绪（2026-11-11 修复）

初装时 toolchain 中断过（`bin/` 缺 rustc），已重装：

```bash
rustup toolchain install stable --profile minimal
rustup default stable-aarch64-apple-darwin
```

Xcode CommandLineTools ✅ SDK 26.4。

### ⚠️ 环境检查坑（已踩过）

用非交互 shell（比如 Bash 工具）查命令是否存在时，**PATH 读不到 `~/.zshrc`**。要先手动 export：

```bash
export PATH="$HOME/sdk/go1.25.8/bin:$HOME/go/bin:$HOME/.cargo/bin:$PATH"
```

否则会误判"go 没装"。

## 7. 当前进度

- [x] 调研完成（`docs/research.md`）
- [x] 架构决策完成（`docs/architecture.md`）
- [x] 项目骨架建立
- [x] Go sidecar 最小骨架（`/healthz` 跑在 17389）
- [x] 小鲸鱼素材 14 张复制到 `assets/pet/`
- [x] **W1**：Tauri 壳 + React 前端 + sidecar WS 通信跑通 ✅ 2026-11-11
- [x] **W2**：多 agent 并行 + 流式对话 + shell 执行 ✅ 2026-11-11
- [ ] W3：git worktree 隔离 + Supervisor 编排
- [ ] W4：搬小鲸鱼 UI + 三端打包 dmg/msi

## 8. W2 已交付（2026-11-11）

### 能体验什么

1. `npm run tauri dev` 起窗口
2. 左侧「+ 新建 Agent」，选角色预设（前端/后端/测试/架构/Reviewer）
3. 建 3 个 agent，各自发消息 —— 三个会话同时流式输出，互不干扰
4. 切「终端」模式发命令，右侧面板看 stdout / exit code
5. 「⚙ 设置」切模型 provider（默认 Mock，无需任何配置）

### 后端结构（sidecar）

| 包 | 职责 |
|---|---|
| `internal/llm` | `Client` 接口 + `mock`（规则引擎）+ `openai`（兼容 Ollama/OpenAI/DeepSeek/Claude），SSE 流式解析 |
| `internal/agent` | `Manager` 管理 N 个会话，每个 `Agent` 独立历史/状态/cancel，并发安全 |
| `internal/pty` | 跨平台 shell 执行，`Setpgid`(unix) / `CREATE_NEW_PROCESS_GROUP`(win) |
| `internal/server` | WS hub（单写 goroutine + 心跳 + 慢客户端淘汰）、命令分发 |
| `internal/{supervisor,worktree,events}` | W3 占位，内含 README 说明规划 |

### WS 协议

前端 → sidecar（`{action, data}`）：

| action | data | 说明 |
|---|---|---|
| `agent.create` | `{name, role}` | 建会话 |
| `agent.delete` | `{id}` | 删会话 |
| `agent.stop` | `{id}` | 取消正在跑的推理 |
| `agent.prompt` | `{agentId, text}` | 发消息，流式回流 |
| `shell.run` | `{agentId, command}` | 执行命令 |
| `llm.config` | `{provider, baseUrl, apiKey, model}` | 切模型 |
| `workdir.set` | `{dir}` | 切工作目录 |

sidecar → 前端（`{event, data, ts}`）：

`bootstrap` `agent.created` `agent.deleted` `agent.status` `agent.delta` `agent.message` `agent.error` `shell.start` `shell.result` `llm.config` `workdir` `error`

### 前端结构

| 文件 | 职责 |
|---|---|
| `src/lib/ws.ts` | WS 连接（自动重连退避）、类型定义 |
| `src/lib/useSidecar.ts` | 单一 state + reducer，把事件流折叠成 UI 状态 |
| `src/lib/presets.ts` | 角色预设、provider 选项、默认 base/model |
| `src/components/Sidebar.tsx` | agent 列表、状态点、新建/删除 |
| `src/components/AgentView.tsx` | 消息流、流式气泡、对话/终端双模输入 |
| `src/components/ShellPanel.tsx` | 命令执行记录、stdout/stderr/exit |
| `src/components/SettingsModal.tsx` | provider 切换、API Key、工作目录 |

### 两个关键设计取舍

1. **Mock provider 兜底**：`llm.New()` 遇到未识别 provider 直接回落 mock，保证任何环境下应用都能启动并演示。避免"没装 Ollama 就白屏"。
2. **`llm.config` 单播确认 + 排除发起者广播**：避免前端收到广播后又发一次导致自激循环（已实测回归：保存一次只收到 1 次事件）。

### 已知限制（W3 要补）

- 会话不持久化，sidecar 重启即清空
- 没有 git worktree 隔离，多个 agent 改同一份代码会冲突
- shell 是同步返回，长命令期间 UI 只能等 `shell.result`
- 没有 Supervisor，任务需人工分配给每个 agent

## 9. 下一步具体动作（W3）

1. `internal/worktree`：每个 agent 一个 worktree，`git worktree add` 编排
2. `internal/supervisor`：任务拆解 → 分发 → 汇总
3. `internal/events`：事件总线抽包 + 断线重放
4. 会话持久化（sidecar 侧落盘，或前端 localStorage 缓存）
5. 小鲸鱼桌宠组件接进 UI

## 10. 关键决策记录

| 决策 | 选了什么 | 为什么不选别的 |
|---|---|---|
| 壳 | Tauri 2 | Electron 100MB vs Tauri 30MB；Berd/Tempest 都验证过 |
| Sidecar 语言 | Go | 作者熟；交叉编译一条命令出三端；不污染前端进程 |
| 多 agent 隔离 | git worktree | Tempest 的核心做法，避免并行 agent 改同一份代码冲突 |
| Agent 运行时 | 自研（不直接套 Goose） | Goose 是 Rust，作者想写 Go；ACP 协议可后补 |
| License | 只 fork Apache 2.0 项目 | Golutra 是 BSL、MyAgents 是 AGPL，有传染性 |
| 小鲸鱼素材 | 从 dsh-pet 复制 | 原插件保留不动，素材已获原作者授权（见 dsh-pet/ASSET_LICENSE.md） |

## 11. 参考链接

- Tempest: https://github.com/tempestai-dev/tempest （Apache 2.0）
- Berd: https://github.com/block/berd （Apache 2.0）
- Goose: https://github.com/block/goose （Apache 2.0，Agent runtime 参考）
- Tauri sidecar 文档: https://tauri.app/develop/sidecar/
- MCP 协议: https://modelcontextprotocol.io/
- A2A 协议: https://a2a-protocol.org/

## 12. 可复用资产来源

| 资产 | 来源 | 说明 |
|---|---|---|
| 14 张桌宠 png | `~/.dsh/plugins/dsh-deepseek-pet/assets/small/` | 已复制到 `assets/pet/` |
| 人设文案池 | dsh-pet `reducer.js` 里的 `COPY` 对象 | 待搬 |
| mood 列表（30+ 姿势） | dsh-pet `index.js` 里的 `MOODS` 常量 | 待搬 |
| SSE 事件驱动思路 | dsh-pet `index.js` 的 broadcast 模式 | 待改成 WebSocket |
| 原插件 README | `~/.dsh/plugins/dsh-deepseek-pet/README.md` | 桌宠交互逻辑参考 |
