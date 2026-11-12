# 研究：前端动画与状态过渡的工程实现（Electron / WebView 悬浮窗场景）

> 调研日期：2025-11-12
> 范围：`src/mini.tsx`（灵动岛悬浮窗）、`src/components/ActivityFeed.tsx`、`src/lib/useSidecar.ts`（高频事件源）
> 资料：Motion 官方文档（context7 拉取 `motion.dev/docs/*` 原文） + 社区/浏览器规范经验
>
> **置信度标注**
> - ✅ **官方文档确认** —— 引用了 `motion.dev/docs/xxx` 的具体页面
> - 🌐 **平台规范** —— CSS / HTML / W3C 规范行为，可自行验证
> - 🔶 **社区经验** —— 无一手文档背书，来自实践总结，需自行 benchmark
>
> 本文只给关键片段与 API 名，**不是**可直接粘贴的完整实现。

---

## 0. 先对齐现状：这个项目里的三个真实痛点

读代码得到的约束，后面的选型都受它们影响：

| 位置 | 现状 | 动画层面意味着什么 |
|---|---|---|
| `src/lib/useSidecar.ts:61` | 单一 `useState<State>` 大对象，任何事件都 `setState(s => ({...s, ...}))` | **全树重渲染**。一整条 `agents` 数组换新引用，所有 agent 卡片重算。这是最大的性能债，比动画本身更贵。 |
| `src/lib/useSidecar.ts:133-140` | `agentMessage` 事件把 `[...a.messages, message]` 追加进数组 | 长会话下数组复制 O(n)，且一条 assistant 消息就触发整棵树。 |
| `src/mygo.ts:342` | `agentDelta`（流式 token 增量）事件存在 | **每秒可达几十次**。`mini.tsx:51-57` 只在 ref 里累积、不进 state —— 这个做法是对的，不要改坏。 |
| `mini.tsx:79-82` | `useEffect` → `Sidecar.resizeMiniWindow(height)`，高度靠 layout 计算 | 窗口尺寸变化是 **Go 侧 IPC**，比 CSS 慢一个数量级。见 `floating-window-review.html` 的 `FitContent` 提案。 |
| `mini.tsx:281-292` | `animation: pulse 1s infinite`（无限 CSS 动画） | 无限动画在隐藏/后台窗口仍占用合成线程；多实例叠加时是"卡"的常见来源。 |
| `mini.tsx:163-166` | `transition: 'all 0.2s'` | `transition: all` 是反模式，见 §4.1。 |

> 🔶 **结论先行**：这个项目此刻缺的不是"更炫的动画库"，而是 **① 事件→渲染的最小粒度订阅**，**② `transition: all` / 无限 pulse 的收敛**。先做 §4，再谈 §1/§2。

---

## 1. 列表项状态变化（新增 / 离开 / 状态流转）

### 1.1 四个层次，先分清用途

| 手段 | 解决什么 | 何时用 |
|---|---|---|
| CSS `transition` | 单属性、状态 A→B | 颜色、透明度、单值跳变 |
| CSS `@keyframes` | 自循环 / 一次性进入 | pulse、shimmer |
| **FLIP** | **元素位置**在 DOM 顺序变化后从旧位置平滑到新位置 | 列表排序、增删导致的重排 |
| Motion `layout` prop | 自动做 FLIP（内部用 `animateLayout` + transform） | 同上，但不用手算 |
| Motion `AnimatePresence` | 解决"React 卸载瞬间元素就没了，来不及播退出动画" | **删除 / 离开** —— CSS 无论如何做不到（除非延迟卸载） |
| Spring 物理 | 过渡曲线手感 | 拖动结束、展开收起、尺寸变化 |

✅ 官方文档（`react-layout-animations`）：`layout` prop 就是 FLIP——
> Add the `layout` prop to a motion component to animate layout changes automatically.

✅ 官方文档（`react-animate-presence`）：
> Use the `exit` prop to define an exit animation, which will fire when the component is removed from the React tree.

