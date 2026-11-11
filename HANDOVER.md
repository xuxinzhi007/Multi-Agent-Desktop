# HANDOVER — 多 Agent 桌面程序项目交接文档

> 这份文档自包含所有上下文。换机器 / 换会话 / 让别人接手，照这份文档就能继续。
> 最后更新：2026-11-11（W1 已完成）

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
├── src-tauri/              ← Rust 壳（待建）
├── src/                    ← React 前端（待建）
├── sidecar/                ← Go 后端
│   ├── main.go             ← 当前：/healthz 健康检查
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
- [ ] W2：单 agent + PTY + Ollama 对话
- [ ] W3：多 agent 并行 + git worktree
- [ ] W4：搬小鲸鱼 UI + 三端打包 dmg/msi

## 8. W1 已验收内容（2026-11-11）

实跑验证通过：

- `npm run tauri dev` 起桌面窗口，Rust 壳 `setup` 阶段自动 spawn sidecar
- sidecar 监听 `127.0.0.1:17389`，提供 `/healthz` + `/ws`
- 前端 `src/lib/ws.ts` 连 WS，收到 `hello` 握手 + 每 3 秒 `ping` 心跳
- 窗口关闭时 `RunEvent::Exit` 杀掉 sidecar 子进程（无残留）

实测事件流：
```
hello    {"service":"multi-agent-sidecar","version":"0.1.0"}
ping     {"clients":1}
ping     {"clients":1}
```

### 关键文件

| 文件 | 作用 |
|---|---|
| `sidecar/main.go` | HTTP + WS hub + 3s 心跳广播 |
| `src/lib/ws.ts` | `fetchHealth()` / `connect()` |
| `src/App.tsx` | 状态灯 + 事件流面板 |
| `src-tauri/src/lib.rs` | spawn sidecar、转发日志、退出时 kill |
| `src-tauri/capabilities/default.json` | `shell:allow-spawn` 白名单 |
| `src-tauri/tauri.conf.json` | `bundle.externalBin = ["binaries/sidecar"]` |

### 下次启动前必做

`src-tauri/binaries/` 被 gitignore，二进制要重新生成：

```bash
./tools/build-sidecar.sh
mkdir -p src-tauri/binaries
cp sidecar/bin/sidecar-darwin-arm64 src-tauri/binaries/sidecar-aarch64-apple-darwin
```

## 9. 下一步具体动作（W2）

1. sidecar 加 `internal/pty`（`creack/pty`），起 zsh/powershell 会话
2. 加 `internal/llm`，OpenAI 兼容 client 接远端 Ollama
3. `/ws` 增加双向消息：前端发 `{action:"chat", prompt}`，sidecar 流式回 `token` 事件
4. 前端加一个输入框 + 流式输出区

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
