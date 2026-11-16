# Playbook：主题色不对

适用症状：亮色（或暗色）主题下某个区域「发灰 / 发脏 / 发亮 / 看不清 / 没有层次 / 玻璃感变黑纱」，但功能完全正常。

**这类 bug 不会报错、不会红测试、不会崩。** 它只能靠「把主观感受变成可断言的数值」来防。本仓库已经出过一次（[`../known-issues.md`](../known-issues.md) 第 3 条，`git show cd59bdd`），根因是三个问题叠加：

1. 底色本身是中性灰，而卡片是纯白 → 脏、无层次；
2. **玻璃层只定义了暗色值，亮色继承了半透明黑** → 亮界面上盖黑纱；
3. 氛围暗角用 `var(--bg-base)` 压边 → 亮色下四周压出一圈灰雾（网格线同理，亮色下白线全不可见）。

所以排查顺序是：**先查 token 是不是漏了第二套 → 再查是不是继承错了 → 最后查是不是有人绕过 token 硬编码。**

---

## 第 0 步：先把"不对"变成数字

不要用眼睛下结论。先在 DevTools Console 里量：

```js
// 切主题（应用用的是 <html data-theme="light|dark">）
document.documentElement.dataset.theme = 'light'

// 1. 底色的实际渲染色
getComputedStyle(document.body).backgroundColor         // 期望亮色近白
getComputedStyle(document.querySelector('.c-ambient')).backgroundColor  // .c-ambient 才是真正的底色层

// 2. 侧栏（玻璃层）
const aside = document.querySelector('aside')
getComputedStyle(aside).backgroundColor
getComputedStyle(aside).backdropFilter

// 3. 氛围层 token
const cs = getComputedStyle(document.documentElement)
;['--bg-base','--bg-glass','--bg-glass-strong','--ambient-vignette','--ambient-grid','--ambient-opacity']
  .forEach(k => console.log(k, cs.getPropertyValue(k).trim()))
```

**别只看 `body`**：`src/index.css` 里 `.c-ambient` 自己画了 `background: var(--bg-base)`（`z-index: -1` + `pointer-events: none`），所以真正铺满屏幕的底色是 `.c-ambient`，不是 `body`。两者不一致本身就是线索。

记录下数值再往下走。判据（与 `e2e/theme.spec.ts` 的断言一致）：

| 量 | 亮色期望 | 红线的含义 |
|---|---|---|
| `body` 背景 r/g/b | ≥ 245 / 246 / 247 | 中性灰（三通道接近且 < 250）= 第 1 个来源 |
| `aside` 背景 r/g/b | 都 > 200 | 继承到了暗色玻璃 `rgba(16,19,24,.72)` = 第 2 个来源 |
| `--ambient-vignette` | 不含 `var(--bg-base)` 且非空 | 拿底色压边 = 第 3 个来源 |
| `--ambient-grid` | 非空 | 亮色下用白网格 = 看不见 |
| `--text-primary` / `--bg-base` 对比度 | > 7:1 | 文字发虚 |

---

## 第 1 步：token 是不是定义了两套？

两套主题分别在 `src/index.css` 的：

- `:root { ... }` —— 暗色，也是**默认值**
- `:root[data-theme='light'] { ... }` —— 亮色覆盖

**核心机制（也是核心风险）**：亮色块里没重定义的变量，**会静默继承暗色的值**。这不会报错、不会有警告、不会出现在任何检查里。这正是 `--bg-glass` 那次事故的成因。

### 一键检查：列出「只在暗色块定义、亮色块没有覆盖」的变量

```bash
python3 - <<'EOF'
import re
src = open('src/index.css').read()
def vars_in(block): return set(re.findall(r'(--[a-z0-9-]+)\s*:', block))
i = src.index(':root {');            dark  = vars_in(src[i:src.index('}', i)])
i = src.index(":root[data-theme='light'] {"); light = vars_in(src[i:src.index('\n}', i)])
print("亮色未覆盖（继承暗色）:", sorted(dark - light))
EOF
```

**判据：输出必须只剩下「主题无关」的 token。** 当前 HEAD 的正确结果是**全部 30 个都是中性的**：

```
--space-*, --text-*, --leading-*, --radius-*, --dur-*, --ease,
--font-sans, --font-mono, --header-h, --sidebar-w, --inspector-w,
--composer-min-h
```

出现下面任何一种 = 就是问题：

| 泄漏的 token 类别 | 后果 |
|---|---|
| `--bg-*`（底色/面色） | 亮色用了暗色底，发灰/发黑 |
| `--text-*`（`--text-primary` 等） | 亮底上白字，看不见 |
| `--border-*` | 边框消失或过重 |
| `--accent-*` | 品牌色对比度不足 |
| `--success/warning/danger/info` | 语义色在亮底上刺眼或发虚 |
| `--bg-glass*` / `--glass-*` | **玻璃层变成半透明黑纱（已发生过）** |
| `--ambient-*` | 光斑/网格/暗角在另一套主题下失真（已发生过） |