### 1.2 `AnimatePresence` 的核心 API 与语义

```tsx
import { motion, AnimatePresence } from "motion/react"   // ← 注意包名，见 §1.5

<AnimatePresence initial={false}>
  {agents.map(a => (
    <motion.li
      key={a.id}                                    // ✅ 必须是稳定业务 id
      layout
      initial={{ opacity: 0, scale: 0.9 }}
      animate={{ opacity: 1, scale: 1 }}
      exit={{ opacity: 0, scale: 0.9 }}
      transition={{ type: "spring", visualDuration: 0.25, bounce: 0.2 }}
    />
  ))}
</AnimatePresence>
```

| 参数 | 官方语义（✅ `react-animate-presence`） | 本项目建议 |
|---|---|---|
| `key` | 每个直接子节点必须有 key，且**跨 render 稳定** | 用 `a.id`，**绝不用 index**。官方明示：用 index 会让 exit 动画错位。 |
| `initial={false}` | 首次挂载时禁用 children 的初始动画 | **必开**。否则悬浮窗一打开，8 个状态点会同时"弹"一下，很吵。 |
| `mode` | `"sync"`（默认）/ `"wait"`（等 exit 完再 mount）/ `"popLayout"`（退出元素脱离布局流，周围立即 reflow） | 列表增删用 **`popLayout`**；单内容切换（如 tooltip 换 agent）用 `wait` |
| `propagate` | 默认 `false`；设 `true` 则父级 exit 时子 `AnimatePresence` 的 exit 也触发 | 展开面板里再套列表时需要 |
| `onExitComplete` | 所有 exit 结束回调 | 用于 `resizeMiniWindow` 的**延迟收尾**（见 §2.4 坑） |
| `custom` + `usePresenceData` | 元素被移除后 props 不再更新，故用 `custom` 传值给 exit 变体 | 需要"按离开方向决定退出动画"时用 |

`mode="popLayout"` 的两个强制条件（✅ 官方明确列出）：

```tsx
// 1) 必须给直接子组件 forwardRef，并把 ref 透到 DOM 节点
// 2) 动画父容器不能是 position: static
<motion.ul layout style={{ position: "relative" }}>
  <AnimatePresence mode="popLayout">
    {agents.map(a => <AgentDot key={a.id} {...a} />)}
  </AnimatePresence>
</motion.ul>
```

### 1.3 已踩坑点（混合 exit + layout 时最容易翻车的地方）

1. ✅ **exit 与 layout 混用要包 `LayoutGroup`**（`react-animate-presence` 原文）：
   > When mixing exit and layout animations, wrap the group in LayoutGroup so components outside AnimatePresence know when to perform a layout animation.

   ```tsx
   <LayoutGroup>
     <motion.ul layout>
       <AnimatePresence>{items.map(...)}</AnimatePresence>
     </motion.ul>
   </LayoutGroup>
   ```
   不包的话，兄弟元素会"跳"一下才动。

2. ✅ **`borderRadius` / `boxShadow` 的形变修正只在 `style` 上生效**。
   `layout` 用 `scale` 实现形变，圆角会被拉伸。官方：
   > Motion automatically corrects distortion on border radius and box shadow, but these must be set via `style`, `animate`, or another animation prop.

   本项目 `mini.tsx:131` 的 `borderRadius: expanded ? '20px 20px 0 0' : 24` 就在 `style` 里——✅ 这个写法对，别改成 CSS class。
   本项目 `mini.tsx:134` 的 `boxShadow: '0 12px 40px ...'` 同理。**但注意**：§3 会建议把 `boxShadow` 换成 `filter: drop-shadow()`。

3. ✅ **子元素也会被 scale 拉伸**，需要给直接子元素也加 `layout`：
   ```tsx
   <motion.div layout>
     <motion.div layout />   {/* 修正子元素形变 */}
   </motion.div>
   ```

