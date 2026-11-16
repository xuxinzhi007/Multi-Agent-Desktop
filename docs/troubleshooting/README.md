# 问题沉淀与学习机制

这个目录解决一件事：**同类 bug 不再重复出现，出现了也能被快速定位。**

历史事实：这个仓库里「亮色主题发灰」「事件契约断裂」「数据竞争」「停止按钮失效」「发完消息界面没反应」「白屏」都是**修过、又换个形状回来过**的。原因不是能力问题，是修的时候只修了现象、没留下任何东西挡住第二次。

所以这里的规则是：**修 bug 不算完成，留下能复现的护栏才算完成。**

---

## 1. 什么时候必须记一条

满足任意一条就记：

| 触发条件 | 例子（本仓库真实发生） |
|---|---|
| 用户报「没反应 / 卡住 / 灰 / 白屏」这类体验级症状 | 发消息后界面无反馈、停止按钮失效、亮色发灰 |
| 根因不在报错的那一行，而在上游或跨层契约 | 后端 emit 了前端没人订阅；`http.Client` 没有 timeout 打满 worker |
| 需要看两个以上文件/两个语言才能解释清楚 | Go 侧 `nil` 切片序列化成 `null` → 前端 `.length` 崩 |
| 测试/工具本身在空转，掩盖了问题 | `tsc --noEmit` 因为 references-only tsconfig 什么都没检查 |
| 代码改对了但数据/历史还在坏 | 旧 `agents.json` 里的 `[ch-xxx]`、`你: ` 前缀继续显示 |
| 同一模块第二次出问题 | channel 包：先是 `sync.Mutex` 值拷贝，后是返回值泄漏内部指针 |

**不用记的**：拼写错误、单点改动、只影响本地临时脚本的问题。

---

## 2. 每条记录的结构（五段，缺一不可）

```markdown
### 标题（现象一句话）

- **现象**：用户看到什么。要具体到「哪个界面、哪个动作、看到什么」，不要写「功能异常」。
- **根因**：实际出问题的机制，给出文件:行 或 commit。禁止写「可能是…」——不确定就继续查。
- **为什么没被及早发现**：这是最重要的一段。要么是护栏缺失，要么是护栏空转，
  要么是护栏测的是错的东西（例如 e2e 用 mock，永远走不到真实后端路径）。
- **解决方案**：改了什么，为什么这样改而不是别的改法。
- **预防措施与护栏**：一条一条对应到具体测试文件 / 脚本 / 运行命令。
  写不出对应护栏的预防措施，等于没写。
```

**硬性要求**：`为什么没被及早发现` 和 `预防措施` 两段不允许为空。如果确实是「纯粹的新功能缺失」，那就在这两段写明「此前无此路径，护栏从 0 建立」，并给出新建的测试文件。

---

## 3. 记录如何与护栏关联

本仓库的护栏分三层，每条已知问题必须挂到其中至少一层：

### 3.1 前端行为护栏：`e2e/*.spec.ts`（Playwright）

| 文件 | 守的是什么 |
|---|---|
| `e2e/theme.spec.ts` | 亮 / 暗两套主题的**实际渲染色值**与文字对比度（直接读 `getComputedStyle`） |
| `e2e/channel.spec.ts` | 群聊不白屏、`members: null` 不崩、`@` 提及渲染成 chip |
| `e2e/message-interaction.spec.ts` | `@` chip、消息列表交互 |
| `e2e/streaming-perf.spec.ts` | 高频 `agent.delta` 下不掉帧（用 rAF 计掉帧，不是数 DOM） |
| `e2e/fleet.spec.ts` | 舰队视图布局（卡片不出视口、检查器不吃空间） |
| `e2e/smoke.spec.ts` | 应用能渲染出 `.c-shell`（不是白屏） |
| `e2e/send-repro.spec.ts` | 发送后消息**立刻**出现在对话里（乐观更新） |

按症状找文件的快捷方式见 `playbooks/`。

### 3.2 后端 / 契约护栏：`go test`

