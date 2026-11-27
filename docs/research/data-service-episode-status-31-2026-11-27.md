# data-service「任务一直显示执行中」根因分析

日期：2026-11-27
数据来源：`~/Downloads/logs-default-1795768572098.txt`（devops 日志导出，14 行）
代码依据：`/Users/xuxinzhi/Documents/gowork/data-service`

## 一、日志实测

- 命中列表接口 `ListJobEpisodesForQC` 的对数共 14 行 = **7 次调用**（每次 audit + access 各一行），每次 `total = 53`、`pageSize = 50`。
- 7 次调用耗时区间 **63 ms ~ 130 ms**，全部亚秒级 → **不是慢查询**。
- 去重后 50 条 episode 的状态分布：
  - `status = 31`（预处理中）：**47 条**
  - `status = 91`（已中止）：**2 条**
  - `status = 35`（待质检）：**1 条**
- 47 条 `status = 31` 的 `data_parsed_status` **全部为 12（待切片）**。
- 时间跨度起始于 `2026-11-27T16:58`，`updated_at` 在此之前就已停止推进。

## 二、业务规则（带行号）

状态机（`internal/biz/status.go:340-362`）：12 待切片 / 31 预处理中 / 35 待质检 / 90 失败 / 91 已中止。

1. **31 → 35 只看前置质检（pre_qc）结果**
   `internal/biz/qc.go:3280-3325`：查询为空、`pending`、`running`、超时，四条分支都保持当前状态 31。
2. **门禁允许的 `data_parsed_status` 集合包含 12**
   `internal/biz/qc.go:3704`：所以「切片没做」不是卡 31 的直接原因，但它堵住了下一段流程——pre_qc 回来后进入 35 之后必须有切片数据。
3. **切片流水线被全局暂停**
   `configs/config.data.yaml` 中 `kafka.parsed_result.paused: true`。
4. **卡在状态 12 没有任何补偿路径**（本次最关键发现）
   - `internal/biz/parsed_slice_queue.go:68-78`：`RequeueStalePendingSlices` 已改为 no-op，返回 `Skipped: true`，消息明示「不再处理历史状态 12」。
   - `ParsedSliceStaleScanInterval` 与 `staleScanLoop` 在全仓 `internal/`、`cmd/` 中**只出现在注释里，没有定义与启动点** → 注释里描述的「定时对账扫描」实际不存在。
   - `internal/data/episode.go:2357` 的 `FindStalePendingSliceItems` 仍在，但没有任何 worker 调用它。
5. **预处理轮询本身既不漏也不高频**
   `internal/biz/preprocess_poll.go:22-26`：scan 间隔 1 分钟、单批 2000、poll tick 200 ms、tick 批 100；`:158-163` 不足一页时回绕到起点，不会漏扫。

## 三、展示口径：为什么看起来是「执行中」

- `internal/biz/status.go:720-724`：`EpisodeStatusAbort`(91) 被映射成 `EpisodeDisplayStatusFailed` → 「已中止」在界面上显示为「失败」，业务语义被折叠。
- `status = 31` → display 值为 `PREPROCESSING`；前端把它统一渲染为「执行中」，于是真实语义「等待前置质检结果」被吞掉，表现为任务无限期执行中。

## 四、结论

1. **不是 data-service 查询慢、也不是漏扫**：接口 63–130 ms，扫描定时器在跑。
2. **47 条长期停在 31**：前置质检（pre_qc）回调没有回来，31 保持不变是该门禁的设计行为；同时切片流水线全局暂停、状态 12 已无自动或手动补偿路径，两条路都堵。
3. **2 条 91** 是「已中止」，被展示层折叠成「失败」，与「执行中」属同一类展示折叠问题。
4. 系统缺少两个兜底：pre_qc 等待超时后的状态迁移、展示层对 31/91 的语义区分。

## 五、建议

运维侧
- 确认 pre_qc 侧是否还对该 job 投递结果；若上游已停，需要人工重置这批 episode 的状态。
- 恢复切片需同时满足：`kafka.parsed_result.paused: false` + 调用 requeue 接口；注意该接口**不处理历史 12**，恢复后仍需按 episode 重导或重置数据。

代码侧
- pre_qc 等待加显式超时，超时迁移到 90 并写入 errorMessage，而不是无限保持 31。
- 为状态 12 补一个真实运行的定时对账 worker（`FindStalePendingSliceItems` 已有，缺调用方）。
- 展示层把 91 拆出独立的「已中止」文案，不要复用 FAILED。
- 前端把 `PREPROCESSING` 渲染为「等待质检 / 预处理中」，不要与「执行中」共用文案。

## 六、复查方式（不编造表名，走现有接口）

1. 再次调用 `ListJobEpisodesForQC`（pageSize=50），统计 `status=31` 的条数是否仍为 47、`updatedAt` 是否发生变化。
2. 若 47 与 `updatedAt` 均无变化超过一小时，即可判定为 pre_qc 回调缺失，直接进入运维侧处理。
3. 对那 2 条 91：确认前端文案当前显示「失败」，即印证展示折叠问题。