4. ✅ **`layoutId` 是全局命名空间**，多个实例会互相抢。
   > Because `layoutId` is global across the site, wrap each `TabRow` in `LayoutGroup` with a unique `id` prop.

   本项目有两个 React root（`src/main.tsx` + `src/mini.tsx`，见 `floating-window-review.html` §6），**两个 root 各自独立**，全局性只在单个 root 内成立 —— 但仍建议给 mini 根节点包 `<LayoutGroup id="mini">` 以防未来复用一个组件。

5. ✅ **`layout` 默认每帧 render 都测量 DOM**。列表长/高频更新时必须收窄：
   ```tsx
   <motion.li layout layoutDependency={a.status} />
   ```
   官方：*By default, layout changes are detected every render. Passing a `layoutDependency` prop reduces measurements.*

   🔶 对本项目：`agentDelta` 每秒几十次更新时，带 `layout` 的元素会跟着测几十次。**这是 `layout` 头号性能杀手**。

6. ✅ **shared layout 用"目标元素"的 transition**（容易困惑）：
   > In a shared layout animation, the transition defined on the element being animated to is used.

7. 🔶 **StrictMode 双调用**：开发模式 `React.StrictMode`（本项目 `mini.tsx:437` 已开）下 effect 跑两次。若在 effect 里 `animate()` 且未 cleanup，会出现"动画播两遍 / 中途被另一个 animate 打断"。生产无此问题，但**别为此关 StrictMode**——把它当作发现真 bug 的探针。

### 1.4 Spring 物理参数（✅ `react-transitions`）

```tsx
transition={{ type: "spring", visualDuration: 0.3, bounce: 0.25 }}
```

| 参数 | 默认 | 官方描述 | 调参直觉 |
|---|---|---|---|
| `bounce` | `0.25` | -1..1；`0` 临界阻尼，`1` 极度弹，负值过阻尼 | **首选参数**。UI 不要超过 `0.4` |
| `visualDuration` | — | 视觉上到达目标的秒数，**覆盖 `duration`** | 比 `stiffness/damping` 好懂，优先用它 |
| `stiffness` | `1`(文档) / 实际常用 100-500 | 越大越"突然" | 与 damping 成对调 |
| `damping` | `10`(文档) / 实际常用 20-40 | 0 = 永久震荡 | |
| `mass` | `1` | 越大越"迟滞" | |
| `velocity` | 当前值速度 | 初始速度 | 打断动画时继承速度，**手感的关键** |
| `restSpeed` / `restDelta` | `0.1` / `0.01` | 低于阈值即结束 | 高频更新时调大 `restDelta` 可省 CPU |

⚠️ ✅ 官方明确：**设了 `stiffness`/`damping`/`mass` 后，`bounce` 和 `duration` 会被覆盖**。别混着写。

🔶 本项目建议值（社区常见区间，需实机调）：
- 状态点状态流转（`running`↔`idle`）：`{ type: "spring", visualDuration: 0.2, bounce: 0 }` —— 状态色变化不该弹
- 悬浮窗展开/收起：`{ type: "spring", visualDuration: 0.32, bounce: 0.15 }`
- 新增 agent 入场：`{ type: "spring", visualDuration: 0.25, bounce: 0.3 }`

> 注意：现在 `mini.tsx:129` 用的是 `cubic-bezier(0.34, 1.56, 0.64, 1)`，这是**过冲贝塞尔**，等价于 spring 的 `bounce≈0.35`。手感已经接近了，换 spring 的收益主要是**可被打断**（新状态来了速度能继承）。

### 1.5 包名 / 版本坑（🔶 高频踩）

- `framer-motion` 已改名为 **`motion`**，React API 从 **`motion/react`** 导入。
  ✅ 官方文档示例里仍出现 `from "framer-motion"`（历史页面未全量清理），但新文档与 `useMotionValueEvent` 页已是 `motion/react`。
  本项目 `package.json` **没有装任何动画库**，若要引入请装 `motion`，不要装 `framer-motion`。
