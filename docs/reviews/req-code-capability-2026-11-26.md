# 需求：把"会写"变成"写得对"——编码能力提升

状态：**P0 + P1-1/P1-3 已实现（2026-11-26 晚）**，P1-2 部分、P2 未做 ｜ 日期 2026-11-26 ｜ 预估 4.5 人天

## 1. 结论先行

现在的瓶颈**不是模型不会写代码**，是三个闭环没闭上：

1. **验证闭环**：`verify` 默认只跑 `go build` / `tsc`，不跑测试、不跑 lint；失败了靠 agent 自觉，没有"必须回到绿"的强制。
2. **定位闭环**：只有 `grep` / `glob`。大仓里"改这个函数会波及谁""该跑哪几个测试"全靠猜，token 花在翻文件上。
3. **自查闭环**：写完没有"回头读自己的 diff"这一步，也没有一次任务改了哪些文件、每步验证结果的持久记录。

对应到用户感受：**改得出来，但不敢信**。这跟"能不能写"已经不是一回事了。

## 2. 现状盘点（先承认已有什么）

| 能力 | 现状 | 位置 |
|---|---|---|
| 读写文件 | `read_file`（带行号、offset/limit、并发安全）、`write_file`、`edit_file`（唯一锚点） | `internal/tool/builtin/builtin.go:264/346/423` |
| 批量改 | `apply_patch`：原子批 + 唯一锚点 + 审批看真 diff | `internal/tool/builtin/apply_patch.go` |
| 搜索 | `glob`、`grep` | 同目录 |
| 验证 | `verify`：Go `go build ./...`；TS 读 `config.yaml` 的 `verify:`（本仓 = `npx tsc --noEmit`）；可用 `cmd` 覆盖为 test/lint | `internal/tool/builtin/verify.go` |
| 执行 | `bash`（超时杀进程组、输出限长、非零退出码当结果） | `builtin.go:39` |
| 质量治理 | 写操作审批、diff 可预览、`verify` 零审批（`Writes()=false`） | 已落地 |

这些是资产，不重写。下面每条需求都只补缺口，并且**不改既有工具的默认语义**（新字段可选）。

## 3. P0｜三件最便宜的（合计 2.6d，按性价比排序）

### P0-1｜`verify` 默认包含"相关测试"，失败即结构化（0.8d）

现状问题：默认只 build。类型过了、逻辑错了照样"验证通过"——这是最坏的一种通过。

改动：

- 命令装配从"build + tsc"扩成三段：`build` → `test`（仅当本轮改过 `_test.go` / `*.test.ts(x)` 或显式要求）→ `lint`（Go: `go vet ./...`；TS: 有 eslint 配置才跑）。
- **相关测试选择**（避免全量 `go test ./...` 慢到没人用）：
  - Go：改动的包路径直接 `go test ./internal/gateway/...`；若改的是被依赖包，用 `go list -deps -f '{{.ImportPath}}' ./...` 反查引用方（有缓存，一次 <1s）。
  - TS：改动文件 → 同名 `*.test.ts(x)` + 同目录 `__tests__`；找不到就跳过，不猜。
- 失败输出保持现有结构（状态/退出码/命令/耗时/前 20 行摘要），新增：`file:line:col` 抽取，前端可点。

验收：故意在 `internal/gateway/broker.go` 制造一个类型错误 → `verify` 返回 `go build` 失败 + 精确 `file:line`；改一个测试文件 → 输出里出现 `go test` 且只跑相关包；全绿时输出 `build ✓ test ✓ lint ✓` 三行，不是一句 "ok"。

### P0-2｜失败闭环：verify 红 → 自动回到修（0.5d）

现状：`verify` 的 Description 写着"唯一能证明完成的工具"，但**没有任何机制阻止它在红着的时候宣布完成**。

改动（在 loop 层，不动工具）：

