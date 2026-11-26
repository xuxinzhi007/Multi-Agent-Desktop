# 需求：多写者下的写安全（Write Safety）

状态：**提案**，未开发 ｜ 日期 2026-11-26 ｜ 预估 3.5～4.5 人天（P0 部分 2.8d）

## 1. 一句话

同一个仓库被两个以上的会话/子 Agent 同时改时，**不要再让"后写者静默覆盖先写者"**：读的时候记版本，写的时候校版本，冲突就报错让模型重读；想彻底不抢，就默认给每个会话一份工作树。

## 2. 为什么现在必须做（真实事故，不是假设）

今天这条时间线上发生了四次同源事故：

| # | 现象 | 根因 |
|---|---|---|
| 1 | 我改 `src/lib/mygo-http.ts` 的脚本 `assert old in s` 失败 | 另一条会话在我读完之后重写了整个文件头（补上了 `?api=&token=`），我的 `old` 已经不成立 |
| 2 | 批量替换 `CORS Allow-Headers` 断言失败 | 别人往同一行追加了 `X-MAD-Web`，字符串变了 |
| 3 | `internal/agent/manager.go` 一度 `cannot use id (*Agent) as string` 编译失败，几分钟后自己好了 | 我读到了别人**编辑中间态**（未写完的文件） |
| 4 | 桌面点完审批，网页还挂着那张卡 | 同一类根因搬到状态层：多个客户端各自写同一份"真相" |

现在 `git status --short` 有 **73 项未提交改动**，来自至少两条会话 + 我。这不是纪律问题，是**机制缺失**：仓库里没有任何东西能告诉写入方"你手里的这份文件已经不是最新的了"。

## 3. 现状盘点（已有什么、缺什么）

已有，且质量不错，别推倒重来：

- `internal/worktree/worktree.go`：能开真实 git worktree（`.mad-worktrees/<repo>/<slug>`，分支 `mad/<slug>`，共用 `.git`，删目录保留分支）。
- `internal/agent/worktree.go` `EnableWorktree/DisableWorktree`，对外是 `API.SetAgentWorktree(agentId, on)`（`api_agent.go:229`）。
- `internal/tool/builtin/apply_patch.go`：**原子批**——任何一条锚点缺失/不唯一，整批零写入；`Preview` 与 `Execute` 共用同一份 plan。
- `internal/tool/builtin/builtin.go` 的 `edit_file`（`:423`）：`oldText` 必须唯一，不唯一报错而不是替换第一处。
- 写操作走审批，diff 是真的（`WriteFile` 会把 before/after 做 unified diff）。

缺的三件，正好对应上面四次事故：

1. **没有版本校验**：`edit_file` / `apply_patch` / `write_file` 只校验"锚点在不在"，不校验"文件是不是我读到的那一份"。锚点还在、但上下文已被别人改过 → 静默写出半旧半新的文件。
2. **隔离默认关**：worktree 只能手动开。并行是常态，默认单份检出就是默认互相踩。
3. **冲突不可见**：没人知道另一个会话正在改 `useBackend.ts`。事故 1、2 发生时我唯一的信号是"自己的断言脚本挂了"，这已经太晚。

## 4. 目标 / 非目标

目标：

- 任何一次写，只要目标文件在"读"之后被改过，就**必须失败**，并告诉模型"重读再改"，而不是写进去。
- 并行会话默认不共享检出（有 git 就隔离），合并回主分支有可执行的一条命令。
- 用户和模型都能看见"谁在改哪个文件"，冲突在发生前就能避。

非目标（明确不做）：

- 不做自动三方合并 / 语义合并。冲突的语义只有写的人知道，工具替它猜就是制造事故。
- 不做跨机器文件锁（Tailscale 组网下 NFS 式锁不可靠，收益远小于复杂度）。
- 不做"多人实时协同编辑"（CRDT）。这是单机多 Agent，不是 Figma。
- 不改审批模型：写安全是"写之前"的事，不新增第二道审批墙。

## 5. 方案

### P0-A｜读后写版本校验（file token）

`read_file` 的返回里多一个版本号 `v`，由 `size + mtime(ns) + sha1(内容)前 12 位` 组成（三者任一变化都变）。写工具的新旧签名：

```
edit_file   { path, oldText, newText, ifVersion? }
apply_patch { items:[{op,path,oldText?,content?,ifVersion?}] }
write_file  { path, content, ifVersion? }        // 覆盖全文时必填
```

规则：

- `ifVersion` 给了 → 与磁盘当前版本比对；不等 → `STALE_WRITE`，错误里带**当前版本**和**读时版本**，并附"该文件在你读之后被改过（可能来自另一个会话），读一遍再改"。
- `ifVersion` 没给 → 只对**读过的文件**生效：会话内维护一张 `path → {v, at, seq}` 台账（`read_file` 写入；`apply_patch`/`edit_file` 写入后更新）。台账里有的文件必须带 `ifVersion`，否则 `READ_REQUIRED`。没读过的文件（新增）不拦。
- 台账放在 `internal/tool/builtin` 里按会话（`Agent`）隔离，进程内即可，不落盘（重启就失效，重读一次的成本可接受）。