- React 19 + `motion`：✅ 支持。本项目 `react@^19.3.1`，没问题。
- 🔶 包体积：`motion` 完整包较大。悬浮窗只需要 `motion/react` + `LazyMotion`/`domAnimation` 可显著瘦身。✅ 官方有 `react-reduce-bundle-size` 页与 `LazyMotion`。

```tsx
// ✅ 官方 LazyMotion 用法（reduced bundle）
import { LazyMotion, domAnimation, m } from "motion/react"
<LazyMotion features={domAnimation} strict>
  <m.div animate={{ opacity: 1 }} />
</LazyMotion>
// 注意：LazyMotion 下用 m.xxx 而不是 motion.xxx
```

---

## 2. 表现「连接关系」：A 正在发给 B

### 2.1 三条技术路线的取舍

| 方案 | 适用 | 性能 | 复杂度 |
|---|---|---|---|
| **SVG `<path>` + `stroke-dashoffset`** | 固定拓扑、几十条边 | 中（非合成层，重绘） | 低 |
| **SVG `<path>` + Motion `pathLength`** | 同上，且要"绘制"效果 | 中 | 低（✅ 官方 API） |
| **`transform` 动画的元素做"数据包"** | 直线/二次曲线上的移动点 | **高**（合成层） | 中（要算位置） |
| **Canvas / OffscreenCanvas** | 上百条边、粒子流 | **最高** | 高（要自己做命中测试、无障碍、缩放） |

🔶 经验阈值：**边数 < ~50 用 SVG；> ~100 或每帧都在变拓扑才考虑 Canvas**。本项目的 agent 数（`mini.tsx:188` 已 `slice(0, 8)`，全量也就几十）**远未到 Canvas 门槛**。

### 2.2 方案 A：SVG `stroke-dashoffset`（纯 CSS，零 JS）

```css
.edge { stroke-dasharray: 1 12; }          /* 短划线 + 间隔 = 虚线 */
@keyframes flow { to { stroke-dashoffset: -13; } }  /* 一个周期 = dash+gap 之和 */
.edge.active { animation: flow 0.9s linear infinite; }
```

🌐 平台规范：`stroke-dashoffset` 是 **SVG 呈现属性**，走的是主线程 paint，**不合成**。
→ 只适合"少量、常驻"的流动线。10 条以上叠加会明显掉帧。

⚠️ 关键：**偏移量必须是 dash+gap 的整数倍**，否则循环接缝处会"跳"。`1 12` → 周期 13，所以是 `-13`。

### 2.3 方案 B：Motion `pathLength`（✅ 官方，推荐做「连线生长」）

```tsx
<motion.path
  d={d}
  fill="none"
  stroke={color}
  initial={{ pathLength: 0 }}
  animate={{ pathLength: 1 }}
  transition={{ duration: 0.6, ease: "easeOut" }}
/>
```

✅ 官方（`react-svg-animation`）：
> Use the `pathLength` progress value (0-1) to animate a line being drawn. Works on path, circle, ellipse, line, polygon, polyline, and rect.

相关 API（✅ `svg-animation` / `svg-effect`）：`pathLength`、`pathSpacing`、`pathOffset`，均为 0-1 进度值。
**收益**：不用自己 `getTotalLength()` 测长，天然适配响应式尺寸。

### 2.4 方案 C：「数据包」流动（真正不卡的做法）🔶

核心思想：**画一条不可见的路径，让一个 `div` 沿它移动；移动只改 `transform`。**

```tsx
// 1) 隐藏的参考路径（仅用于测点）
<path ref={pathRef} d={d} fill="none" stroke="none" />

// 2) 数据包：只动 transform
const len = pathRef.current.getTotalLength()
const pt  = pathRef.current.getPointAtLength(progress * len)
// 用 motion value 直接写，绕过 React render
<circle style={{ x, y }} r={3} />

useEffect(() => {
  const controls = animate(0, 1, {
    duration: 1.2, repeat: Infinity, ease: "linear",
    onUpdate: (p) => {
      const pt = pathEl.getPointAtLength(p * len)
      x.set(pt.x); y.set(pt.y)      // ✅ 不触发 re-render
    },
  })
  return () => controls.stop()        // 🔶 StrictMode 下必须 cleanup
}, [d])
```

