# island-trigger 决定

> 落 `specs/island-trigger/decision.md`。规则在 `references/scope.md`。
> L1 用几句填完。

- 级别：L1
- 原话（一字不改）：对于现有的 这个 灵动岛的 触发我觉得 还是需要进行优化一下 现在的 有点太挫 进行梳理一下吧 使用合适的 skill 进行实现
- 我理解你要的是：灵动岛现在「点不动、右键就杀、拖和点抢同一块地」这套触发机制要重做。不是改视觉，是把「看一眼 / 选中 / 跳转 / 停止」这四个意图拆开，各自给一个确定能触发的入口；拖动作为窗口能力单独留一条通道，不再和内容抢事件。
- 可能理解错的地方：
  - 「触发」我理解为**用户输入如何被灵动岛接收并转成动作**（点击、双击、右键、拖动、hover）。如果你指的是「灵动岛何时自动弹出/出现」，那是另一件事（窗口显隐策略，`main.go:720 ToggleMiniWindow`），这次不做。
  - 「太挫」我落成四个可证的具体故障（点击失效、右键即杀、拖动即双击、tooltip 被裁）。如果你还指视觉质感（玻璃、圆角、配色），那属于表现层，这次只做到「不裂」不改设计语言。
- 本质：`mini.tsx:154` 把 `--app-region: drag` 写在根节点，而 CSS 自定义属性会继承，于是整条岛（含 8 个状态点）都是拖动句柄；mygo 的 `mousedown` 拦截器（`packages/bridge/src/bridge.ts:187`）只豁免 `INTERACTIVE` 白名单里的标签，状态点用的是 `<div>` 不在名单内，`preventDefault()` 直接把点击吃掉。**用户看见的是一个能 hover、能拖动、但点哪都没反应的条。**
- 怎样算满足：
  1. 命令：`npx tsc --noEmit`
     点名用例：无（纯类型门）
     期望输出：exit 0，无 error TS 行
     覆盖：验收条件 1（触发层改动不破坏类型契约）
  2. 命令：`grep -c "app-region': 'drag'" src/mini.tsx`
     点名用例：拖动手柄唯一化
     期望输出：`1`（只有 DragHandle 一个元素持有 drag），且该元素不含任何 onClick
     覆盖：验收条件 2
  3. 命令：`grep -n "onDoubleClick\|onContextMenu" src/mini.tsx`
     点名用例：意图拆分
     期望输出：`onDoubleClick` 与 `onContextMenu` 均**只**绑定到 `handleOpen` / `handleContextMenu`（不得再出现 `handleAgentClick`）；`handleAgentClick` 在全文零命中
     覆盖：验收条件 3
  4. 命令：`grep -n "stopAgent" src/mini.tsx`
     点名用例：危险动作需二次确认
     期望输出：命中行所在函数体内必须先有 `setPendingStop` 或等价确认态，不得直接调用
     覆盖：验收条件 4
  5. 命令：`cd cmd/data-service 2>/dev/null; go vet ./... && go test ./...`
     点名用例：`TestAttachNoteToAgent` / `TestSendToChannelPartialFailureKeepsOthers`
     期望输出：全 `ok`，无 `FAIL`
     覆盖：验收条件 5（后端零改动，回归不倒退）
  6. 人工：真机（`npm run dev`）点一次状态点、拖一次岛、右键一次
     点名用例：手动触达
     期望输出：点击后岛不消失且该点高亮；拖动只移动窗口；右键弹出菜单而非杀进程
     覆盖：验收条件 6
- 未知：
  - `DrawHandle` 之外的空白区要不要也能拖？我按「整条岛可拖，但内容层 `no-drag`」做，这样既保留拖动又不吞事件。若你希望「只有左侧留一条细手柄可拖」，会影响布局，需要拍板。
  - 选中态要不要持久（切回主窗再缩小，之前选中的还在）？我按「不持久，收起即清」做。
- 这次不做：
  - 不改视觉设计语言（玻璃、圆角、配色、品牌字「舰队」的位置）
  - 不改窗口显隐策略（`ToggleMiniWindow` 互斥模型，属于通知方案的 P0）
  - 不改 `FitContent` / `StateKey` / `data-theme`（尺寸与主题，属于另一批）
  - 不碰后端 `internal/agent` 与 `main.go` 的 Sidecar 方法签名
  - 不做通知分流
- 现有能力：
  - `src/mini.tsx:154` 根节点 `--app-region: drag` —— 已证实
  - `src/mini.tsx:193-195` `onClick`/`onDoubleClick`/`onContextMenu` 三绑定 —— 已证实
  - `src/mini.tsx:139-143` `handleContextMenu` 直接 `Sidecar.stopAgent` —— 已证实
  - `src/mini.tsx:159-160` `handleAgentClick` 调 `Sidecar.focusAgent` —— 已证实
  - `main.go:734-746` `FocusAgent` 内 `miniWindowRef.Hide()` —— 已证实
  - `packages/bridge/src/bridge.ts:14-15` `INTERACTIVE` 白名单不含 `div` —— 已证实
  - `packages/bridge/src/bridge.ts:107-117` `appRegion` 用 `getComputedStyle` 读继承值 —— 已证实
  - `packages/bridge/src/bridge.ts:185-192` `mousedown` 捕获阶段 `preventDefault` —— 已证实
  - 无出处的词：无
- 旧决定：`specs/dynamic-island/decision.md`。实现是否仍符合：**不符合**（该决定的「单击=跳转+隐藏」正是本次要拆掉的行为）
- 最小充分方案：
  1. **抽出 `<DragHandle>`**：只承载 `--app-region: drag`，绝对定位铺满岛，`z-index` 低于内容层，**无任何 onClick**。
  2. **内容层显式 `no-drag`**：状态点、按钮一律用 `<button>`（进 INTERACTIVE 白名单），或所在容器写 `--app-region: no-drag`。
  3. **意图四拆**：
     - `hover` → tooltip（就地预览，不改状态）
     - `单击` → `setSelectedAgent(id)` + 就地高亮，**不隐藏岛**
     - `双击`（或 selected 后点「打开」）→ `focusAgent` 跳主窗
     - `右键` → 弹出小菜单（停止 / 打开），**停止需二次确认**
  4. **`--app-region` 收窄**：根节点去掉 `drag`，改由 DragHandle 独占。
- 为什么更小的做法不够：
  - 只把状态点换成 `<button>`：`appRegion` 读的是 `getComputedStyle(el)` 的继承值，按钮若在 `drag` 容器内且自身未写 `no-drag`，仍读到 `drag`；但 `closest(INTERACTIVE)` 会豁免它 —— 所以单换标签**确实能让点击生效**。但这留下「拖动区覆盖内容」的结构问题：tooltip、展开面板、未来的就地操作卡片都要各自再写一次 `no-drag`，每忘一处就是一个新 bug。抽 DragHandle 是让这件事**只做对一次**。
  - 只删 `onDoubleClick`：解决「拖动变成双击跳转」，但点击仍然吞、右键仍然杀。
- 相关的债：B 局部偿还（触发层）。归入「这次不做」的有：C 暂缓（`FitContent`/`StateKey`/`data-theme`）、C 暂缓（通知分流）、C 暂缓（`blocked` 就地处理入口）。
- 风险：不碰契约、持久化、权限、状态、回滚。纯前端交互层。
- 待拍板：无（两处「未知」已写明假设，且都不改变方案骨架；若你要改成「左侧细手柄」需另说）