为什么用"版本号"而不是"内容指纹比对后再合并"：合并要判断语义，错判一次就是静默坏代码；报错让模型重读，成本是一次 tool call，最坏结果也只是慢 2 秒。

为什么不用 `flock`：写完即放锁，锁不住"读—想—写"之间那几十秒，也不跨进程可靠；版本校验正好覆盖这段窗口。

验收：

- 单测：读 → 外部 `os.WriteFile` 改一个字节 → `edit_file` 带旧 `ifVersion` → 返回 `STALE_WRITE`，文件**字节未变**。
- 单测：`apply_patch` 一批 3 个文件，其中第 2 个版本过期 → 整批零写入（沿用现有原子语义）。
- 单测：没读过就 `edit_file` → `READ_REQUIRED`，错误里给"先 read_file"。
- 回归：现有 `apply_patch_test.go` / `builtin_test.go` 全部不改语义（新增字段是可选的，老调用路径读不到台账时放行）。

### P0-B｜写意图广播（让冲突在发生前可见）

写操作在 `plan` 阶段（真正落盘前）发一条事件：

```json
{ "event": "file.write.intent", "path": "src/lib/useBackend.ts", "agentId": "…", "agentName": "前端工程师", "at": 1795686300 }
```

- 网关已有事件总线（`internal/gateway/broker.go`），加一个事件名即可，SSE 与桌面两条宿主都自动收到。
- 前端在文件树/消息流里标"正在被 前端工程师 修改"，同一文件 60 秒内出现两个不同 agent → 在会话里插一条提示（不是弹窗）。
- 只做"告知"，不做"抢占"：同文件并发写在 P0-A 会以 `STALE_WRITE` 收场，不需要锁。

验收：两个会话改同一文件，第二个会话在动手前能在界面上看到提示；日志里可见两条 intent 事件带不同 agentId。

### P0-C｜默认隔离 + 一条命令合并回去

1. 新建会话选定 git 仓库时，**默认开 worktree**（`config.yaml` 加 `worktree.auto: true`，默认 true；非 git 目录静默跳过，不报错——不能因为不是仓库就拦住用户）。
2. 新增 `worktree_merge` 工具（Writes=true，走审批）：
   - 前置：工作树里工作区必须干净（否则先让它 commit 或 stash，工具不替它决定）。
   - 动作：`git -C <repo> rebase <branch>`？不——正确方向是把 `mad/<slug>` 合进主分支：先 `git -C <main> merge --ff-only <branch>`，失败则 `merge --no-ff`，仍冲突就**中止合并（`--abort`）**并把冲突文件列表交回给模型，由人/模型解决。
   - 合并后自动跑一次 `verify`（复用 `internal/tool/builtin/verify.go`），失败则**不自动回滚**（回滚会藏住证据），直接报告"合并成功但验证失败"。
3. UI：会话头部显示 `分支 mad/ui · 基于 151779f`，一个"合回主干"按钮。

验收：两个会话各改不同文件 → 都能 ff 合回；改同一文件 → 第二个 `merge --abort` 干净回到合并前，冲突列表准确；合并后 verify 失败时主分支状态不被悄悄改。

### P1｜文件树"他人在改 / 已过期"标记

- 数据源：`file.write.intent` + `read_file` 台账的 `at`。
- 呈现：文件树行尾一个灰点（他人在改）/ 黄点（你手上的版本已过期）。不做单独面板。

### P2（写进 backlog，不做）

- 跨仓库多根的统一租约；跨机器的写协调；冲突自动解决。

## 6. 与 Claude Code 的差异（回答"是不是一样"）

Claude Code 有：read-before-edit 的强制、编辑前校验文件未被外部修改、`/rewind`。它没有：把"并行 Agent"当默认场景、多份工作树自动管理、合并回主干的一等命令。

我们的差异点不在"再实现一遍 read-before-edit"，而在**并行是默认而不是例外**：默认隔离（P0-C）+ 冲突可见（P0-B）+ 写前校版本（P0-A）。这三件是它没做、而我们的产品形态（多 Agent 同仓协作）必须做的。

## 7. 成本与排期

| 项 | 成本 | 依赖 |
|---|---|---|
| P0-A 版本校验 + 台账 | 1.5d | 无 |
| P0-B 写意图事件 + UI 标记 | 0.8d | 网关事件名（已有） |
| P0-C 默认隔离 + `worktree_merge` | 2.0d | `worktree` 包（已有） |
| P1 文件树标记 | 0.5d | P0-B |
| 合计 | 4.8d（P0 回滚最小集 3.8d） | |

先做 P0-A：它是唯一能**单点消除今天四次事故里三次**的改动，且不改任何既有语义（字段可选）。

## 8. 风险

- **台账误报**：外部格式化（gofmt 保存即触发）会让版本变化，模型被迫重读。缓解：只在"锚点仍能唯一匹配"时才允许继续（说明内容语义未变），否则报 `STALE_WRITE`。
- **默认隔离的副作用**：用户会在 `.mad-worktrees/` 下看到一份"看起来像仓库"的东西，误以为改了两份。缓解：UI 明确标注"隔离工作树 · 分支 mad/xxx"，README 一条说明。
- **合并冲突把工作流卡住**：只在用户主动点"合回"时发生，不自动合。冲突就交回人。
