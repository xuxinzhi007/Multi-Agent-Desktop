# 需求：上下文预算与工具输出防护（参考 gp / gp-context-lab）

状态：**提案，待开发** ｜ 日期 2026-11-26 ｜ P0 预估 3 人天 ｜ 来源：`npc1607/gp`（主干）、`npc1607/gp-context-lab`（实验场）

## 1. 为什么现在做

当前会话有两个真实痛点，都不是"再加功能"能解决的，而是**上下文里塞了什么、怎么塞**的问题：

1. **工具输出不可恢复。** `bash` 跑一次编译日志 3 万字节，`Registry.truncate` 裁成头尾各半、中间插一行 `[N bytes omitted; output truncated]`，**原文直接丢弃**（`internal/tool/tool.go:373`）。模型后面想回看那段报错，只能重新跑一遍命令——慢、且可能已经不复现。
2. **长会话越跑越贵。** `internal/loop/loop.go:952` 的 `compact` 有预算、也有摘要冻结（`shouldRecomputeSummary`），但它是**摘要型**：早期工具输出一路原样带进上下文，直到被压成一段自然语言。压缩会漂移、冲突无法仲裁、也回查不到原文——这正是 `gp-context-lab` 诊断的三个矛盾里的前两个。

`gp` 已经用 **Observation Masking** 解决了第一层（长输出折叠 + 按需还原），`gp-context-lab` 在实验第二层（结构化账本 + 工作集 + 缓存友好布局）。本需求只取**被实测验证过、且与现有架构不冲突的部分**，不整体照搬。

## 2. 现状核对（先确认不重复造）

| 能力 | 现状 | 结论 |
|---|---|---|
| 工具输出截断 | `truncate` 头尾各半、原文丢弃（`tool.go:373`） | **要改**：截断前先落盘，留 `callId` 指针 |
| 结果元信息 | `Meta{DurationMS,BytesRaw,BytesSent,Truncated,…}`（`tool.go:33`），无 callId、无原文路径 | **要扩** |
| 上下文压缩 | `llm.CompactWith` + 摘要冻结（`loop.go:952`、`agent/summarize.go`） | **保留**，mask 是它的前置层，不替代 |
| 断点续跑 | `loop.FileStore.Save/Load`（`loop/store.go:39/77`）+ `resumable(prev)` 恢复（`agent/loop_backend.go:268`） | **已够**，本需求不动 |
| 子会话工具白名单 | 只有两档：`readOnlyRegistry`（只读）与全量（`delegate.go:452`） | **不在本期**（gp 的五角色裁剪是另一件事） |
| 缓存命中数据 | `llm/usage.go:58-93` 已解析 `cached_tokens` / `cache_read_input_tokens` | **可直接用来验收**，不必新造 |
| MCP | 无客户端；`acp` 只把 `mcpServers: []` 透传 | **要新建** |
| 并发 Governor | `internal/scheduler` 的令牌桶只管定时任务，不管 agent 并发 | **不在本期** |

## 3. P0（本期只做这两项）

### P0-1 工具输出防护：折叠 + 按需还原（1 人天）

**规则**（照 gp 的实测参数，不重新调参）：

- 只看**工具结果**，不动 user / assistant 消息。
- 最近 **5 个工具轮**保留原文；更早的才允许折叠。
- 折叠门槛：`len > 400 字节` 或 `行数 > 8`。小于这个的不折——小输出折了反而丢信息。
- 折叠不是删，换成带指针的占位符：

```
[已折叠工具输出: tool=bash call_id=call_8f2a | 原文 2140 行 / 312KB;
 需要原文时调用 restore_observation(call_id="call_8f2a")]
```

**落盘**：`truncate` 判定超限时，先把完整原文写到会话数据目录 `observations/<runID>/<callID>.txt`，再裁。这样 `restore_observation` 能原样取回，且重启后仍有效。

**新工具** `restore_observation(call_id, offset?, limit?)`：只读、零审批、返回原文（默认前 200 行，gp 的 `defaultRestoreMaxLines`）。找不到时明确说"该调用没有留存原文（可能是小输出或已过期）"，不要返回空。

**接在哪**：`loop` 组装请求前（`compact` 之前）对 `st.Messages` 做一次纯函数变换；**变换结果不回写 `st.Messages`**，checkpoint 里永远存原文。这一点是硬约束——否则折叠后的内容会被当成历史落盘，`restore` 就变成不可逆的丢失。

**验收**：