### 修法

在 `:root[data-theme='light']` 里补上该变量的亮色值，并在旁边写一行注释说明「为什么需要单独定义」。参考已有写法（`src/index.css:150-164`）：

```css
  /* --- 玻璃层（亮色）---
     之前没定义，直接继承了暗色的 rgba(16,19,24,.72) ——
     等于在亮色界面上盖了层半透明黑，这是「发灰」的第二个来源。 */
  --bg-glass: rgba(255, 255, 255, 0.76);
```

---

## 第 2 步：是不是继承 / 引用错了？

token 两套齐了，但某个元素仍然不对，说明它的 `var()` 指向了错的 token，或者层级叠加出了错。

### 2.1 顺着 `var()` 链往上查

```bash
# 谁用了这个 token（反过来：这个 token 从哪来）
grep -rn -- '--bg-glass'      src/*.css src/**/*.css
grep -rn -- '--ambient-vignette' src/
grep -rn -- 'var(--bg-base)'  src/
```

本仓库已确认的「引用错」模式与合法用法：

1. **暗角引用底色**。`--vignette` 原本写 `var(--bg-base)`：暗色下是自然暗角，亮色下就是四周压出一圈灰。修法是拆成独立 token `--ambient-vignette`（暗色仍 = 底色，亮色 = `rgba(226,232,240,.55)`），并且 `src/index.css:662` 处的注释明确写了「用 `--ambient-vignette` 而不是 `bg-base`」。
   - **判据**：`grep -rn 'var(--bg-base)' src/` 的结果里，不该有「当作暗角 / 遮罩 / 渐变压边」的用法。
   - **当前 HEAD 的合法命中**（不要误判）：
     - `src/index.css:273` — `body { background: var(--bg-base) }`（底色本身）
     - `src/index.css:587` — `.c-ambient { background: var(--bg-base) }`（氛围层底色）
     - `src/index.css:25` — 暗色下 `--ambient-vignette: var(--bg-base)`（暗色刻意复用底色）
     - `src/styles/components.css` 与 `ChannelView.tsx`/`MentionPicker.tsx` 里的 `color: var(--bg-base)`（**作"反白字"用**：在 accent 实心块上写底色当白字）
   - **只在该 token 被用作"压边/遮罩/渐变压暗"时才可疑**。
2. **网格线用固定色**。`--ambient-grid` 暗色是 `rgba(255,255,255,.028)`（白线），亮色必须是 `rgba(15,23,42,.045)`（黑线）。用白线 → 亮色下网格完全不可见（不是"淡"，是"零")。

### 2.2 检查叠层的顺序（玻璃 + 氛围 + 底色）

三层的叠加顺序决定了最终色：

```
.c-ambient (background: var(--bg-base), z-index:-1)
  ├─ 光斑 .c-ambient__blob   (opacity: var(--ambient-opacity))
  ├─ 网格 .c-ambient__grid   (var(--ambient-grid))
  └─ 暗角                      (var(--ambient-vignette))
侧栏 / 检查器               (background: var(--bg-glass) + backdrop-filter: blur(var(--glass-blur)))
```

要查的：

- 玻璃层的 `background` 是不是**半透明**且颜色与主题同向（亮色 = 偏白透明，暗色 = 偏黑透明）。亮色配黑透明 = 第 2 个来源复现。
- `--glass-blur` / `--glass-highlight` 是否也两套都有。若亮色 `--glass-highlight` 是白色高光，叠在白色玻璃上等于没有。
- `--ambient-opacity` 两套不同（暗 0.62 / 亮 0.72，`src/index.css:26`、`:164`）—— 亮色需要更强才看得出层次，把亮色调回 0.62 会"看起来没效果"。
- **`backdrop-filter` 层数越多越容易"发灰"**：每层半透明叠加都会压低饱和度。症状是「越往里越脏」。排查：临时给某一层 `background: transparent` 看变化是否"变干净"。

### 2.3 确认 CSS 加载顺序没被打乱

```bash
sed -n '1,6p' src/main.tsx
```

`src/main.tsx:3-4` 的顺序是：`import './index.css'` 在前，`import './styles/components.css'` 在后。**这个顺序不能反**：

- `index.css` = 设计 token（`:root` / `:root[data-theme='light']`）+ 全局基础样式；
- `components.css` = 组件样式，大量使用 `var(--...)`。