🔶 坑点：
- `getPointAtLength` 是**同步 DOM 读取**，每帧调用会强制 layout。路径 `d` 不变时应**缓存采样点**（如 60 个采样点数组），后续只做数组索引 + 插值。
- 路径随窗口 resize 变化时，需 `ResizeObserver` 重算并**重启**动画（`controls.stop()` 先）。
- `animate()` 的 `onUpdate` 优于 `useEffect + setState`：后者每帧一次 React render。

### 2.5 本项目特化：不要真的画连线

🔶 悬浮窗尺寸只有 ~52px 高（`mini.tsx:120`），**8 个状态点之间画连线在视觉上必然变成一团**。建议：

| 场景 | 建议表达 |
|---|---|
| mini 悬浮窗 | **不画线**。用「目标 agent 状态点被短暂高亮 + 一个 3px 数据包沿直线飞到目标点 + 目标点 ring 扩散一次」 |
| 主窗 `ChannelView` | 可用 SVG `pathLength` 做"消息投递"生长动画；拓扑固定，边数少 |
| `ActivityFeed.tsx` | 已有 `fromName → toName` 语义，用**文字 + 短横线**的 CSS 过渡即可，别上 SVG |

⚠️ 重要的工程坑：**mini 窗口高度是 Go 侧 `resizeMiniWindow` 控制的**（`mini.tsx:79-82`）。
如果动画改变了内容高度，而窗口尺寸同步跟随，会出现"动画与窗口缩放打架"。正确顺序：

```
动画开始 → 动画进行（窗口不动，内容溢出被裁剪）
        → onExitComplete / onAnimationComplete → 调 resizeMiniWindow
```

用 ✅ `AnimatePresence` 的 `onExitComplete` 或 `motion` 组件的 `onAnimationComplete` 做收尾，**不要**在 `useEffect` 里按数据长度算高度（现状 `mini.tsx:80` 就是这么做的）。

---

## 3. 可访问性：`prefers-reduced-motion`

### 3.1 三层防线

**① 库层（✅ 官方 `react-accessibility`）—— 首选，一行搞定**

```tsx
import { MotionConfig } from "motion/react"

<MotionConfig reducedMotion="user">
  <MiniApp />
</MotionConfig>
```

✅ 官方原文：
> Wrap your app in MotionConfig with `reducedMotion="user"` to automatically disable **transform and layout** animations for users who prefer reduced motion, while **preserving opacity and backgroundColor** animations.

含义很重要：**它不会把动画全关掉**，而是把"位移/缩放/布局"降级为"透明度变化"。这正是产品想要的——状态变化仍可见，只是不晃。

`reducedMotion` 取值：
- `"user"`（默认行为：跟随系统）
- `"always"`（强制减少）
- `"never"`（强制不减少）

✅ 官方还提供了"应用内覆盖"：
> Allow users to override Reduced Motion for your site by setting `reducedMotion` to "always" or "never" based on their profile.

→ 建议在 `SettingsModal.tsx` 加一个「减少动效」三态选项（跟随系统 / 开 / 关），映射到 `MotionConfig`。

**② Hook 层（✅ `useReducedMotion`）—— 需要定制降级时**

```tsx
import { useReducedMotion } from "motion/react"

const reduce = useReducedMotion()
const animate = reduce ? { opacity: 1 } : { x: 0, opacity: 1 }
```

✅ 官方示例就是「把 transform 换成 opacity」。

**③ CSS 层（🌐 标准媒体查询）—— 覆盖纯 CSS 动画**

本项目 `mini.tsx:281-292` 的 `pulse` / `errorPulse` 是纯 CSS `@keyframes`，`MotionConfig` **管不到**。必须自己加：

