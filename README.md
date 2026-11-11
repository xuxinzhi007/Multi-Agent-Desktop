# multi-agent-desktop

跨平台的**多 Agent 开发工具**桌面程序：开 N 个 AI agent，各自独立会话，同时干活。

```
Tauri 2 (Rust 壳)  +  React 19 (前端)  +  Go sidecar (后端)
```

灵感来自 [Tempest](https://github.com/tempestai-dev/tempest)（多 agent + worktree 并行）
和 [Berd](https://github.com/block/berd)（壳 / sidecar 分离）。

---

## 现在的真实状态

> 请先读这一段，避免期待错位。

**已完成（W1 + W2）**

- Tauri 壳自动拉起 / 关闭 sidecar，进程干净退出
- 前端 ↔ sidecar WebSocket 全双工通信
- 多个 agent 会话并存，各自独立历史、状态、颜色
- token 级流式渲染，运行中可中断
- shell 命令执行，右侧面板看 stdout / stderr / exit code
- 模型 provider 可切换：Mock / Ollama / OpenAI 兼容 / DeepSeek / Claude

**还没做（W3 + W4）**

- ❌ **agent 不能自己执行代码** —— 目前 shell 需要你手动输入，agent 干活靠字
- ❌ **没有 git worktree 隔离** —— 多个 agent 改同一份代码会互相踩
- ❌ **没有 Supervisor** —— 任务要你手动分配给每个 agent，不能自动拆解
- ❌ **会话不持久化** —— sidecar 重启即清空
- ❌ 小鲸鱼桌宠素材在 `assets/pet/` 躺着，还没接进 UI
- ❌ 没有打包产物（dmg / msi / deb）

**一句话**：现在是「多个 AI 聊天窗口并排」，还不是「多个 AI 在你代码上并行干活」。
前者已经能用，后者是 W3 的核心目标。

---

## 能立刻体验的部分

```bash
cd ~/Documents/gowork/multi-agent-desktop
npm run tauri dev
```

窗口打开后：

1. 左下「**+ 新建 Agent**」→ 输入名字，选角色预设（前端 / 后端 / 测试 / 架构 / Reviewer）
2. 重复建 3 个，各发一条消息 —— 三个会话**同时**流式输出，互不干扰
3. 底部切「**终端**」→ 输入 `ls -la`、`git status` 等，右侧面板出现执行结果
4. 右上「**⚙ 设置**」→ 切模型 provider

### 关于默认的 Mock 模式

本机没有 Ollama 时，provider 默认是 `mock`：回复由本地规则生成，
**不是真实分析**，只用来验证「多 agent 并行 / 流式渲染 / 会话隔离 / 停止」这几件事。

想要真实能力，装 Ollama：

```bash
brew install ollama
ollama serve
ollama pull qwen2.5-coder:7b
```

然后在设置里选 **Ollama**（默认 `http://127.0.0.1:11434` + `qwen2.5-coder:7b` 已填好）→ 保存。

或者填 OpenAI / DeepSeek 的 Base URL + API Key。Key 只存在 sidecar 进程内存里，不落盘。

---

## 环境要求

| 工具 | 版本 | 备注 |
|---|---|---|
| Go | 1.22+ | 本机 1.25.8，在 `~/sdk/go1.25.8/bin` |
| Node | 22+ | 本机 26.13，配 npm 11 |
| Rust | 1.90+ | `rustup default stable-aarch64-apple-darwin` |
| Xcode CLT | — | macOS 必需，验证 `xcrun --show-sdk-path` |
| git | 任意 | W3 需要，当前仅用于版本管理 |

> **非交互 shell 注意**：Bash 工具读不到 `~/.zshrc`，先手动加 PATH
> ```bash
> export PATH="$HOME/sdk/go1.25.8/bin:$HOME/go/bin:$HOME/.cargo/bin:$PATH"
> ```

---

## 开发

```bash
npm install                    # 前端依赖
./tools/build-sidecar.sh       # 交叉编译四平台 sidecar 到 sidecar/bin/
cp sidecar/bin/sidecar-darwin-arm64 \
   src-tauri/binaries/sidecar-aarch64-apple-darwin   # Tauri externalBin 要求带 target triple
npm run tauri dev              # 起前端 + 壳 + 自动拉起 sidecar
```

`src-tauri/binaries/` 被 gitignore（6MB 二进制不入库），**换机器或清理后必须重新生成**，
否则启动会报 sidecar 找不到。

只调后端：

```bash
cd sidecar && go run . --port 17389 --workdir ..
```

只调前端（不启壳，连已有 sidecar）：

```bash
npm run dev    # http://localhost:5173
```

---

## 架构

```
┌────────────────────────────────────────────────┐
│ Tauri 2 壳 (Rust)                              │
│  启动时 spawn sidecar，退出时 kill，转发日志     │
│  src-tauri/src/lib.rs                          │
├────────────────────────────────────────────────┤
│ React 19 前端                                   │
│  Sidebar        agent 列表 / 状态 / 增删         │
│  AgentView      消息流 / 流式气泡 / 双模输入     │
│  ShellPanel     命令执行记录                    │
│  SettingsModal  provider / key / 工作目录       │
├────────────────────────────────────────────────┤
│ Go sidecar  (127.0.0.1:17389)                  │
│  internal/server   WS hub + 命令分发            │
│  internal/agent    N 个隔离会话                 │
│  internal/llm      mock + OpenAI 兼容 SSE       │
│  internal/pty      跨平台 shell 执行            │
└────────────────────────────────────────────────┘
```

通信全是 localhost WebSocket + JSON，没有自研二进制协议。

### 目录

```
multi-agent-desktop/
├── HANDOVER.md              ← 交接文档（自包含全部上下文）
├── README.md
├── docs/
│   ├── research.md          ← 候选开源项目调研与选型
│   └── architecture.md      ← 详细架构 + 跨平台坑清单
├── src/                     ← React 前端
│   ├── lib/ws.ts            WS 连接 + 自动重连 + 类型
│   ├── lib/useSidecar.ts    事件流 → UI 状态的 reducer
│   ├── lib/presets.ts       角色预设 / provider 默认值
│   └── components/*.tsx
├── src-tauri/               ← Rust 壳
│   ├── src/lib.rs           sidecar 生命周期管理
│   ├── capabilities/        权限白名单（shell:allow-spawn）
│   └── binaries/            交叉编译产物（gitignore）
├── sidecar/                 ← Go 后端
│   ├── main.go              入口，解析 flag / env
│   └── internal/
│       ├── llm/             mock / openai / 接口定义
│       ├── agent/           会话管理
│       ├── pty/             命令执行
│       ├── server/          WS hub + dispatch
│       ├── supervisor/      W3 占位（见 README）
│       ├── worktree/        W3 占位（见 README）
│       └── events/          W3 占位（见 README）
├── assets/pet/              小鲸鱼 14 张 png（未接入 UI）
└── tools/build-sidecar.sh   四平台交叉编译
```

---

## 协议

### 前端 → sidecar

```jsonc
{ "action": "agent.create", "data": { "name": "前端", "role": "React 专家" } }
{ "action": "agent.delete", "data": { "id": "agent-xxx" } }
{ "action": "agent.stop",   "data": { "id": "agent-xxx" } }
{ "action": "agent.prompt", "data": { "agentId": "agent-xxx", "text": "..." } }
{ "action": "shell.run",    "data": { "agentId": "agent-xxx", "command": "ls -la" } }
{ "action": "llm.config",   "data": { "provider": "ollama", "baseUrl": "...", "model": "..." } }
{ "action": "workdir.set",  "data": { "dir": "/path/to/project" } }
```

### sidecar → 前端

| 事件 | 含义 |
|---|---|
| `bootstrap` | 首次连接推全量快照（agents / llm / workDir / version） |
| `agent.created` / `agent.deleted` | 会话增删 |
| `agent.status` | `idle` / `running` / `done` / `error` |
| `agent.delta` | 流式片段（token 级） |
| `agent.message` | 一条完整消息（user 或 assistant 落定） |
| `agent.error` | 该会话出错 |
| `shell.start` / `shell.result` | 命令开始 / 结果 |
| `llm.config` | provider 变更（单播确认带 `origin: true`） |
| `workdir` | 工作目录变更 |

### 环境变量

sidecar 支持用环境变量预设 provider，省得每次点设置：

```bash
MAD_LLM_PROVIDER=ollama
MAD_LLM_BASE_URL=http://127.0.0.1:11434
MAD_LLM_MODEL=qwen2.5-coder:7b
MAD_LLM_API_KEY=sk-...
```

---

## 路线图

| 周 | 目标 | 状态 |
|---|---|---|
| W1 | Tauri 壳 + sidecar WS 通信 | ✅ 2026-11-11 |
| W2 | 多 agent 并行 + 流式对话 + shell | ✅ 2026-11-11 |
| W3 | git worktree 隔离 + Supervisor 编排 | ⬜ 下一步 |
| W4 | 小鲸鱼桌宠 + 三端打包 | ⬜ |

W3 要解决的正是「现在感觉怪」的根因：**让 agent 真的能改代码，且互不打架**。

---

## 已知问题

- **Mock 回复是占位的**，别当真实分析看
- **agent 不会自己用终端** —— `shell.run` 是前端发起的，跟 agent 推理链没打通
- **长命令会卡住** —— shell 是同步等结果，没有流式输出，跑 `npm install` 只能干等
- **`llm.config` 曾出现过自激回环** —— 已用「单播确认 + 排除发起者广播」修复，有回归测试
- **workdir 自动推断**只在开发时有效（cwd 在 `src-tauri/` 时取父目录）

---

## 文档

- [HANDOVER.md](HANDOVER.md) —— 交接文档，换机器 / 换会话照它继续
- [docs/research.md](docs/research.md) —— 开源项目调研、技术选型对比
- [docs/architecture.md](docs/architecture.md) —— 详细架构、跨平台坑点清单

## License

待定。参考的 Tempest / Berd / Goose 均为 Apache 2.0。
小鲸鱼素材来自 `dsh-deepseek-pet`，授权见其 `ASSET_LICENSE.md`。
