# multi-agent-desktop

跨平台多 Agent 开发工具桌面程序。

**架构**：Tauri 2 (Rust 壳) + React 19 前端 + Go sidecar。
**灵感**：Tempest（多 agent + git worktree 并行）× Berd（壳/sidecar 分离）。
**吉祥物**：复用 dsh-deepseek-pet 的蓝色小鲸鱼。

## 状态

✅ W1 完成（2026-11-11）

- [x] 目录结构
- [x] 调研文档 `docs/research.md`
- [x] 架构设计 `docs/architecture.md`
- [x] 桌宠素材 `assets/pet/`（14 张）
- [x] Go sidecar 最小骨架（`/healthz` @ 17389）
- [x] React 19 + Vite + TS 前端脚手架
- [x] **W1: Tauri + Go sidecar WS 通信跑通** ✅ 2026-11-11
- [ ] W2: 单 agent + Ollama ← 下一步
- [ ] W3: 多 agent + worktree
- [ ] W4: 三端打包

## 快速开始

```bash
# 1. 装工具链（见 docs/architecture.md 第六节）
brew install go
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh

# 2. 启动 sidecar（Go）
cd sidecar && go run .

# 3. 构建 sidecar（放 src-tauri/binaries/）
./tools/build-sidecar.sh
cp sidecar/bin/sidecar-darwin-arm64 src-tauri/binaries/sidecar-aarch64-apple-darwin

# 4. 启动前端 + Tauri 壳（壳会自动拉起 sidecar）
npm install
npm run tauri dev
```

## 文档

- [交接文档](HANDOVER.md)
- [调研资料](docs/research.md)
- [架构设计](docs/architecture.md)