```css
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    animation-duration: 0.01ms !important;
    animation-iteration-count: 1 !important;   /* 关掉无限循环 */
    transition-duration: 0.01ms !important;
    scroll-behavior: auto !important;
  }
}
```

🔶 社区经验补充：不要用 `animation: none` —— 会让依赖 `animationend` 的逻辑永远不触发。`0.01ms + 1 次` 是业界通行做法（"reduce, not remove"）。

### 3.2 本项目必须补的三处

| 文件:行 | 问题 | 处置 |
|---|---|---|
| `mini.tsx:281-292` | 内联 `<style>` 里的 `pulse`/`errorPulse` 无限动画 | 加媒体查询；**或**改成"只在 `status==='running'` 时挂 class"以节省 CPU |
| `mini.tsx:163-166` | `transition: 'all 0.2s'` | 收窄属性 + 受媒体查询控制 |
| `src/index.css:354` | `animation: pulse-dot 1.2s ease-in-out infinite` | 同上 |

> 🌐 补充规范点：`prefers-reduced-motion` 在 Electron/WebView 里**跟随系统设置**（macOS 辅助功能 → 显示 → 减弱动态效果）。Chromium 支持良好。不需要额外开关。

### 3.3 无障碍不是只有动效

🔶 状态点（`mini.tsx:170-200`）当前是纯 `<span>` + 颜色 + 大小差异：
- 无 `role` / `aria-label` → 屏幕阅读器读不出来
- **仅靠颜色区分 `running/queued/error/idle`**（`#e8a33d`/`#4d8cf0`/`#e5534b`/`#6e7789`）→ 违反 WCAG 1.4.1「不能只用颜色传达信息」
- 建议：`role="status"` + `aria-label={`${a.name} ${statusLabel(a.status)}`}`，并补**形状/角标差异**（错误已有 `!` 角标，`mini.tsx:203-227`，这个做法是对的）。

---

## 4. 高频状态更新（每秒多次）下的性能要点

> 这一节比 §1/§2 更重要。**动画库救不了重渲染架构。**

### 4.1 哪些属性可动画（🌐 浏览器渲染管线）

| 阶段 | 属性 | 成本 |
|---|---|---|
| **Composite only** ✅ | `transform`、`opacity`、`filter`(部分)、`backdrop-filter` | 最便宜，GPU 合成，不碰主线程布局 |
| **Paint** | `background-color`、`color`、`box-shadow`、`border-radius`、`stroke-dashoffset` | 中，需重绘 |
| **Layout** ❌ | `width`/`height`、`top`/`left`、`margin`、`padding`、`max-height` | 最贵，可能连锁 reflow |

✅ 官方 `performance` 页原文：
> Setting an element's `height` triggers layout, paint, and composite steps, which can be slow.

✅ 官方推荐：
> Animate independent transforms like `x` and `scale`, which are hardware accelerated just like animating `transform` directly.

✅ 官方 `boxShadow` → `filter`：
> Animating `filter` with `drop-shadow` is better performing than animating `boxShadow`.

✅ 官方 `willChange`：
> Setting `willChange` to `"transform"` hints the browser to create a new layer, reducing repaint cost... **Use sparingly**, as each layer consumes GPU space.

### 4.2 本项目可立即修的动画性能问题

