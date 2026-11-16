---
name: ui-design
description: >
  multi-agent-desktop 的界面设计规范。写任何 .tsx / .css 前读它：
  设计 token、布局层级、动效、空状态、无障碍、以及本项目踩过的具体坑。
  当用户说「好看一点」「有设计感」「加个动效」「这个样式不对」时用它。
---

# UI 设计规范

这套规范来自本仓库实际迭代出来的结果，不是通用设计理论。
每条规则后面尽量写明「为什么」，因为反例都在 git 历史里真实发生过。

**核心判断标准**：一次改动是否让用户更快看懂「发生了什么、我该做什么」。
不满足这条的「美化」不做。

---

## 1. 颜色：只走 token，不写字面值

`src/index.css` 定义 169 个变量，组件里**禁止**出现 `#fff`、`rgba(...)` 这类字面值。

判断依据：改主题时只需要动 `:root` 一处。任何组件内的硬编码都会在亮色/暗色切换时暴露。

### 命名规律

```
--bg-{base|chrome|surface|elevated|inset}     背景层级
--bg-surface-{hover|active}                   交互态
--bg-glass / --bg-glass-strong                玻璃层（侧栏、检查器）
--border-{subtle|default|strong|focus}        边框
--text-{primary|secondary|tertiary|disabled}  文字层级
--{success|warning|danger|info}-{,dim,border} 语义色三件套
--diff-{add,del}-{bg,text}                    diff 专用
--agent-1..6                                  agent 身份色
--ambient-{grid|vignette|opacity}             氛围层
--space-1..8 (4px 基准)  --text-xs..xl  --radius-sm..full
```

语义色必须成套使用：`--success` 配 `--success-dim`（背景）+ `--success-border`（边框）。
只用一个会导致「有色文字浮在无背景上」，在亮色主题下特别脏。

---

## 2. 亮色主题的三个陷阱（真实踩过）

亮色主题曾经整屏发灰。三个原因叠加，单独看都不明显：

### 2.1 底色不能用中性灰

```css
/* ✗ 错：灰底 + 纯白卡片，观感又脏又没层次 */
--bg-base: #f2f3f5;

/* ✓ 对：冷调近白，靠边框分层 */
--bg-base: #fafbfc;
```

Vercel / Linear 的做法：底色近白、卡片纯白、**层次靠边框而不是靠底色深浅**。

### 2.2 每个新 token 都必须给亮色主题定义

`--bg-glass` 只在暗色块里定义了 `rgba(16,19,24,.72)`，
亮色主题继承到的是**半透明黑**——等于在亮色界面上盖了层黑纱。

**规则**：在 `:root`（暗色）新增任何视觉 token，立刻去 `:root[data-theme='light']` 补一个对应值。

### 2.3 暗角不能用底色

```css
/* ✗ 错：暗色下是自然暗角，亮色下是四周一圈灰雾 */
background: radial-gradient(..., var(--bg-base) 100%);

/* ✓ 对：拆成独立 token，两套主题各给各的 */
background: radial-gradient(..., var(--ambient-vignette) 100%);
```

同理，网格线颜色也要拆：暗色用白线 `rgba(255,255,255,.028)`，
亮色用黑线 `rgba(15,23,42,.045)`，否则亮色下网格完全不可见。

---

## 3. 布局层级

```
AmbientBackground (position:fixed, z-index:-1, pointer-events:none)
└── c-shell (z-index:1, background:transparent)
    ├── Sidebar      --bg-glass + backdrop-blur
    ├── 主区域       （舰队 / 对话 / 频道）
    └── Inspector    --bg-glass + backdrop-blur
```

**氛围层必须在 `c-shell` 外面**。放在里面时它是 `position:fixed` 且带不透明底色，
会把所有兄弟内容盖住——整屏只剩背景光，消息全消失。这个 bug 真实发生过。

### 玻璃层

侧栏和检查器用玻璃底，让氛围光透上来，界面才有纵深：

```css
background: var(--bg-glass);
backdrop-filter: blur(var(--glass-blur));
```

**性能约束**：`backdrop-filter` 在内容高频变化时会持续重算。
流式文本容器必须隔离：

```css
.c-fleet-stream {
  contain: content;
  transform: translateZ(0);  /* 提升为独立合成层 */
}
```

实测：不加这两行，8 agent × 20 消息下流式输出最差帧 62ms（明显卡顿）；
加上后稳定 18ms。护栏在 `e2e/streaming-perf.spec.ts`，阈值 40ms。

---

## 4. 动效

### 用 CSS transform，不用 WebGL

这是桌面应用，GPU 要留给用户的真实工作负载（agent、终端、diff）。
氛围光斑用两个 `animation` 漂移 + 鼠标视差，全部由合成器处理，不占主线程。
只有真需要粒子/大规模着色时才考虑 WebGL。

### 时长与缓动

```
--dur-fast  交互反馈（hover、按下）    ~120ms
--dur-base  状态变化（展开、切换）     ~200ms
--ease      默认缓动
```

### 必须有 `prefers-reduced-motion` 兜底

```css
@media (prefers-reduced-motion: reduce) {
  .c-xxx { animation: none; transform: none; }
}
```

这不是可选项。前庭功能障碍用户会因持续动画产生眩晕。

### 动效要服务于信息

| 场景 | 动效 | 为什么 |
|---|---|---|
| agent 运行中 | 顶部色条呼吸 + 状态点闪烁 | 证明「它在动，不是卡住」 |
| 正在流式输出 | 尾部光标 | 区分「还在写」和「写完了」 |
| 新建 agent | 头像弹性入场 | 让「多了个队友」被感知到 |
| 右键菜单 | 从鼠标位置轻微放大 | 比淡入更有「点开了」的手感 |
| 产出增长 | 强度条宽度过渡 | 文字在涨，条就在涨 |

