# 架构设计：Tempest × Berd 融合方案

> 取 Tempest 的"多 agent 并行 + git worktree 隔离"内核，套 Berd 的"壳 / sidecar 协议分离"骨架。

---

## 一、架构图

```
┌─────────────────────────────────────────────────────────┐
│  Tauri 2 Shell (Rust)                                   │
│  ├─ 窗口/托盘/自动更新/文件对话框/系统通知                │
│  ├─ 拉起/监视/退出 sidecar 进程                          │
│  └─ localhost WebSocket ↔ sidecar                       │
├─────────────────────────────────────────────────────────┤
│  React 19 + TS 前端                                     │
│  ├─ Agent 舰队面板（N 个 agent 并排）                    │
│  ├─ git worktree / 分支状态 UI                          │
│  ├─ 小鲸鱼桌宠挂件（复用 dsh-pet 素材）                  │
│  └─ 设置：API Key / Ollama 地址 / 模型选择              │
├─────────────────────────────────────────────────────────┤
│  Go Sidecar（核心，用户主场）                             │
│  ├─ supervisor/   多 agent 编排（Supervisor/Worker）     │
│  ├─ worktree/     每个 agent 一个 git worktree           │
│  ├─ pty/          每个 agent 一个终端会话                │
│  │   ├─ macOS: openpty (creack/pty)                     │
│  │   └─ Windows: ConPTY (creack/pty)                    │
│  ├─ llm/          LLM Router：Ollama / Claude / DeepSeek │
│  ├─ mcp/          MCP Client：挂工具                     │
│  └─ events/       WebSocket 事件总线                     │
└─────────────────────────────────────────────────────────┘
```

## 二、技术栈

| 层 | 选型 | 理由 |
|---|---|---|
| 桌面壳 | Tauri 2 | 30MB，三端一套代码，sidecar 一等公民 |
| 前端 | React 19 + Vite + Tailwind + shadcn/ui | Berd/Tempest 同款，组件现成 |
| Sidecar | Go 1.22+ | 用户主场；交叉编译 `GOOS=windows GOARCH=amd64 go build` 一条命令出三端 |
| 进程通信 | localhost WebSocket + JSON-RPC | 不自研重协议，20 个事件够用 |
| PTY | `creack/pty` | 跨平台事实标准 |
| Git | `os/exec` 调系统 git | go-git 的 worktree 支持不全 |
| LLM | OpenAI 兼容 client | Ollama 本身就是 OpenAI 兼容，统一一个 client |
| 打包 | Tauri CLI + GitHub Actions | push tag 自动出 dmg/msi/deb |

## 三、跨平台坑点清单

| 坑 | macOS | Windows | 解决方案 |
|---|---|---|---|
| sidecar 路径 | `Contents/Resources/` | 同目录 `.exe` | Tauri `externalBin` 配三端后缀 |
| PTY | openpty | ConPTY (Win10 1809+) | `creack/pty` 已封装 |
| 换行符 | LF | CRLF | git `core.autocrlf input`；写文件统一 `\n` |
| 权限 | 全盘访问弹授权 | UAC | 不请求管理员权限；文件操作让用户选目录 |
| 签名 | 必须 notarization | 不签名弹 SmartScreen | 先用 debug 构建；上架前再买证书 |
| 默认 shell | `/bin/zsh` | `powershell.exe` | sidecar 按 `runtime.GOOS` 分支 |
| 路径分隔符 | `/` | `\` | 全程 `path/filepath.Join`，禁止手拼 |
| 进程退出 | kill 进程组 | taskkill /T | `SysProcAttr{Setpgid:true}` / `CREATE_NEW_PROCESS_GROUP` |
| 自动更新 | Tauri updater 需签名 | 同 | v1.0 前关掉 |

## 四、目录结构

```
multi-agent-desktop/
├── src-tauri/              # Rust 壳（几乎不动）
│   ├── src/main.rs
│   └── tauri.conf.json
├── src/                    # React 前端
│   ├── pages/AgentPanel.tsx
│   ├── components/PetWidget.tsx
│   └── lib/ws.ts
├── sidecar/                # Go 后端（核心）
│   ├── main.go
│   ├── internal/
│   │   ├── supervisor/
│   │   ├── worktree/
│   │   ├── pty/
│   │   ├── llm/
│   │   └── events/
│   └── go.mod
├── assets/pet/             # 小鲸鱼素材（从 dsh-pet 复制）
├── tools/
│   └── build-sidecar.sh    # 三端交叉编译
├── docs/
│   ├── research.md
│   └── architecture.md
└── README.md
```

## 五、里程碑（4 周）

| 周 | 目标 | 验收 |
|---|---|---|
| W1 | 骨架跑通：Tauri 空壳 + Go sidecar WS 通信 | mac/win 都能启动，前端收到 sidecar ping |
| W2 | 单 agent 跑通：PTY + Ollama | 一个 agent 能对话、能跑 shell |
| W3 | 多 agent 并行 + worktree | N 个 agent 同时干活，git 不冲突 |
| W4 | 搬小鲸鱼 + 三端打包 | dmg/msi 安装包各一个 |

## 六、环境依赖（本机待装）

| 工具 | 状态 | 安装命令 |
|---|---|---|
| Go 1.22+ | ❌ 未装 | `brew install go` |
| Rust / cargo | ❌ 未装 | `curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs \| sh` |
| pnpm | ❌ 未装 | `brew install pnpm` |
| Tauri CLI | ✅ npx 可用 | `cargo install tauri-cli`（装完 Rust 后） |
| Node 22 | ✅ 已有 | - |
| git | ✅ 已有 | - |
| Ollama | ❌ 本机未装 | `brew install ollama`（或继续用 N100 小主机跑） |