| 位置 | 现状 | 问题 | 建议 |
|---|---|---|---|
| `mini.tsx:163-166` | `transition: 'all 0.2s'` | `all` 会把**未来任何属性变化**都纳入过渡，含 layout 属性 | 显式列出：`'background-color .18s, opacity .18s, box-shadow .18s'` |
| `mini.tsx:170-186` | 状态点 `transition: 'all 0.2s'`，且同时改 `width`/`height`(10→12)/`boxShadow`/`opacity` | `width/height` 触发 layout；`boxShadow` 触发 paint | 改成 `transform: scale()`（合成层）+ `opacity`；`boxShadow` → `filter: drop-shadow()` |
| `mini.tsx:278` | 展开面板 `transition: 'max-height .3s'` | **`max-height` 是最贵的一类**（每帧 layout） | 已知高度时用 `height`（仍 layout）；**更好**：`transform: scaleY()` 或 Motion `layout` / `animate()` 的 `height:"auto"` |
| `mini.tsx:284` | 面板 `overflowY: 'auto'` + `max-height` 过渡 | 过渡中滚动条来回闪 | 过渡期间 `overflow: hidden`，结束恢复 |
| `mini.tsx:281-292` | `pulse` 无限动画同时叠加在多个点上 | 每个无限动画一个合成层 + 每帧 paint（`opacity` 可合成，但 `errorPulse` 动了 `box-shadow` → paint） | `errorPulse` 去掉 `box-shadow`，改用 `opacity` on 一个伪元素 |

### 4.3 React 重渲染：真正的瓶颈

**问题根源**：`useSidecar.ts:61` 的单一 `useState`，`agents` 数组每次都是新引用 → `App.tsx:39-46` 的 `useMemo` 依赖 `s.agents`，全部失效 → 所有子组件重渲染。

**手段 1：拆 state / 拆分订阅（最有效）** 🔶
把"每秒多次"和"每秒 0 次"的数据分开放：

```ts
// 慢变：agents 列表（增删、状态流转）→ React state
// 快变：streaming token 增量 → 完全不走 React state
```

`mini.tsx:51-57` 的做法（`lastOutputs` 存 `useRef`，不 `setState`）**已经是正确范式**，应推广到 `useSidecar.ts`。目前 `useSidecar` 里 `streaming` 放在 state 里（`useSidecar.ts:32`），且 `agentMessage` 每次都写 → 主窗会随 token 流重渲染整树。

**手段 2：`useSyncExternalStore` + selector 订阅**
把 `agents` 里单个 agent 的订阅粒度降到 item 级，配合 `useSyncExternalStore` 避免父级 state 变化时全量重算。

**手段 3：`React.memo` + 稳定 props**
`mini.tsx` 里每个状态点是内联对象 → 必然失效。需拆成 `<AgentDot agent={a} />` 并 `memo`，且回调用 `useCallback`。

**手段 4：Motion Value 绕过 React（✅ 官方推荐的免重渲染通道）**
✅ 官方 `react-motion-component`：
> Using a motion value instead of React state for style updates **avoids triggering React re-renders**.

```tsx
const x = useMotionValue(0)
useEffect(() => { setTimeout(() => x.set(100), 1000) }, [])  // ✅ 无 re-render
return <motion.div style={{ x }} />
```

✅ 相关 API：`useMotionValue`、`useTransform`、`useSpring`、`useMotionValueEvent`、`useTime`、`useVelocity`，全部从 `motion/react` 导入。

关键区分（🔶 容易搞混）：
- `style={{ x }}`（MotionValue）→ **不重渲染**，只在动画帧改 style。用于**高频视觉**
- `animate={{ x: 100 }}`（普通对象）→ 走 prop diff，值变了会触发渲染。用于**低频状态**

**手段 5：`layout` 的测量成本**
见 §1.3-5。高频更新下的 `layout` 元素必须加 `layoutDependency`，或干脆不用 `layout`（局部用 `transform` 手写）。

### 4.4 悬浮窗特有：窗口尺寸动画

🔶 **`Sidecar.resizeMiniWindow()` 是跨进程 IPC**，一帧 16.7ms 的预算里它可能占掉大半。绝对不能在动画帧里调用。

- ✅ 官方对"高度动画"的态度：`animate()` 的 `height: "auto"` 可用，但仍是 layout。
- 🔶 本项目最佳方案（与 `floating-window-review.html` 的 `FitContent` 提案一致）：
  **让窗口跟随内容**（`FitContent` + `MaxHeight`），React 侧**只做内容动画，不碰窗口尺寸**。
  这样动画全在合成层/单次 layout 内完成，Go 侧零 IPC 抖动。