**反例**：给所有列表项加 hover 位移、给按钮加弹跳——
密集操作区里的装饰性动效只会让人分心。

---

## 5. 空状态

空状态是首次使用的**唯一出口**，必须给可点击的下一步。

```tsx
/* ✗ 错：只有一句文案 */
<div className="c-empty">还没有 agent</div>

/* ✓ 对：一键角色 + 自定义入口 */
<div className="c-empty">
  组建你的 agent 舰队
  <button className="c-empty-cta">🧩 前端</button>
  <button className="c-empty-cta">⚙️ 后端</button>
  <button className="c-hint-chip">自定义名称与角色 →</button>
</div>
```

护栏：`e2e/onboarding.spec.ts` 断言 `.c-empty` 内 `button` 数量 > 0。

### 提示 chip 必须可点

`EmptyChat` 里曾经有三个提示（"这个模块有什么风险？"），
用 `<span>` 渲染——看得见摸不着。必须用 `<button className="c-hint-chip">`，点击后填入输入框。

---

## 6. 交互细节的默认值

这些是「用户不会说但会不舒服」的点：

| 规则 | 反面 |
|---|---|
| 输入框聚焦**不要**整框高亮描边 | 点一下就变蓝框，纯噪音。光标在框里已足够 |
| 弹层支持 Esc 关闭 | 只有点遮罩能关，键盘用户被卡住 |
| 目录/路径**不要**手填输入框 | 已有「浏览」和「拖入」，再让人敲绝对路径是多余的 |
| 空状态按钮 hover 有反馈 | 静态按钮看不出可点 |
| 破坏性操作要二次确认 | 停止 agent 直接执行，误点无法挽回 |
| 长文本默认折叠 3 行，可展开 | 一条长笔记撑满整个侧栏 |

---

## 7. 数据展示

### 用已保存的配置，不要让用户重填

模型选择弹层的反例：用户在设置里配好 API Key，
会话里点开模型弹层却要求**再填一遍**，还把草稿当凭据去请求。

**规则**：凭据只在一处配置。其他地方只做「选择」，不做「输入」。
缺配置时应该明确说「缺什么」并给直达入口，而不是就地展开一个表单。

### 状态要说人话

| ✗ | ✓ |
|---|---|
| `loading` | 正在加载 deepseek 的模型列表… |
| `error` | 警告：先在设置里填写 API Key |
| `queued` | 排队（2） |

---

## 8. 组件清单

新增 UI 前先看有没有现成的：

| 组件 | 用途 |
|---|---|
| `Modal` | 所有弹窗（Radix Dialog） |
| `Select` | 所有下拉（**不要用原生 `<select>`**，深色下会弹系统白底菜单） |
| `DropZone` | 接收拖入的文件/目录（走 mygo 的 `onFileDrop`，DOM 事件拿不到绝对路径） |
| `MentionText` | 消息里的 @提及渲染成 chip |
| `AmbientBackground` | 背景氛围层 |
| `ErrorBoundary` | 兜住渲染异常，不让整屏白掉 |
| `c-hint-chip` / `c-empty-cta` | 空状态的提示 chip / 主行动按钮 |

### 消息渲染的特殊规则

用户消息**不走 markdown**（纯文本语义更准），走 `MentionText`；
agent 消息走 `ReactMarkdown`。

原因：用户输入 `@后端 看下 *这个*` 里的 `*` 会被 markdown 吃掉，
而用户输入本来就不该被当成文档解析。

所有展示消息文本的地方都必须经过 `src/lib/message.ts` 的 `parseMessage()`，
它会剥掉历史遗留的内部标记：`[from x]`、`[to x]`、`[ch-123]`、`[user]`、`名字: `。

---

## 9. 护栏

改 UI 后跑 `npm run verify`（含 `go test -race` + Playwright）。

| 测试文件 | 守住什么 |
|---|---|
| `e2e/theme.spec.ts` | 亮色底色近白、侧栏不是半透明黑、暗角不等于底色、对比度 > 7:1 |
| `e2e/streaming-perf.spec.ts` | 流式输出最差帧 < 40ms |
| `e2e/onboarding.spec.ts` | 空状态有可点按钮、首上手 ≤ 3 次点击 |
| `e2e/message-interaction.spec.ts` | @ chip 渲染、配置在输入区、路径可辨认 |
| `e2e/fleet.spec.ts` | 舰队并排、指标可见、不吃检查器空间 |

**新增视觉规则时同时加断言**。`toBeVisible()` 不够——
颜色、位置、尺寸这类要直接取 `getComputedStyle` 或 `boundingBox()` 比对。

---

## 10. 反模式速查

- ❌ 组件里写字面色值 → 改主题必挂
- ❌ 新增 token 只给暗色 → 亮色继承到错误的半透明黑
- ❌ 用 `position: absolute; bottom: 100%` 做输入框内的补全 → 应该内联在容器里
- ❌ 原生 `<select>` → 深色下弹系统白底菜单
- ❌ 所有列表项加 hover 位移 → 密集区域变吵
- ❌ 空状态只有文案 → 用户卡在第一步
- ❌ 提示文字用 `<span>` → 看得见摸不着
- ❌ 让用户重复填已经配过的凭据 → 直接不信任这个应用
- ❌ `backdrop-filter` 容器内有高频文本更新 → 掉帧
- ❌ 氛围层放在 `c-shell` 内部 → 盖住全部内容