- 一轮里调用过写工具、并且 `verify` 失败 → 系统在下一回合注入一条结构化提醒（不是自然语言说教）：`{"verify":"failed","cmd":"go build ./...","firstError":"broker.go:52: undefined: replayLimit"}`，并允许继续修。
- 连续 3 轮失败 → **停**，把三轮失败摘要一起交给人，附一句"已连续 3 次未通过，需要你决定方向"。防死循环烧钱，也防它靠改测试蒙混（见 P1-2）。

验收：制造失败 → 观察它自己重试并在 2 轮内变绿；人为让它改不动（比如删掉依赖）→ 第 3 轮停下，消息里能看到三次不同错误。

### P0-3｜编辑失败时给"现场"，不给"你猜"（0.2d）

现状：`apply_patch` 锚点找不到只说 `anchor (oldText) not found. Read the file again…`（`apply_patch.go:157`），模型得再花一次 `read_file` 才可能找到差在哪（差一个空格、差一行注释）。

改动：失败结果里附**磁盘上最相近的片段**——用锚点首行做模糊匹配（现有 `patmatch.go` 有匹配器），返回命中位置的 `±5 行` 真实内容（带行号），并标注"你的锚点第 N 行与这里不同：`-旧` / `+现`"（逐行 diff 的第一处差异）。

验收：把锚点里一个字符改错 → 错误信息里直接看到真实那几行 + 差异位置，模型一次就能改对（不需要额外 `read_file`）。

## 4. P1｜定位与自查（1.9d）

### P1-1｜`repo_map`：按需的符号级导航（1.0d）

不要向量索引、不要数据库（已被否决过）。只做一层便宜的结构摘要：

- 输入：目录或包路径 + 可选 `focus` 关键词。
- 输出：每个文件的**声明清单**（Go: `go/ast` 取 `func/type/const/var` 名 + 首行注释；TS: 正则取 `export function/const/class/interface` + 首行注释），按"最近被改 / 被 focus 命中"排序，硬上限 60 行/文件、总 200 行。
- 缓存：按文件 mtime，进程内 LRU。

验收：在 `internal/` 下调 `repo_map({path:"internal/gateway"})`，返回 ≤200 行、无重复、每个符号一行；连续两次调用第二次 <10ms（缓存命中）；对 200 个文件的目录不超时（上限截断而不是挂死）。

价值：模型从"grep 三次 + read 五个文件"变成"看一次地图 + 精准 read 两个文件"，token 通常省 5～10 倍——这也是 CLI 手感（速度）的直接来源。

### P1-2｜反作弊护栏：不许靠改测试变绿（0.4d）

现状：没有约束。模型完全可以把断言改松、把测试标记 skip 来过 verify。

改动：

- `verify` 结果里带 `changedTests`（本轮 diff 里的测试文件清单）。
- 若"测试从红变绿"且 `changedTests` 非空 → 在结果里标 `tests.modified`，并在会话里留一条可见记录（不是阻断，阻断会误伤合法的测试更新）。
- 真正的护栏放在审批：改测试文件的 `apply_patch` 走审批时，标题写"修改测试（可能是放宽断言）"，让人一眼看见。

验收：让 agent 改 `broker_test.go` 里的期望值 → 审批卡片标题里出现"修改测试"，`verify` 输出里有 `tests.modified` 标记。

### P1-3｜变更集自查（0.5d）

- 一次任务结束（或用户点"收尾"）时，把本次改动的文件清单 + 每个文件的 `+/-` 行数 + 最后一次 verify 结果拼成一段 `changeset`，附在最后一条回复里。
- 模型被要求"先读自己的 changeset 再说完成"（系统提示一行，不新增工具）。

验收：任务结束后消息末尾能看到"改了 4 个文件 +120/-18，verify ✓"；没跑 verify 时显示"未验证"，而不是假装通过。

## 5. P2（写进 backlog）

| 项 | 说明 | 成本 |
|---|---|---|
| gopls / tsserver 接入 | 定义跳转、引用查找、诊断（比 repo_map 强，但依赖外部进程与版本，先 `gopls` CLI 试探） | 2～3d |
| 覆盖率驱动 | 改完跑 `go test -cover`，新增代码未覆盖则提示补测 | 1d |
| 任务计划板 | 会话内 todo 持久化 + 每步 verify 状态（现在是模型自己在回复里列，会丢） | 1d |
| 回滚 | 工作树/变更集级回滚（`/rewind` 对应物）；依赖 P0-C 的 worktree | 1.5d |