---

## 5. 落地建议（按性价比排序）

| 优先级 | 动作 | 依据 | 是否需引入依赖 |
|---|---|---|---|
| **P0** | `mini.tsx` 去掉 `transition: 'all'`；`max-height` 过渡改 `transform`/`layout`；`errorPulse` 去掉 `box-shadow` | §4.1 ✅ | 否 |
| **P0** | 全局加 `@media (prefers-reduced-motion: reduce)` 兜底 | §3.1 ③ 🌐 | 否 |
| **P0** | `useSidecar` 把 `streaming` 移出 React state（改用 ref/外部 store） | §4.3 | 否 |
| **P1** | 引入 `motion`，用 `<MotionConfig reducedMotion="user">` + `AnimatePresence mode="popLayout" initial={false}` 做状态点增删 | §1.2 §3.1 ✅ | 是（`motion`） |
| **P1** | agent 状态点拆 `memo` 组件，回调 `useCallback` | §4.3 | 否 |
| **P1** | 悬浮窗改 `FitContent`，React 不再调 `resizeMiniWindow` | §4.4 | 否（Go 侧） |
| **P2** | 状态点补 `role="status"` + `aria-label` + 非颜色区分 | §3.3 | 否 |
| **P2** | 「投递」用 Motion `pathLength` 做一次性连线段（仅主窗，非 mini） | §2.3 ✅ | 是 |
| **P3** | 「数据包」沿路径飞行（`getPointAtLength` + MotionValue，缓存采样点） | §2.4 🔶 | 是 |
| **不做** | Canvas 连接图、无限流动虚线 | §2.1 🔶 边数远低于阈值 | — |

---

## 6. 官方文档索引（本次引用的原文页）

| 主题 | URL |
|---|---|
| AnimatePresence（exit/mode/initial/custom/propagate/onExitComplete） | https://motion.dev/docs/react-animate-presence |
| Layout animations（layout/layoutId/borderRadius 修正） | https://motion.dev/docs/react-layout-animations |
| LayoutGroup（id 命名空间、与 AnimatePresence 混用） | https://motion.dev/docs/react-layout-group |
| Motion component（layoutDependency、MotionValue 免重渲染） | https://motion.dev/docs/react-motion-component |
| Transitions / Spring（bounce/visualDuration/stiffness/damping/mass/rest*） | https://motion.dev/docs/react-transitions |
| SVG animation（pathLength/pathSpacing/pathOffset） | https://motion.dev/docs/react-svg-animation |
| Accessibility（MotionConfig reducedMotion / useReducedMotion） | https://motion.dev/docs/react-accessibility |
| reduce bundle size（LazyMotion / domAnimation） | https://motion.dev/docs/react-reduce-bundle-size |
| Performance（height=layout、willChange、filter drop-shadow、独立 transform） | https://motion.dev/docs/performance |
| useTransform / useMotionValue / useMotionValueEvent | https://motion.dev/docs/react-motion-value |

> 注：`motion.dev` 部分页面示例仍写作 `from "framer-motion"`（迁移遗留）；新包名是 `motion`，React 入口 `motion/react`。

---

## 7. 未验证 / 需实机确认

1. 🔶 Spring 参数的具体数值手感 —— 必须在真机 macOS 上试，无文档可依。
2. 🔶 `motion` 打包后对 `mini.html` 体积的影响（mini 是独立入口，`vite.config.ts` 需确认多入口配置）。
3. 🔶 `backdrop-filter: blur(20px)`（`mini.tsx:136`）+ 动画叠加在 Electron 透明窗口下的实际帧率 —— 透明窗口 + backdrop-filter 是已知的 GPU 重灾区，建议单独 benchmark。
4. 🔶 `getPointAtLength` 在各 WebView 内核下的缓存收益 —— 需 profile。
5. 待确认：`mini` 与主窗是否共享 `node_modules` 打包产物（影响是否值得为 mini 单独用 `LazyMotion`）。