| 文件 | 守的是什么 |
|---|---|
| `main_contract_test.go` → `TestEveryBackendEventIsSubscribed` | Go 侧每个 `mygo.NewEvent[T]("x.y")` 事件，前端 `src/lib/useBackend.ts` 或 `src/mini.tsx` 必须有人订阅 |
| `internal/llm/openai_test.go` → `TestStreamTimesOutOnHangingUpstream` | 上游挂起时 `Stream` 必须在 `StreamTimeout` 内返回错误 |
| `internal/agent/manager_test.go` → `TestStopReturnsToIdleAndStaysThere` / `TestPromptAfterStopWorks` | 停止后回到 `idle` 且不被覆盖；停止后新消息仍能执行 |
| `internal/agent/stopcli_test.go` | 停止 CLI backend 时事件流必须结束，而不是卡在 `for range events` |
| `internal/cli/proc_test.go` | 取消 ctx 后子进程组被杀，`stdout` 被释放（否则 scanner 永不退出） |
| `main_test.go` → `TestMockPromptDoesNotLeaveAgentBlocked` | mock 对话结束后不能卡在假 `blocked` 状态 |
| `internal/channel/channel_test.go` | 返回值必须是副本，并发下无竞争（配合 `-race` 才有意义） |

### 3.3 静态护栏：`npm run verify`

```bash
npm run verify
# = npm run typecheck && go vet ./... && go test -race ./... && npm run test:e2e
```

两个关键点，别改回去：

- `typecheck` 必须是 `tsc -p tsconfig.app.json --noEmit`。**根 `tsconfig.json` 是 references-only，`tsc --noEmit` 用它什么都不检查**（见 `known-issues.md` 第 8 条）。
- `go test -race` 而不是 `go test`。channel 包的数据竞争只有在 `-race` 下才会红。

### 3.4 关联写法

在 `known-issues.md` 每条末尾用固定一行挂护栏，便于 `grep` 汇总：

```markdown
护栏：`e2e/theme.spec.ts`（亮色底近白 / 侧栏非半透明黑 / 暗角≠底色 / 对比度>7:1）
```

没有现成护栏时，只允许两种写法：

```markdown
护栏：无 —— 缺口已登记在第 4 节待办
```

或者当场补一条护栏再提交。**不允许空着。**

---

## 4. 定期回顾

### 4.1 每次提交前（强制性）

`npm run verify`。它已经是上面三层护栏的合体，跑不过就不提交。

### 4.2 每修完一个 P0/P1（即时）

1. 在 `known-issues.md` 追加一条记录；
2. 补 / 指向护栏；
3. 如果现有护栏**测不出**这个 bug，说明护栏有问题，先修护栏再修 bug —— 否则你无法证明自己修好了。

### 4.3 每两周（或每 ~10 个提交）

用 `git log` 做一次归因巡检：

```bash
# 看最近哪些提交是 fix，fix 里重复出现的关键词就是系统性缺口
git log --oneline -30 --grep='^fix' -i

# 统计踩坑集中区
git log --oneline -50 --name-only | grep -E '^(src|internal)/' | sort | uniq -c | sort -rn | head -20
```

产出三件事：

- 新问题补进 `known-issues.md`；
- **同一文件出现两次以上**的 → 检查是否需要结构性改动（例如 `channel.go` 两次 ← 所有权收口）；
- 护栏的「最后验证时间」过期的 → 更新本文档第 3 节表格或补用例。

### 4.4 每次发布前

逐条过 `known-issues.md`，对每条执行它的护栏用例，确认仍然是绿的。任何一条变红，不许发版。

---

## 5. 文档约定

- **禁止编造**。现象/根因都要能指到 `git show <hash>` 或 `文件:行`。查不到就写「未核实」，不要猜。
- **commit 是证据来源**。本仓库的提交信息写得比较完整（根因 + 验证命令），写记录时先 `git show <hash>` 读全文，别只看标题。
- 一个 bug 一条记录，不做「杂项修复」合并条目。
- 修法被回滚过，保留原记录并在末尾追加「复现」小节，不要删。