1. 同一条 30KB 编译日志，第 8 个工具轮后该轮内容被折叠，`EstMessages` 比折叠前下降 ≥ 60%。
2. `restore_observation` 取回的字节数与落盘文件一致；跨重启仍可读。
3. 最近 5 轮内的输出**一字不改**（断言原字符串）。
4. 折叠后的前缀稳定性：用 `usage.cached_tokens` 对比连续两轮请求，未新增工具轮时应命中缓存（见 P1-2 的度量方式）。

### P0-2 MCP stdio 客户端（2 人天）

**为什么**：`tools/feishu-mcp` 已有 11 个文档工具（含原地改块 `feishu_update_block` / 保格式替换 `feishu_patch_block`），本应用模型**一个飞书工具都没有**，写好的报告只能人工贴。同一套客户端还能接 `qoder-mcp`、`comfyui-mcp`、`grill-mcp`。

**照抄 gp 的结构**（`internal/mcp`：`NewStdioClient` → `Initialize` → `ListTools` → `CallTool` → `Notify` → `Close`），落到 `internal/mcp/`：

- `config.yaml` 增加 `mcp.servers[]`：`{name, command, args, env, dir, timeoutMs}`。
- 启动时对每个 server 握手 + `tools/list`，注册为 `mcp__<server>__<tool>`（前缀必须有，否则和内置 `read_file`/`grep` 撞名）。
- **硬约束**（不做就会出事故）：
  - 外部工具一律 `Writes() = true`，走审批；`ConcurrencySafe() = false`。
  - **不参与 `filestate` 版本校验**：`ifVersion` 只对内置写工具生效，外部工具描述里必须写明"可能覆盖他人改动，建议在 worktree 内使用"。
  - 单次调用硬超时（默认 60s，可配），超时杀子进程；输出走 `truncate`（复用 P0-1 的落盘）。
  - server 退出/崩溃不得阻塞 loop：失败返回明确错误，不重试超过 1 次。
  - 工具结果不写进系统提示，只当工具结果。

**验收**：真实跑一次 `mcp__feishu__feishu_read_document` 读一篇文档、`feishu_patch_block` 改一段（在测试文档上），故意 kill server 验证超时与错误文案。

## 4. P1（列清楚，不排期）

1. **Epoch 对齐的折叠边界（0.5d）**：折叠边界按 16 个工具轮对齐、epoch 内冻结（gp 的 `DefaultObservationMaskEpoch=16`）。目的是别每轮都推进边界改写前缀，否则 prefix cache 全废。**没有 P0 的折叠层，这条没有意义，所以排后面。**
2. **ContextLedger 最小版（1.5d）**：`active / superseded / resolved` 三态 + `provenance` 指针，冲突用 supersede 链仲裁，渲染只输出活跃约束 + 最小注脚。**必须由 LLM 或工具调用维护，禁止正则关键词提取**——lab 的实测里，启发式抽取在无显式触发词的口语化用例上过时污染率从 0% 飙到 41.7%。
3. **缓存命中看板（0.5d）**：把 `usage` 里的 `cached_tokens` 按会话累计，界面显示"本轮命中 X%"。纯展示，不改策略。

## 5. 明确不做

- **PPR / 热核扩散 / ACT-R / MMR / k-DPP / 五段拼装**：lab 自己的结论是"先合最小高确定性 PR"，整套算法还在实验阶段，且启发式抽取不达标。现在抄等于把未验证的复杂度搬进主干。
- **Bubbletea TUI**：已有 React 界面，不重复。
- **Goja JS 扩展、Governor 并发治理、SQLite 替换现有存储**：与本期目标无关。
- **把 `compact` 改成账本**：摘要冻结继续用，账本是叠加层，不是替换。

## 6. 工期与顺序

P0-1（1d）→ P0-2（2d）。两项都完成后，再评估 P1-1 与 P1-2。

理由：P0-1 改的是"上下文里塞什么"，立刻省钱、立刻让模型能回看；P0-2 解的是"模型没有飞书文档能力"这个明确缺口。两者都不新增包、不改架构，符合"两周内只修体验、不铺范围"的收敛原则。

## 7. 风险

| 风险 | 处理 |
|---|---|
| 折叠掉关键信息 | 400 字节/8 行门槛 + 最近 5 轮原文 + `restore` 兜底；折叠占位符里写明行数与大小 |
| 折叠后模型不会主动 restore | 占位符直接给出可复制的调用示例（gp 已验证这个写法有效） |
| 落盘原文占空间 | 单次 >1MB 的只留头尾各 500KB；按 runID 分目录，会话删除时一并清理 |
| MCP 外部工具绕过写安全 | 默认 Writes + 审批 + 描述声明；不进 `filestate` 台账，文档里写清边界 |
| 折叠与 checkpoint 冲突 | 折叠只在组装请求时做，永不回写 `st.Messages` |