顺序反了，组件里的 `var()` 会拿到未定义值 → 走浏览器默认（黑色/透明）→ 表现为「颜色不对」但找不到源头。**目前没有测试保护这个顺序**，改动入口文件时注意。

---

## 第 3 步：是不是有人硬编码绕过了 token？

这是最容易复发的一类。任何 `#rrggbb` / `rgb()` / `rgba()` 直接写在组件里，**都不会随主题切换**。

```bash
# 组件里的字面量颜色（应只剩极少数有正当理由的，例如头像渐变用的 agent 身份色）
grep -rn '#[0-9a-fA-F]\{3,8\}\b' src/components/*.tsx src/mini.tsx | grep -v 'var(--'

# style 对象里的颜色
grep -rnE "color:|background:|borderColor:" src/components/*.tsx | grep -vE 'var\(--|transparent|inherit|none'
```

当前 HEAD 的正确结果：

- 第一条命令**无输出**时，说明没有任何字面量颜色绕过 token。
- 第二条命令会命中一批 `color: a.color` / `background: p.agent.color` / `color: string` —— 这些是 **agent 身份色（运行时数据）**，不是主题 token，**允许**。

所以要看的只有「**不是** `xxx.color` 的命中」。例如出现 `background: '#fff'`、`color: '#333'` 这种字面值就是绕过 token：改成 `var(--xxx)`；若确实需要新颜色，先在 `src/index.css` 的**两套**里都定义再引用。

再看 `src/index.css` 内部的写死值：`color-mix(in srgb, ...)`、`rgba(...)` 出现在**组件样式区**（`.c-ambient__*` 之后）时，要确认它不承载语义。语义色一律用 `--success/--warning/--danger/--info` 及其 `-dim`/`-border` 变体（两套都定义了）。

---

## 第 4 步：用护栏锁定结论（强制）

**主观感受修好了，不等于下次不会回来。** 修完必须做两件事：

### 4.1 确认护栏是绿的

```bash
npx playwright test e2e/theme.spec.ts e2e/fleet.spec.ts
```

`e2e/theme.spec.ts` 有 4 条，直接量 `getComputedStyle` 的实际渲染色：

| 用例 | 断的是什么 |
|---|---|
| 亮色主题底色是近白，不是灰 | `body` r≥245 / g≥246 / b≥247 |
| 亮色主题下侧栏不是半透明黑 | `aside` r/g/b 都 > 200 |
| 亮色主题氛围层暗角不是灰雾 | `--ambient-vignette` 不含 `var(--bg-base)`，两个 ambient 变量非空 |
| 两套主题的文字对比度都达标 | WCAG 相对亮度比 > 7:1（亮、暗各测一次） |

`e2e/fleet.spec.ts` 覆盖了 cd59bdd 顺带修的布局副作用（卡片不出视口、检查器不吃对话区空间）。

### 4.2 证明护栏能红（否则它可能是空转的）

新加一条断言后，**把对应的 CSS 改回错误值**，跑该用例，必须失败：

```bash
# 例：验证「侧栏不是半透明黑」真的有效
# 临时删掉亮色块里的 --bg-glass 定义 → 继承暗色 rgba(16,19,24,.72)
npx playwright test e2e/theme.spec.ts -g '侧栏不是半透明黑'   # 必须红
# 恢复
```

不红的断言不算护栏 —— 本仓库已经有过一次「检查器一直绿但其实什么都没检查」的教训（`../known-issues.md` 第 8 条，typecheck 空转）。

### 4.3 记一条

在 [`../known-issues.md`](../known-issues.md) 追加记录，`为什么没被及早发现` 那一段写清这次是「哪一类来源」（漏定义 / 引用错 / 硬编码），并把新增/复用的 e2e 用例挂上去。

---

## 附：快速对照表

| 症状 | 最可能的原因 | 直接查 |
|---|---|---|
| 整屏发灰、像蒙了层雾 | 亮色继承了暗色 `--bg-glass` | 第 1 步的脚本，看是否列出 `--bg-glass` |
| 四周一圈灰 | 暗角用了 `var(--bg-base)` | `grep -rn 'var(--bg-base)' src/` |
| 有层次但"脏"、卡片浮不起来 | 底色本身是中性灰 | `e2e/theme.spec.ts` 第 1 条 |
| 亮色下网格完全看不见 | `--ambient-grid` 用了白线 | `src/index.css` 亮色块 |
| 光斑/氛围"加了等于没加" | `--ambient-opacity` 亮色没调高 | `src/index.css:164` |
| 只有某个组件颜色不对 | 组件内硬编码 | 第 3 步的 grep |
| 文字发虚、对比度不足 | `--text-*` 亮色未覆盖 | 第 1 步的脚本 |
| 切主题后部分区域不变 | 该区域没用 token | 第 3 步的 grep |