## 6. 不做什么（避免又一次"大而全"）

- 不建全仓 AST 数据库、不做向量/语义索引（此前已否决，收益与维护成本不匹配）。
- 不把 `verify` 变成第二道审批墙（它是零审批的只读验证，这条设计不要破）。
- 不追求"自动修复一切"：闭环上限 3 轮，之后必须交人。
- 不新增第 4 套界面：`repo_map`、`changeset` 都走现有消息流。

## 7. 度量（做完怎么判断有用）

用 `docs/reviews/benchmark-cli-speed-2026-11-26.md` 的基线，加四个指标：

1. 一次任务平均 `verify` 次数（期望 1.2～1.8，太低说明没验，太高说明在瞎试）。
2. 首次 verify 通过率。
3. 平均 token / 任务（P1-1 落地后应下降 ≥30%）。
4. "改测试变绿"的发生次数（应可被看见，而不是为零——为零说明护栏在造假）。

## 8. 与 P0 写安全的关系

两份需求共用一层底座：**文件版本台账**（`req-write-safety-2026-11-26.md` 的 P0-A）。

- 写安全用它防止覆盖别人；
- 编码能力用它做 `repo_map` 缓存失效、`changeset` 的"哪些文件真变了"、`verify` 的"哪些测试受影响"。

所以顺序建议：先做写安全的 P0-A（1.5d），再并行推本需求的 P0-1/P0-2/P0-3（1.5d）。两边都不改既有语义，可以同时开工。

## 9. 落地清单（2026-11-26 晚）

| 提案项 | 落点 | 测试 |
|---|---|---|
| P0-1 verify 扩维 | `internal/tool/builtin/verify_scope.go`：改动包 → `go test <改动包>`（≤8 个包）；`package.json` 有 `lint` 脚本才跑 `npm run lint`；报告新增 `changedTests` | `verify_scope_test.go` 6 条（包去重/截断、测试文件识别、lint 检测、真仓库取改动） |
| P0-2 失败闭环 | `internal/loop/loop.go`：verify 失败 → 把失败摘要以系统消息写回上下文；连续 3 次 → `PhaseFailed` 停下；绿一次即清零；"同参重复被拦"不算验证失败 | `verify_loop_test.go` 3 条 |
| P0-3 编辑失败给现场 | `excerpt.go`（同写安全需求共用） | 见 `filestate_test.go` |
| P1-1 repo_map | `internal/tool/builtin/repo_map.go`：Go AST + TS 行级，跳过 `node_modules`/工作树，focus 排序，行数硬上限 | `repo_map_test.go` 3 条（声明覆盖、focus 优先、40 行预算） |
| P1-3 changeset | `internal/tool/builtin/changeset.go`：`git status` + `numstat`（工作区与暂存）+ 未跟踪标记 + 测试文件提醒 | `changeset_test.go` 2 条 |

### 修正提案里的两处误判（实测后改）

1. **verify 不是"只跑 build"**：原代码已有 `go build` + `go vet`（`goVerifySteps()`），提案低估了现状。本次在其后追加"相关测试"，`ts` 侧追加 lint。
2. **`apply_patch` 已是原子批**：不需要重建，只在 `plan` 阶段插入版本校验，保留"任一条失败整批零写入"。

### 未做（明确留白）

- P1-2 的"审批标题标出『修改测试』"：`changedTests` 已经在 verify 报告和 changeset 里可见，
  但审批卡标题还没接（审批标题在 `Preview` 生成，需与审批中心一起改）。
- P1-3 的"任务结束自动附 changeset"：现在是模型主动调 `changeset`，没有在收尾时强制注入。
- P2 全部（gopls/tsserver、覆盖率驱动、任务计划板、回滚）。
