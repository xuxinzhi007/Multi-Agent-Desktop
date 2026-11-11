# 多 Agent 桌面程序调研报告

> 整理时间：2026-11-11
> 目标：做一个独立的、跨平台（macOS / Windows）的多 Agent 开发工具桌面程序，可参考 Grok Bot / Claude Code Agent Teams 形态。

---

## 一、整体架构共识

主流多 Agent 桌面程序都是三层分离（参考 OpenHands / Berd / Tempest）：

```
┌─ 桌面壳 (Shell)        Electron / Tauri / Flutter Desktop
├─ 前端 UI (Frontend)    React/Vue + 状态管理
├─ Agent 运行时 (Sidecar) Python / Go / Node，常驻独立进程
└─ LLM 接入层            云端 API + 本地 Ollama
```

**核心原则：Agent 不跑在前端进程里**，sidecar 独立常驻，前端通过 HTTP/WebSocket/SSE 通信。

---

## 二、桌面壳选型对比

| 维度 | Electron | Tauri v2 | Flutter Desktop |
|---|---|---|---|
| 包体积 | ~100MB | ~30MB | ~20MB |
| 内存 | 高 | 低 | 低 |
| 前端栈 | React/Vue 生态最成熟 | React/Vue/Svelte | Dart |
| sidecar 支持 | 自由 | 原生 `externalBin` | 自己管进程 |
| 原生能力 | Node.js 全套 | Rust 插件 | Dart FFI |
| 代表项目 | Claude Desktop, Cursor | Berd, Tempest, LobeHub | 较少 |

**选定：Tauri v2**。理由：体积小、sidecar 一等公民、Berd/Tempest 都验证过。

---

## 三、候选开源项目对比（二次开发友好度排序）

### 🏆 第一梯队

| 项目 | 技术栈 | License | 多 Agent 形态 | 仓库 |
|---|---|---|---|---|
| **Tempest** | Tauri 2 (Rust) + React | Apache 2.0 | N 个 agent 并行，每个隔离在独立 git worktree | github.com/tempestai-dev/tempest |
| **Berd** (Block 官方) | Tauri 2 + React 19 | Apache 2.0 | 壳 + Goose sidecar，ACP 协议 | github.com/block/berd |
| **Golutra** | Vue 3 + Rust Tauri | BSL-1.1 ⚠️ | 编排 Claude Code/Codex/Gemini CLI | github.com/golutra/golutra |

#### Tempest 亮点
- 多 agent 并行 + git worktree 隔离是核心卖点
- 支持后端：Claude Code / Aider / OpenCode / Copilot CLI / Cline / Goose / 任意 terminal agent
- 本地优先，无云
- Apache 2.0 无法律包袱

#### Berd 亮点
- Block（Square）官方，91 contributors，v0.6.2
- **壳和 runtime 完全分离**：Berd 不实现 agent loop，Goose sidecar 通过 ACP（Agent Client Protocol）WebSocket 通信
- Goose 已捐给 Linux 基金会 AAIF
- 你可以把 Goose sidecar 换成自己的 Go runtime

### ❌ 不推荐

| 项目 | 原因 |
|---|---|
| Golutra | BSL-1.1，源码可见但不能商业化 |
| MyAgents | AGPL-3.0 强传染，改完必须开源全部代码 |
| Void / PearAI | VS Code fork，体积巨大，改不动 |
| TalkCody | 刚开源一周，成熟度未知 |
| Chatbox / Cherry Studio | 聊天客户端，不是 coding agent 工作台 |
| 从零自研 | 重复造轮子 |

---

## 四、关键协议认知

| 协议 | 解决什么 | 定位 |
|---|---|---|
| **MCP** (Anthropic) | Agent ↔ 工具/数据源（垂直） | 给 Agent 装"手"：文件、终端、API |
| **A2A** (Google) | Agent ↔ Agent（水平） | 不同 Agent 互相委派任务 |
| **ACP** (Block/Goose) | 桌面壳 ↔ Agent runtime | Berd 用的 WebSocket 协议 |

实践：Agent 内部用 MCP 挂工具，Agent 之间用 A2A 或本地函数调用，壳和 sidecar 用自定义 WebSocket JSON-RPC 即可（不必硬上 ACP）。

---

## 五、多 Agent 编排模式

- **Supervisor / Worker**：一个主管 Agent 拆解任务，分发给专职 Worker
- **Shared State / Blackboard**：所有 Agent 读写的共享上下文
- **Mailbox / Task List**：Agent 间异步通信（Claude Code Agent Teams 用 JSON 文件 mailbox 实现）
- **Git worktree 隔离**：每个并行 agent 一个独立分支目录，避免冲突（Tempest 的核心做法）

---

## 六、对原 dsh-deepseek-pet 项目的定位

原项目 `/Users/xuxinzhi/.dsh/plugins/dsh-deepseek-pet/` 是 **DSH（DeepSeek Harness）的 Web UI 插件**，不是独立桌面程序。可复用资产：

- `assets/small/*.png`：14 张桌宠表情素材（idle/happy/wave/sleepy/curious/shy/surprised/review/eat/read/work/hug/dance/drink）
- 人设文案池（reducer.js 里的 COPY）
- mood 列表（30+ 姿势）
- SSE 事件驱动思路

**原插件保留不动**，新项目是独立的桌面程序，只复制素材和文案。
