# CI 与提交卫生：落地方案

> 整理日期：2026-11-25
> 范围：第 3 点「CI 和提交卫生」。结构拆分（第 2 点）、产品定位（第 4 点）、审批归属（第 5 点）另文。
> 结论口径：本文是方案，不是现状描述。除「现状盘点」一节，其余都还没落地。

---

## 0. 一句话结论

门禁脚本其实已经写好了（`package.json` 的 `verify`），缺的只是「自动触发」。

但**今天接 CI 会立刻红**：全量 `go test ./...` 有一个失败用例，而且测试与实现已经分叉。所以顺序是「先让绿灯成立 → 再挂门禁 → 再切提交」，不是先写 yaml。

---

## 1. 现状盘点（实测，2026-11-25 19:40 左右）

| 项 | 现状 | 证据 |
|---|---|---|
| Go 版本 | go.mod 写 `go 1.27.3`，本机 go1.27.4 | `go.mod` |
| 测试规模 | internal 69 个测试文件；根包 9 个（其中 4 个带 `//go:build e2e`，默认不跑）；e2e 23 个 spec | `find`, `e2e/` |
| 全量耗时 | **2m46s**；`internal/agent` 165s、`internal/llm` 127s 是大头 | `time go test ./...` |
| 全量结果 | **FAIL**：`internal/llm/TestContextLimitUsesWindowOrProvider`，期望 4096，实现返回 8192 | `compact_test.go:10` vs `compact.go:15` |
| 既有门禁脚本 | `npm run verify` = typecheck + vet + `test -race` + e2e，**已存在** | `package.json` |
| 自动触发 | 无。仓库没有 `.github/`、没有 `.githooks/`、没有 husky/lefthook | `ls -a` |
| lint | 只有 `.oxlintrc.json`，**oxlint 没装**（node_modules/.bin 里没有） | `ls node_modules/.bin` |
| 远端 | GitHub `xuxinzhi007/Multi-Agent-Desktop` | `git remote -v` |
| 分支状态 | `fix/p0-correctness-stopgap`，ahead 3，未推送 | `git status -sb` |
| 工作区 | 23 条改动、约 4000 行；含 2 个 `MM`、1 个 `AM`、1 个未跟踪 | `git status --porcelain` |
| 测试污染源 | `tmp-gp-context-lab/` 被 `.gitignore` 的 `tmp-*/` 忽略，但 `go test ./...` 仍会扫进去 | `find` 结果里有它的包 |

`MM` / `AM` 的具体文件：`src/components/ChannelView.tsx`、`src/styles/components.css`（MM），`src/components/MentionInput.tsx`（AM）。未跟踪：`e2e/channel-readability.spec.ts`。

---

## 2. 先修红灯：`ContextLimit` 到底是 4096 还是 8192

这是接 CI 前唯一的阻塞项，而且必须当成一个决策来做，不能「改测试让它过」。

- `compact.go:14-15`：`llamacpp` / `ollama` 返回 **8192**
- `compact_test.go:9-10`：断言 **4096**，失败信息 `llamacpp=8192`

两种情况只能选一个：

1. **8192 是新默认**（本地模型上下文确实变宽了）→ 改测试断言，commit message 写清「本地模型上下文上限从 4096 提到 8192」。
2. **4096 才是有意设计**（`EstTokens` 估高、留余量的策略）→ 改 `compact.go` 回去，并补一条注释说明为什么不能提。

判断依据：`git log -S "return 8192" -- internal/llm/compact.go` 只命中 `01e84f3`，而 `e7b89ec`（今天）也动过这个文件。改之前先看这两次提交的意图，别把「临时调大调试」当成「新默认」。

> 这件事本身就是「没有 CI」的代价：代码和测试分叉了，没人发现。修完之后这条要写进提交信息，作为「为什么现在要接 CI」的第一手证据。

---

## 3. 三层门禁

| 层 | 触发 | 跑什么 | 预算 | 失败怎么办 |
|---|---|---|---|---|
| pre-commit | `git commit` | 只查暂存区的 `.go`：`gofmt -l`；`.ts/.tsx` 的 lint（需先装 oxlint，见 §5） | <2s | 阻断，提示改哪个文件 |
| pre-push | `git push` | `npm run typecheck` + `go vet ./...` + `go build ./...` + **快速包测试**（见下） | <30s | 阻断 |
| CI | push / PR | `go vet` + `go build` + `go test -race ./internal/... .` + `npm run typecheck` + `npx playwright test` | 6-10 分钟 | 阻断合并 |
| nightly（可选） | 定时 | `mygo build` 三平台交叉编译 + 需要真窗口的 e2e | — | 只通知 |

**pre-push 的「快速包」**：实测 <10s 的包，够覆盖治理核心，又不至于让人想加 `--no-verify`。

```
internal/loop internal/tool internal/tool/builtin internal/approval
internal/audit internal/channel internal/shellguard internal/workdir
internal/worktree internal/knowledge internal/config internal/pty
```

