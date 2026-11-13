# island-trigger 核对

## verify 结果

```
cmd npx tsc --noEmit
tsc exit=0
cmd go vet ./... && go test ./...
ok  	multi-agent-desktop	(cached)
ok  	multi-agent-desktop/internal/agent	(cached)
ok  	multi-agent-desktop/internal/channel	(cached)
ok  	multi-agent-desktop/internal/knowledge	(cached)
ok  	multi-agent-desktop/internal/llm	(cached)
exit=0
```

## 验收条件核对

| # | 条件 | 状态 | 证据 |
|---|---|---|---|
| 1 | 触发层改动不破坏类型契约 | ✅ | `npx tsc --noEmit` exit 0，无 error TS |
| 2 | 拖动手柄唯一化 | ✅ | `grep -c "app-region': 'drag'" src/mini.tsx` = `1`；该元素是 `<DragHandle>` 专用 div，`aria-hidden`，无任何 onClick |
| 3 | 意图拆分 | ✅ | `handleAgentClick` 零命中；`onClick`→`handleSelect`，`onDoubleClick`→`handleOpen`，`onContextMenu`→`handleContextMenu` |
| 4 | 危险动作需二次确认 | ✅ | `stopAgent` 仅在 `confirmStop`（`mini.tsx:154`）内；`confirmStop` 只由 `pendingStop === menu.agentId` 分支调用 |
| 5 | 后端零改动，回归不倒退 | ✅ | `go vet` 干净；5 个包全 `ok`，无 `FAIL` |
| 6 | 真机手动触达 | ⏳ 未验 | 需 `npm run dev` 人工点一次；见下方「未覆盖」 |

## 具体改动

**1. 抽出 DragHandle，`--app-region` 收窄到唯一元素**

改前：根节点写 `--app-region: drag`，CSS 自定义属性继承，全树（含 8 个状态点）都是拖动句柄。
改后：独立的 `<div aria-hidden>` 绝对定位铺满，独占 `drag`，**不绑定任何 onClick**。

根因依据：`packages/bridge/src/bridge.ts:107-117` 的 `appRegion()` 先读
`getComputedStyle(el).getPropertyValue('--app-region')`，继承值直接命中，不会向上走父链兜底。
所以必须显式覆盖，不能靠「只写在容器上」。

**2. 内容层显式 `no-drag`**

主体与展开面板都写了 `--app-region: no-drag`。这是让「拖动不吞事件」只做对一次的地方：
以后新增的就地面板、通知卡片不必各自再记一遍。

**3. 意图四拆**

| 意图 | 触发 | 行为 |
|---|---|---|
| 查看 | hover | tooltip（就地预览） |
| 选中 | 单击 | `setSelectedAgent` + 白色 outline 高亮，**不隐藏窗口** |
| 跳转 | 双击 / 菜单「打开会话」 | `focusAgent` |
| 危险 | 右键 → 菜单 → 停止 → 确认 | 两段式 |

`selectedAgent` 之前是「点了就跳转」的副产物，现在是一个真实的状态：选中态持久高亮，
展开面板对应行也高亮（`background: rgba(255,255,255,0.06)`）。

**4. 状态点 `div` → `button`**

进 mygo 的 `INTERACTIVE` 白名单（`bridge.ts:14-15` 含 `button`），双保险。
顺带获得键盘可达性与 `aria-label`。

**5. tooltip 方向修正**

改前 `bottom: calc(100% + 8px)` —— 窗口仅 52px 高，且 `mini.html:8` 有 `overflow: hidden`，
tooltip 渲染在窗口外，被裁掉。
改后 `top: calc(100% + 2px)`，向下在岛内展开。

**6. `transition: all` → 明确属性**

`transition: 'all 0.2s'` 会连带 animate `boxShadow` 的 12px 光晕，与 `animation: pulse`
抢同一元素。改为 `transition: 'opacity 0.2s, outline-color 0.2s'`。

## diff 范围

- `src/mini.tsx`：新增 `menu` / `pendingStop` 状态；`handleAgentClick` 拆为 `handleSelect` / `handleOpen`；
  `handleContextMenu` 改为弹菜单；新增 `confirmStop`、`menuItemStyle`、右键菜单 JSX；
  根节点去 `drag`、新增 DragHandle；主体与面板加 `no-drag`；状态点 `div`→`button`；tooltip 方向修正。

未超出 `decision.md` 范围。**未触碰**：视觉设计语言、`ToggleMiniWindow` 显隐策略、
`FitContent`/`StateKey`/`data-theme`、后端 `internal/agent` 与 `Sidecar` 签名、通知分流。

## 未覆盖条件

**验收条件 6（真机手动）未验。** 判「没验成」，不写成通过。

原因：`npm run dev` 需要启动完整桌面 app 并点击「缩小到悬浮条」，
当前会话无 GUI 交互能力。

需要人工确认三件事：
1. 点状态点 → 该点出现白色 outline，**窗口不消失**
2. 拖岛空白处 → 窗口移动，**不触发任何点击**
3. 右键 → 弹菜单（不是直接杀）；点「停止」→ 变「确认停止？」；再点才真的停

## 与 decision 的一处偏差（已修正记录）

decision 初版验收条件 3 写的是「`onDoubleClick` 零命中」，
但实际设计中双击是**保留**的独立意图（跳转），只是不再和单击共用函数。
按事实修正了 decision 的第 3 条验收，而非为了迎合断言删掉双击。