排掉 `internal/agent`（165s）和 `internal/llm`（127s），这两个放 CI。`internal/feishu`（5.3s）可留可去，建议留——它是审批卡链路，值得每次 push 都验。

---

## 4. 要落的文件

### 4.1 `.github/workflows/ci.yml`

```yaml
name: ci

on:
  push:
    branches: [main, "fix/**", "feat/**"]
  pull_request:

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  go:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.27.3"
          cache: true
      - run: go vet ./...
      - run: go build ./...
      # 显式列包，避免扫到 tmp-gp-context-lab
      - run: go test -race ./internal/... .

  web:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: "22"
          cache: "npm"
      - run: npm ci
      - run: npm run typecheck
      - run: npx playwright test
```

三点必须注意：

- **`go test` 显式用包路径而不是 `./...`**。`tmp-gp-context-lab/` 虽然被 gitignore，但 `./...` 会扫到它，CI 里那是「本地有、远端没有」的不确定源。更彻底的做法是把它改名成 `_tmp-gp-context-lab`（Go 忽略 `_` 开头目录），或者挪出仓库。
- **e2e 用系统 Chrome**。`playwright.config.ts` 里写死 `channel: 'chrome'`，`ubuntu-latest` 自带 google-chrome-stable，不需要 `playwright install`。如果第一次跑因缺系统库失败，再加一步 `npx playwright install-deps chrome`。
- **`-race` 先只给 CI，不给 pre-push**。165s 的 agent 包带 race 会明显更慢，本地体验会崩，人就学会 `--no-verify` 了。

### 4.2 `.githooks/pre-commit`

```sh
#!/bin/sh
# 只查已暂存的文件，不碰整个工作区——否则改一行要等全仓库扫描。
set -e

files=$(git diff --cached --name-only --diff-filter=ACMR)

bad=$(printf '%s\n' "$files" | grep '\.go$' | xargs -r gofmt -l 2>/dev/null || true)
if [ -n "$bad" ]; then
  echo "gofmt 未通过："
  echo "$bad"
  echo "修：gofmt -w <上面的文件>，再 git add"
  exit 1
fi

ts=$(printf '%s\n' "$files" | grep -E '\.(ts|tsx)$' || true)
if [ -n "$ts" ] && [ -x node_modules/.bin/oxlint ]; then
  node_modules/.bin/oxlint $ts
fi
```

### 4.3 `.githooks/pre-push`

```sh
#!/bin/sh
set -e

echo "→ typecheck"
npm run typecheck

echo "→ go vet + build"
go vet ./...
go build ./...

echo "→ 快速包测试"
go test ./internal/loop ./internal/tool ./internal/tool/builtin \
        ./internal/approval ./internal/audit ./internal/channel \
        ./internal/shellguard ./internal/workdir ./internal/worktree \
        ./internal/knowledge ./internal/config ./internal/pty ./internal/feishu

echo "✓ pre-push 通过（全量与 e2e 由 CI 跑）"
```

启用：

```bash
cd ~/Documents/gowork/multi-agent-desktop
chmod +x .githooks/pre-commit .githooks/pre-push
git config core.hooksPath .githooks
```

**为什么不用 husky**：钩子会进 `.git/hooks` 或依赖 `npm prepare`，多一层 node 依赖，换机器容易失效。`core.hooksPath` 是 git 原生能力，克隆后跑一条命令就生效，且钩子文件本身可以进版本控制（团队其他人只需要执行同一条 config）。

### 4.4 `package.json` 增补

```json
"verify:fast": "npm run typecheck && go vet ./... && go build ./... && go test ./internal/loop ./internal/tool/... ./internal/approval ./internal/channel ./internal/shellguard ./internal/workdir ./internal/worktree ./internal/knowledge ./internal/config ./internal/feishu",
"verify:full": "npm run typecheck && go vet ./... && go test -race ./internal/... . && npm run test:e2e"
```

`verify` 保留原样（它已经是全量口径），`verify:fast` 给日常用、给 pre-push 用。

---

## 5. lint 的坑：`.oxlintrc.json` 存在但工具没装

现在 `package.json` 的 devDependencies 里没有 oxlint，`node_modules/.bin` 里也没有。两个选择：

1. 加依赖：`npm i -D oxlint`，钩子里直接调本地 bin。CI 里 `npm ci` 后也能用。**推荐这个**，离线、版本确定。
2. 不加依赖：钩子里用 `npx --yes oxlint@latest`——每次联网拉包，慢且版本漂移，不适合门禁。

注意 oxlint 是 Rust 写的、速度很快，适合 pre-commit。但它是 lint 不是 typecheck，**别拿它替代 `tsc --noEmit`**，类型错误它抓不到。

---

## 6. 提交切分：23 条改动切成 4 个提交

当前最大的风险不是代码错，是**同一批文件同时有 staged 和 unstaged 两份改动**（`MM`）。这种状态下直接 rebase 或切分支，很容易丢一半。

### 6.1 先保命

```bash
cd ~/Documents/gowork/multi-agent-desktop
git branch backup/pre-split-20261125
git stash create > /tmp/mad-stash-id.txt   # 记一个可回退的 commit 对象
```

### 6.2 按主题分四组

| 组 | 文件 | 提交信息 |
|---|---|---|
| 1 测试与 e2e | `e2e/channel-live.spec.ts`、`e2e/channel-mentions.spec.ts`、`e2e/channel-social.spec.ts`、`e2e/channel.spec.ts`、`e2e/channel-readability.spec.ts`、`internal/agent/scene_isolation_test.go` | `test(channel): 补 @提及、场景隔离与可读性用例` |
| 2 后端 | `internal/agent/doubao_backend.go`、`internal/agent/loop_backend.go`、`internal/llm/chat.go`、`internal/llm/compact.go` | `fix(agent): 群回合场景隔离与上下文上限对齐` |
| 3 前端 | `src/App.tsx`、`src/main.tsx`、`src/components/ChannelView.tsx`、`src/components/MentionInput.tsx`、`src/components/FleetView.tsx`、`src/components/PermissionPicker.tsx`、`src/components/ProfileCard.tsx`、`src/lib/contextMeter.ts`、`src/lib/preview-runtime.ts`、`src/lib/useBackend.ts`、`src/styles/components.css` | `feat(channel): 提及输入组件与权限选择器补齐` |
| 4 依赖 | `package.json`、`package-lock.json` | `chore(deps): 依赖与脚本更新` |

### 6.3 执行序列

```bash
git add -A
git restore --staged .

# 组 1
git add e2e/channel-live.spec.ts e2e/channel-mentions.spec.ts \
        e2e/channel-social.spec.ts e2e/channel.spec.ts \
        e2e/channel-readability.spec.ts internal/agent/scene_isolation_test.go
git commit -m "test(channel): 补 @提及、场景隔离与可读性用例"

# 组 2
git add internal/agent/doubao_backend.go internal/agent/loop_backend.go \
        internal/llm/chat.go internal/llm/compact.go
git commit -m "fix(agent): 群回合场景隔离与上下文上限对齐"

# 组 3
git add src/App.tsx src/main.tsx src/components/ChannelView.tsx \
        src/components/MentionInput.tsx src/components/FleetView.tsx \
        src/components/PermissionPicker.tsx src/components/ProfileCard.tsx \
        src/lib/contextMeter.ts src/lib/preview-runtime.ts \
        src/lib/useBackend.ts src/styles/components.css
git commit -m "feat(channel): 提及输入组件与权限选择器补齐"

# 组 4
git add package.json package-lock.json
git commit -m "chore(deps): 依赖与脚本更新"
```

### 6.4 每切完一组先验一遍

```bash
npm run typecheck && go build ./...
```

**如果组 2 和组 3 分开后中间态编译不过**（场景隔离开关前后端一起改才会通），就别硬拆——把 2 和 3 合成一个提交，提交信息写 `feat(channel): 提及输入与场景隔离（前后端）`。拆提交的目标是「每个提交都能编译」，不是「每条消息都好看」。

---

## 7. 验收标准

这份方案算完成，要同时满足下面 7 条：

- [ ] `ContextLimit` 的红灯修掉，全量 `go test ./...` 绿（修之前记录 2m46s 基线）
- [ ] `npm run verify:fast` 本地 <30s 通过
- [ ] `npm run verify` 全绿（含 e2e）
- [ ] push 后 Actions 的 `go` 和 `web` 两个 job 都绿
- [ ] **反向验证**：临时删 `shellguard` 一条规则，pre-push 红；恢复后绿（测试不是假通过）
- [ ] **反向验证**：故意提交一个 gofmt 不合规的 `.go`，pre-commit 拦住
- [ ] 工作区清空、4 个提交、`ahead 7`，`git status` 干净

最后一条反向验证很重要。你们在 `docs/reviews/review-recheck-2026-11-25.md` 里已经实践过「回退代码看测试是否真失败」，这次同样：门禁必须证明它会拦，而不是证明它会绿。

---

## 8. 不做什么（避免把门禁做成负担）

- **不在 pre-commit 跑测试**。测试 3 分钟，人第一件事就是学会 `--no-verify`，门禁等于没有。
- **不在 pre-push 跑全量**。`internal/agent` 单包 165s，加上 race 更久。
- **不引 husky / lefthook**。多一层 node 生命周期，换机器会静默失效。
- **不把 `-race` 放进日常钩子**。留给 CI。
- **不在 e2e 里下载 Chromium**。`channel: 'chrome'` 已是既定设计，别为了让 CI 简单去改它。

---

## 9. 与后面几条的衔接

CI 是第 2 点（结构拆分）的前置条件：AgentView 拆层、manager 拆包都是高风险改结构，没有「每次 push 自动跑 23 个 e2e」这道护栏，拆完只能靠手点。

顺序建议仍是：**红灯 → 门禁 → 提交切分 → 前端 Runtime 收口 → 事件桥 → AgentView → manager**。

第 4、5 点（产品定位、审批归属）另文整理；审批那块的第一优先级是「飞书卡片谁能点」的身份校验，它比子会话卡丢失更严重。
