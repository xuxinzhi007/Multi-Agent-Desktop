# 研究：桌面 Agent 如何在用户正在看的网页上接管并点击

> 调研日期：2026-11-19
> 范围：本仓库 `internal/browser`（macOS，Apple Events 注入固定脚本）对照 Playwright、Chrome DevTools Protocol、browser-use 源码、OpenAI computer use / Operator / ChatGPT 浏览器、Anthropic computer use、Safari WebDriver、macOS `CGEvent`
> 资料：各项目官方文档、协议 JSON、一方法源码、本机随应用安装的脚本词典。不引用二手综述。
>
> **置信度标注**
> - ✅ **官方文档或一方法源码** —— 下面每条都带原始 URL 或仓库文件
> - 🌐 **平台规范** —— DOM / WebDriver 规范原文
> - 🔶 **文档没写死、只读了相邻材料** —— 文末单独列出，不当成事实
>
> 本文只记 API 名、协议字段和源码在做什么。**不是**可粘贴的实现，也不改代码。

---

## 0. 先对齐现状

读 `internal/browser` 得到的约束，后面每条路线都拿它衡量：纯 Go，无 cgo，macOS 优先，悬浮岛保持很小，用户要在**自己正在看的那个标签**上看见接管和点击。模型不能送任意 JS。没有第二个浏览器，也没有「截图 → 坐标」循环。

| 位置 | 现状 |
|---|---|
| `internal/browser/act_darwin.go:57` | `osascript -l JavaScript`，用 Apple Events 进前台浏览器 |
| `internal/browser/act_darwin.go:85-102` | Safari 走 `doJavaScript`；Chrome、Edge、Brave、Arc、Vivaldi、Opera、Dia、Comet 走 `tab.execute({javascript})` |
| `internal/browser/op.go:10-11` | `Op` 只接受 `read` / `open` / `click` / `type` / `press`。脚本在这里生成 |
| `internal/browser/op.go:137-142` | `click` 在主文档里 `querySelectorAll`，用可见文字、`value`、`aria-label`、`placeholder` 找控件 |
| `internal/browser/op.go:94-116` | 画出 `#mad-agent-takeover` 横幅、青色 `outline`、`#mad-agent-cursor` 圆点 |
| `internal/browser/act_darwin.go:24-38` | 先注入 show 脚本，等 650ms，再注入 act 脚本 |
| `internal/browser/op.go:157-159` | 真正的点击是 `target.click()` |

这套东西已经满足「当前标签 + 用户看得见」。它缺的是鼠标事件、iframe、shadow tree，以及系统光标。下面每条成熟路线都回答：它能不能补上这些，以及要不要为此另开一个浏览器。

---

## 1. Playwright：高亮、有头、接已有 Chrome、鼠标、trace

### 1.1 `locator.highlight()` 画在页面上，定位是调试

✅ [Locator.highlight](https://playwright.dev/docs/api/class-locator#locator-highlight)：

> Highlight the corresponding element(s) on the screen. Useful for debugging, don't commit the code that uses locator.highlight().

可选 `style`（v1.60 起）写成内联 CSS，例如 `outline: 2px dashed red`，或 `{ color: 'red' }`。返回 `Promise<Disposable>`。`locator.hideHighlight()` 去掉这次高亮。

这和本仓库的青色描边是同一类手段：画在页面上，人眼能看见。官方把它标成调试 API，不建议留在提交的测试里。

### 1.2 有头模式是「再开一个浏览器窗口」

✅ [Debugging Tests · Headed mode](https://playwright.dev/docs/debug#headed-mode)：默认 headless。要看见窗口：

```js
await chromium.launch({ headless: false, slowMo: 100 });
```

`headless` 默认 `true`。✅ [BrowserType.launch](https://playwright.dev/docs/api/class-browsertype#browser-type-launch) 同样写明 `headless` 默认 `true`，`slowMo` 把每次操作放慢若干毫秒。`--debug` 也会让浏览器以 headed 启动，并把默认超时设为 0。

这是 Playwright 自己启动的窗口，不是用户已经打开的那个标签。

### 1.3 `connectOverCDP` 能接已有 Chromium，而且只要调试端点

✅ [BrowserType.connectOverCDP](https://playwright.dev/docs/api/class-browsertype#browser-type-connect-over-cdp)：

> This method attaches Playwright to an existing browser instance using the Chrome DevTools Protocol.
>
> Connecting over the Chrome DevTools Protocol is only supported for Chromium-based browsers.

示例是 `playwright.chromium.connectOverCDP('http://localhost:9222')`，然后 `browser.contexts()[0]`、`defaultContext.pages()[0]`。`endpointURL` 可以是 `http://localhost:9222/`，也可以是 `ws://127.0.0.1:9222/devtools/browser/<id>`。也可以传通道名 `chrome` 或 `msedge`，「connect to a browser with remote debugging enabled in its default user data directory」。

同一页还有三条和本产品直接相关的限制：

- 这条连接的保真度明显低于 `browserType.connect()`。出问题时应改走 Playwright 自己的协议。
- 自己启动浏览器时，若参数和 Playwright 维护的那份不一致，接上之后部分功能会坏。
- `noDefaults`（v1.60）：为 `true` 时不改已有默认上下文的下载、字体、焦点模拟和媒体模拟。「Useful when attaching to a user's daily-driver browser」。默认 `false`。
- `slowMo` 同样可以加在这条连接上，方便人眼跟上。

Chrome 自己从 136 起不允许在**默认资料目录**上开调试端口，见 §2.4。所以「API 允许接到日常 Chrome」和「当前用户那个默认配置的 Chrome 肯不肯开端口」是两件事。

### 1.4 鼠标是页面里的事件；红点在 Trace Viewer / Inspector 里

✅ [Mouse](https://playwright.dev/docs/api/class-mouse)：坐标是主 frame 视口左上角起算的 CSS 像素。

> If you want to debug where the mouse moved, you can use the Trace viewer or Playwright Inspector. A red dot showing the location of the mouse will be shown for every mouse action.

`mouse.click(x, y)` 是 `move` + `down` + `up` 的简写。`move` 的 `steps` 默认 1，设大了会在当前位置和目标之间插多个 `mousemove`。`down` / `up` 派发的是 `mousedown` / `mouseup`。

✅ [Page](https://playwright.dev/docs/api/class-page) 上 `locator.click()` 的步骤里包含「Use page.mouse to click in the center of the element」。每个 `page` 都有自己的 `page.mouse`。

红点不是操作系统光标，也不是普通运行时画在页面上的节点。要在**当时**看见它，得开 Trace Viewer 或 Inspector。本仓库的 `#mad-agent-cursor` 反而是页面里的 DOM，用户盯着标签就能看见。

### 1.5 Tracing 是事后回放

✅ [Tracing](https://playwright.dev/docs/api/class-tracing)：`context.tracing.start({ screenshots: true, snapshots: true })`，结束时 `stop({ path: 'trace.zip' })`。截图用来做时间线预览；`snapshots: true` 等于 `{ dom: true }`。v1.59 的 `live: true` 把 trace 写成随动作更新的未打包文件，「useful for live trace viewing during test execution」。

✅ [Trace viewer](https://playwright.dev/docs/trace-viewer-intro)：在动作前后看页面状态，中间是 DOM 快照，可以打开 DevTools。这是测试录像，不是用户当前标签上的接管条。

### 1.6 iframe 与 shadow DOM：Playwright 写明了边界

✅ [Frames](https://playwright.dev/docs/frames)：页面级 `click` 默认在主 frame。iframe 要用 `page.frameLocator(...)` 或 `page.frame(...)` 再操作。

✅ [Locators · Locate in Shadow DOM](https://playwright.dev/docs/locators#locate-in-shadow-dom)：

> All locators in Playwright by default work with elements in Shadow DOM.

例外写了两条：XPath 不穿透 shadow root；**closed shadow root 不支持**。

---

## 2. Chrome DevTools Protocol

协议正文来自官方镜像的 [`browser_protocol.json`](https://github.com/ChromeDevTools/devtools-protocol/blob/master/json/browser_protocol.json)（本次拉取的 tip-of-tree）。总览页说明：规范源文件是 Chromium 里的 `browser_protocol.pdl`，GitHub 镜像生成这份 JSON。✅ [Chrome DevTools Protocol](https://chromedevtools.github.io/devtools-protocol/)

### 2.1 它是谁的协议

✅ 总览页原文：CDP 用来仪器化、检查、调试、剖析 **Chromium、Chrome 和其他 Blink 浏览器**。CDP 不是 Chrome 的公开支持 API，不保证向后兼容。官方支持直接建在 CDP 上的产品是 Chrome、Chrome DevTools 前端、Chrome DevTools for agents、Lighthouse、Puppeteer、ChromeDriver。第三方应通过这些产品间接使用，而不是自己连 CDP。

### 2.2 调试端口：没有端口就没有那套 HTTP / WebSocket

✅ 总览页「HTTP endpoints」：

> When Chromium or Chrome launches with `--remote-debugging-port=<port>` (for example, `--remote-debugging-port=9222`), it starts a local HTTP server...

| 端点 | 作用 |
|---|---|
| `GET /json/version` | 浏览器版本，以及 `webSocketDebuggerUrl` |
| `GET /json` 或 `/json/list` | 可检查的 target：page、worker 等 |
| `GET /json/list?for_tab` | 把 `tab` 类型也列出来 |
| `PUT /json/new?{url}` | 新建页面；必须用 PUT |
| WebSocket `/devtools/browser/{guid}` | 浏览器级会话，再用 `Target.attachToTarget` |
| WebSocket `/devtools/page/{targetId}` | 单个 target |

`--remote-debugging-port=0` 时，选中的端口和 browser target 路径写到 stderr，以及用户数据目录里的 `DevToolsActivePort`。

Target 类型表里和页面有关的包括 `page`、`tab`、`iframe`（out-of-process subframe）、`worker`。`iframe` 是单独 target，父级在 `parentId`。

### 2.3 Overlay、Input、Target、Page

域名和本任务相关的方法（JSON 原文）：

| 域 | 方法 | JSON 里的说明 |
|---|---|---|
| Overlay | 域描述 | 「drawing atop the inspected page」。域本身 `"experimental": true`，依赖 DOM、Page、Runtime |
| Overlay | `highlightNode` | 按 `nodeId` 或 JS object wrapper 高亮。`nodeId` 与 `objectId` 至少给一个。外观走 `highlightConfig`。还可给 `backendNodeId`、`selector` |
| Overlay | `highlightRect` | 高亮矩形，坐标相对主 frame 视口。JSON 写明：**DPR 不是 1 时坐标不对，客户端得自己乘**（`crbug.com/437807128`） |
| Overlay | `highlightQuad` | 高亮四边形，坐标同样相对主 frame 视口 |
| Overlay | `hideHighlight` | 去掉任何高亮 |
| Overlay | `highlightFrame` | **已废弃**。跨进程时 owner 节点可能在另一个进程，不可靠。让客户端自己找 owner，改用 `highlightNode` |
| Input | `dispatchMouseEvent` | 「Dispatches a mouse event to the page。」`type` 为 `mousePressed` / `mouseReleased` / `mouseMoved` / `mouseWheel`。`x`、`y` 是主 frame 视口的 CSS 像素 |
| Target | 域描述 | 「Supports additional targets discovery and allows to attach to them.» |
| Target | `attachToTarget` | 附到给定 target，返回 `sessionId`。`flatten: true` 时命令带 `sessionId` |
| Target | `getTargets` | 列出可用 target |
| Target | `activateTarget` | 聚焦该 target |
| Page | 域描述 | 「Actions and events related to the inspected page belong to the page domain.» |
| Page | `bringToFront` | 把页面带到前台 |
| Page | `captureScreenshot` | 截页面图，`format` 为 `jpeg` / `png` / `webp` |
| Page | `getFrameTree` | 「Returns present frame tree structure.» |
| DOM | `getDocument` 的 `pierce` | 「Whether or not iframes and shadow roots should be traversed when returning the subtree (default is false).」`describeNode` 同样有这个参数，默认 false |

Overlay 画在检查器的覆盖层上，不往页面 DOM 里插节点。`dispatchMouseEvent` 发的是页面上的鼠标事件，不是 macOS 光标移动。

浏览器级 WebSocket 先 `Target.getTargets` / `Target.attachToTarget`，再在对应 `sessionId` 里发 Overlay 和 Input。跨进程 iframe 要附到类型为 `iframe` 的 target，不能假设主页面会话看得到里面的 DOM。`highlightFrame` 的废弃说明就是这个原因。

### 2.4 Chrome 136：默认资料目录不再接受调试开关

✅ [Changes to remote debugging switches](https://developer.chrome.com/blog/remote-debugging-port)（2025-03-17，Will Harris）：

从 Chrome 136 起，`--remote-debugging-port` 和 `--remote-debugging-pipe` **在调试默认 Chrome 数据目录时不再生效**。必须同时带 `--user-data-dir`，指向非标准目录。浏览器自动化被建议改用 Chrome for Testing，后者仍按原行为处理这些开关。

因此：用户已经开着的、用默认配置目录的那个 Chrome，不能靠再加一个调试端口被 Playwright 或手写 CDP 客户端接上。要 CDP，就得换一个非默认资料目录，那是另一个配置，不是当前这套标签和登录态。

### 2.5 不通过端口的唯一官方入口：`chrome.debugger`

✅ 同一份 [CDP 总览](https://chromedevtools.github.io/devtools-protocol/)：`chrome.debugger` 扩展 API 把 CDP 的 JSON 传输暴露给扩展，manifest 里要 `debugger` 权限。Chrome 只在 best-effort 上支持**面向开发者的调试扩展**。面向终端用户的扩展，以及非调试用途，**不受支持**。后端还会限制扩展通过它碰文件系统或逃出沙箱。命令和事件同样可能无通知变化。

这是「不重启、不换资料目录、仍对当前 Chrome 发 CDP」的官方通道。产品形态是一个带 `debugger` 权限的扩展，而且官方写明不支持终端用户产品。本仓库的约束是纯 Go + React，没有这条扩展。

---

## 3. browser-use：光标和元素序号实际画在哪

仓库：[browser-use/browser-use](https://github.com/browser-use/browser-use)。1.0 和 0.13 不是同一套画法。两边都**没有**在页面上画一个假的系统光标。

### 3.1 当前 main（1.0.6 这一线）：点击之后画橙色角标

✅ [`browser_use/repl/highlight.py`](https://github.com/browser-use/browser-use/blob/main/browser_use/repl/highlight.py) 文件头：

> orange corner brackets around the element a click, focus or text input reached … Cosmetic … aria-hidden, in a closed shadow root and pointer-transparent … A screenshot removes it first … the model never sees a bracket.

做法：

- `Highlighter.after` 只在 `Input.dispatchMouseEvent` 且 `type == mouseReleased`，以及 `DOM.focus`、`Input.insertText` 成功之后触发。
- 鼠标路径用 `DOM.getNodeForLocation` 拿 `backendNodeId`，再 `DOM.resolveNode`，然后 `Runtime.callFunctionOn` 跑一段固定的 `DRAW_JS`。
- `DRAW_JS` 在元素的 `ownerDocument` 里插入 `data-browser-use-interaction-highlight`，`attachShadow({ mode: 'closed' })`，四个角是 `rgb(255,127,39)` 的 3px 边。约 1 秒后透明度收到 0 再移除。
- `hidden()` 在截图前把这块覆盖层拿掉。模型看不到角标。

✅ [`README.md`](https://github.com/browser-use/browser-use/blob/main/README.md) 与此一致：角标显示 1 秒，不出现在 agent 自己的截图里；`Agent(highlight_actions=False)` 关掉。

✅ [`browser_use/agent/prompt.py`](https://github.com/browser-use/browser-use/blob/main/browser_use/agent/prompt.py) 告诉模型：`click_at_xy(x, y)` 在视口坐标发鼠标事件；`screenshot()` 截视口。跨源 iframe 要 `Target.getTargets`，`attach` 时 `flatten=True`，再用那个 `session_id`。

这是给人看的事后描边，不是给模型编号，也不是一个跟着走的光标。点击本身是 CDP 鼠标事件，角标是事件之后补上的。

### 3.2 0.13.14：序号画在截图像素上，点击仍是 CDP 鼠标

✅ [`browser_use/browser/python_highlights.py` @ 0.13.14](https://github.com/browser-use/browser-use/blob/0.13.14/browser_use/browser/python_highlights.py) 模块说明：「replaces JavaScript-based highlighting with fast Python image processing to draw bounding boxes around interactive elements directly on screenshots。」

`create_highlighted_screenshot` 用 PIL 在截图上画虚线框。`process_element_highlight` 里 `index_text = str(element_id)`，注释写明「Use the selector-map key because that is the index shown to the model。」小元素把序号放到框外，避免挡住图标。坐标从 CSS 像素乘 `device_pixel_ratio` 再画到设备像素的截图上。

这条序号**不在用户看着的页面上**。它在送给模型的那张图上。

✅ [`browser_use/actor/mouse.py` @ 0.13.14](https://github.com/browser-use/browser-use/blob/0.13.14/browser_use/actor/mouse.py)：`click` 先 `move` 再按下/松开，底层是 `Input.dispatchMouseEvent`（`mouseMoved` / 按下 / 松开）。没有插入光标 DOM。

✅ [`browser_use/browser/demo_mode.py` @ 0.13.14](https://github.com/browser-use/browser-use/blob/0.13.14/browser_use/browser/demo_mode.py) 注入的是右侧日志面板（`#browser-use-demo-panel`），不是光标。样式里的 `cursor: pointer` 是 CSS 光标属性。

✅ [`MIGRATING.md`](https://github.com/browser-use/browser-use/blob/main/MIGRATING.md)：0.x 的 `highlight_elements` 已删除；1.0 改为默认的橙色角标，`Agent(highlight_actions=False)` 关闭。`browser_use.dom` 和 `browser_use.actor` 的 `Page` / `Element` / `Mouse` 也已移除，改由 REPL 里的 Python 和原始 CDP 驱动。

和本仓库的差别：browser-use 的可见反馈要么在截图上（0.13 的序号，给模型看），要么在点击之后闪一下角标（1.0，并故意不让模型看见）。本仓库的横幅和圆点在点击前就在页面上，而且模型根本不看图像。

---

## 4. OpenAI：computer use、Operator、Codex / ChatGPT 浏览器

### 4.1 computer 工具：截图 + 坐标动作

✅ [Computer use](https://platform.openai.com/docs/guides/tools-computer-use)（同一正文也在 [developers.openai.com](https://developers.openai.com/api/docs/guides/tools-computer-use)）：

有两条路。

1. 模型写代码。桌面示例的工具说明要求 `display(pyautogui.screenshot())`，然后用截图坐标操作。浏览器示例把 Playwright 的 `page` 放进持久 JS 环境，并要求行动前先看截图。
2. 结构化 computer 工具。`tools: [{ type: "computer" }]`。一次 `computer_call` 里有有序的 `actions`。文档中的例子是：

```json
{ "type": "click", "button": "left", "x": 405, "y": 157 }
```

✅ 原文：「The model can request `click`, `double_click`, `drag`, `move`, `scroll`, `keypress`, `type`, `wait`, or `screenshot`.」应用按顺序执行，再截一张新图，用 `computer_call_output` 交回 `computer_screenshot`。缩小截图时，必须把模型坐标映射回环境坐标。

官方示例里的浏览器驱动是自己 `chromium.launch()` 出来的 Playwright `page`，`page.mouse.click(action.x, action.y)`，再 `page.screenshot()`。这是截图坐标循环，跑在调用方提供的浏览器里。

### 4.2 Operator：它自己的浏览器，看截图，用鼠标键盘

✅ [Introducing Operator](https://openai.com/index/introducing-operator/)（2025-01-23；文内 2025-07-17 更新：Operator 并入 ChatGPT agent，独立站点将关闭）：

> Using its own browser, it can look at a webpage and interact with it by typing, clicking, and scrolling.
>
> Operator can “see” (through screenshots) and “interact” (using all the actions a mouse and keyboard allow) with a browser...

模型是 Computer-Using Agent。用户可以随时收回那个**远程浏览器**。接管模式（takeover）下，用户输入登录或支付信息时，Operator 不收集、不截那些输入。这是 OpenAI 托管的浏览器，不是用户 Mac 上的当前标签。

### 4.3 ChatGPT 桌面浏览器和 Codex：另一套配置；现有标签走扩展

✅ [Browser](https://learn.chatgpt.com/docs/browser)：

- 「Browser isn’t available in Codex CLI or the Codex IDE extension.」要用内置浏览器，打开 ChatGPT 桌面应用。
- 内置浏览器「uses a browser profile that is separate from your regular browser. It doesn’t automatically share your existing tabs or browser session.」
- 「Use the browser extension instead when ChatGPT needs to work in an existing Chrome, Edge, Brave, Opera, or Vivaldi tab or use your regular browser profile.」
- 「Computer Use in the browser」：桌面应用里，ChatGPT Work 或 Codex 直接操作**内置浏览器**：打开页面、点击、输入、检查渲染结果、截图、核对。`@Browser` 指这个内置浏览器。
- Developer mode：「gives ChatGPT controlled access to the Chrome DevTools Protocol (CDP).」在 Settings > Browser 里打开 Enable full CDP access。组织可以用 `browser_use_full_cdp_access = false` 关掉。用现有 Chrome 的开发者模式要装 Chrome 扩展，并用 `@Chrome`。
- ChatGPT Work：「uses its own browser, running on a separate computer in the cloud, not the browser on your phone or laptop.」云端浏览器有自己的 cookie 和登录态，「doesn’t use your personal browser’s open tabs, browsing history, saved passwords, cookies, extensions, or existing signed-in sessions.」
- Codex Cloud：任务被授予浏览器权限时，Codex 在**云任务里的浏览器**中打开应用、复现、核对，并可附截图。

文档没有把 Codex 的点击定义成一组公开的坐标 JSON。它写明的是：内置浏览器里可以点击和截图；完整 CDP 是单独打开的开发者模式；要进用户现有的 Chrome 标签，官方路径是浏览器扩展，而不是接当前进程。

---

## 5. Anthropic computer use：截图像素上的鼠标

✅ [Computer use tool](https://docs.anthropic.com/en/docs/agents-and-tools/computer-use)：

> Claude can interact with computer environments through the computer use tool, which provides screenshot capabilities and mouse/keyboard control for autonomous desktop interaction.

当前文档里的工具集是 `{"type": "computer_toolset_20260801"}`，一次声明展开成 `screenshot`、`left_click`、`type`、`zoom` 等成员。调用方在自己控制的环境里执行。Claude 不直接连上那台机器。

动作表（文档原文，节选）：

| 成员 | 输入 | 做什么 |
|---|---|---|
| `screenshot` | `{}` | 截整个显示，返回图像 |
| `zoom` | `region: [x0, y0, x1, y1]` | 该区域的全分辨率图，再缩进通常的截图尺寸 |
| `left_click` | `coordinate` 可选 `[x, y]` | 在该点按左键；省略坐标则点当前光标位置 |
| `mouse_move` | `coordinate: [x, y]` | 移动光标，不点击，例如悬停 |
| `left_click_drag` | `start_coordinate`，`coordinate` | 按下、拖、松开 |
| `cursor_position` | `{}` | 用文本返回当前 `[x, y]` |
| `scroll` / `type` / `key` / `wait` | 见该页表格 | 滚轮、打字、按键、等待 |

✅ 坐标：「Coordinates are in screenshot pixels … origin at the top left。」`zoom` 之后坐标仍然是全屏截图坐标系。应用若把截图缩小后再交回，必须把坐标乘回去。

参考环境是 Docker 里的虚拟显示：Xvfb、Mutter、Tint2，预装 Firefox 等。工具实现把「move mouse」「take screenshot」译成这个虚拟环境里的操作。这是一台给模型用的 Linux 桌面，不是用户的 Chrome 标签。

同页还提到网页任务可以另声明 browser use 工具，两套工具集坐标各自独立。本次只核对了 computer use 这一页对鼠标和截图的描述。

---

## 6. Safari：WebDriver，以及 Apple Events 里的 JavaScript

Safari 不在 §2.1 的协议范围内。✅ [CDP 总览](https://chromedevtools.github.io/devtools-protocol/) 把协议限定在 Chromium、Chrome 和其他 Blink 浏览器。✅ [connectOverCDP](https://playwright.dev/docs/api/class-browsertype#browser-type-connect-over-cdp) 写明 CDP 连接只支持 Chromium 系。没有一份 Apple 文档把「Safari 实现了 CDP」列为能力。能接到 Safari 的官方自动化是 WebDriver，以及 Safari 自己的 Apple Event `do JavaScript`。

### 6.1 WebDriver / `safaridriver`

✅ [Testing with WebDriver in Safari](https://developer.apple.com/documentation/webkit/testing-with-webdriver-in-safari)：

- Safari 与 Safari Technology Preview 各带一个 `safaridriver`。系统 Safari 的可执行文件在 `/usr/bin/safaridriver`。每个 driver 只能启动它对应的那一版 Safari。
- 客户端把命令变成 REST，发到 driver 本地的 HTTP 服务，driver 再转给对应的浏览器实例。
- High Sierra 及以后：开发者支持默认关闭，要跑一次 `safaridriver --enable`。
- 示例用 `webdriver.Safari()`，`find_element`、`click()`、`execute_script(...)`。

✅ [WebDriver（Safari 开发者文档）](https://developer.apple.com/documentation/safari-developer-tools/webdriver.md)：Safari 的 driver 叫 `safaridriver`，协议是 W3C WebDriver，多数 Selenium 库有现成 preset。

🌐 [WebDriver § Element Click](https://www.w3.org/TR/webdriver2/#element-click)：

> The Element Click command scrolls into view the element if it is not already pointer-interactable, and clicks its in-view center point.

元素中心被挡住则 `element click intercepted`；在视口外则 `element not interactable`。远程端会滚动进视口，再按元素类型派发 `mouseOver` / `mouseMove` / `mouseDown` 等。规范没有要求画横幅或假光标。

这是测试驱动：先启用 `safaridriver`，由 driver 把命令送进 Safari。它不提供本仓库那种「先在页面上标出来再点」的步骤。

### 6.2 Apple Events JavaScript：本仓库现在走的词典

本机随应用安装的脚本词典（一方法源，不是网页）：

- ✅ `/Applications/Safari.app/Contents/Resources/Safari.sdef`：命令 `do JavaScript`（code `sfridojs`），「Applies a string of JavaScript code to a document.」参数 `in` 指定 document 或 tab。这就是 `act_darwin.go` 里 `app.doJavaScript(req.js, {in: tab})` 的词典条目。
- ✅ `/Applications/Google Chrome.app/Contents/Resources/scripting.sdef`：命令 `execute`（code `CrSuExJa`），「Execute a piece of javascript.」参数名 `javascript`。这就是 `tab.execute({javascript: req.js})` 的词典条目。

✅ [Apple Events 权限](https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.security.automation.apple-events)：`com.apple.security.automation.apple-events` 表示应用可否提示用户，以取得向其他应用发送 Apple Event 的许可。只给自己或同一 Team ID 的进程发事件时不需要这项。当前 Go 进程通过 `osascript` 发事件，用户看到的是自动化许可，不是辅助功能许可。

Safari 这条路径能改当前标签的 DOM（横幅、描边、`element.click()`）。它不能发 CDP 的 `Input.dispatchMouseEvent`，也不能用 `Overlay.highlightNode`。

---

## 7. macOS `CGEvent`：真的系统光标，以及授权

✅ [CGEvent](https://developer.apple.com/documentation/coregraphics/cgevent)：不透明类型，表示底层硬件事件（Quartz 事件）。普通路径是设备驱动把事件放进 window server 队列；应用也可以把 Quartz 事件投进事件流。文档列出的能力包括创建鼠标事件、读取鼠标位置，以及「Posts a Quartz event into the event stream at a specified location」和投给指定应用。

这次没有打开到一份仍在线的 `CGEventPost` 符号页（`.../cgeventpost` 与 `post(tap:event:)` 均 404）。事件流投递以 `CGEvent` 类页面为准。

授权成本有两份 Apple 材料：

✅ [AXIsProcessTrustedWithOptions](https://developer.apple.com/documentation/applicationservices/1459186-axisprocesstrustedwithoptions)：返回当前进程是不是受信任的辅助功能客户端。`kAXTrustedCheckOptionPrompt` 为真时，若进程未受信任，会异步告知用户；文档举例是启动时提示「accessibility is not enabled for the current process」。返回值不受这次提示影响。

✅ [Developer Forums thread 103992](https://developer.apple.com/forums/thread/103992)，Frameworks Engineer，2018-06，针对 `CGEventPost` 在 10.14 不生效：

> We are changing the default system permissions for using CGEventTaps and CGEvent posting to remove this capability without explicit user permission.

同一回复说明首次投递时期望出现「trying to control the computer using Accessibility feature」的提示，用户到「Security & Privacy → Privacy → Accessibility」里勾选。后续回复确认：以前 Quartz event tap 只对键盘要这项授权，Mojave 起扩展到全部 event tap。同帖引用了归档的 [App Sandbox Design Guide](https://developer.apple.com/library/archive/documentation/Security/Conceptual/AppSandboxDesignGuide/DesigningYourSandbox/DesigningYourSandbox.html)（该页本次抓取超时，只核对了论坛里的引文）：沙箱应用不能用 `CGEventPost` 这类函数向另一个应用投递键盘或鼠标事件。

和现在的差别：`CGEvent` 移动的是系统光标，点击按屏幕坐标命中最前面的窗口，用户会看见真的指针。代价是辅助功能授权（论坛原文），沙箱应用还被设计指南排除。当前实现要的是自动化（Apple Events）许可，只在浏览器标签里执行固定 JS，不接管整台 Mac 的指针。

---

## 8. 对照

| 路线 | 用户当前标签 | 用户看得见点击 | 要的额外东西 |
|---|---|---|---|
| 本仓库 Apple Events + 固定 JS | 是。前台 Chrome 系或 Safari 的当前 tab | 是。横幅、描边、圆点，然后 `element.click()` | 自动化许可。无调试端口 |
| Playwright headed + `highlight` + `mouse` | 否。自己的浏览器窗口 | 高亮在页面上；红点在 Trace Viewer / Inspector | 捆绑浏览器或本机通道。默认 headless |
| Playwright `connectOverCDP` | 只有该 Chrome **已经**开着调试端口 | 接上之后可以用 `highlight` 和 `mouse` | 调试端口。Chrome 136 起默认资料目录不接受 |
| CDP `Overlay` + `Input.dispatchMouseEvent` | 同上，或不受支持的 `chrome.debugger` 扩展 | Overlay 画在检查层上；鼠标事件本身不画光标 | 调试端口或扩展的 `debugger` 权限 |
| browser-use 1.0 | 它自己的浏览器 / CDP 会话 | 点击后橙色角标约 1 秒；截图前去掉 | Python、CDP。模型看的是截图和坐标 |
| browser-use 0.13 序号 | 同上 | 序号在送给模型的截图上，不在活页面上 | 同上 |
| OpenAI computer / Operator / ChatGPT Work | 否。自有或云端浏览器 | 模型靠截图；产品 UI 另说 | 截图坐标循环，或 OpenAI 托管浏览器 |
| ChatGPT 内置浏览器 / Codex | 否。独立 profile。现有标签要官方扩展 | 内置浏览器里操作；文档写了点击和截图 | 桌面应用。CLI / IDE 扩展没有这个浏览器 |
| Anthropic computer use | 否。调用方的环境；参考实现是 Xvfb | 取决于调用方怎么移动光标 | 截图像素坐标。参考实现是另一台 Linux 桌面 |
| Safari WebDriver | driver 转给对应 Safari | 规范只要求滚进视口并点中心，不画标记 | `safaridriver --enable` |
| `CGEvent` | 点的是屏幕最前窗口，不按 DOM | 真的系统指针 | 辅助功能授权。沙箱应用被排除 |

---

## 9. 对本应用的建议

继续用 Apple Events 把固定脚本打进前台标签。Chrome 系是词典里的 `execute javascript`，Safari 是 `do JavaScript`。可见的接管就留在页面里：横幅、描边、圆点，停大约 650ms 再点。这和 Playwright `locator.highlight()`、browser-use 1.0 的角标是同一类「画在页面上给人看」的办法，而且不另开一个产品浏览器。

Playwright headed、Operator、ChatGPT Work、Anthropic 的参考桌面，都是另一只浏览器或另一块屏幕。`connectOverCDP` 和手写 CDP 能发真正的 `dispatchMouseEvent`，也能用 `Overlay.highlightNode` 描节点，但 Chrome 136 起默认资料目录不再接受 `--remote-debugging-port`，接不到用户正在用的那个 Chrome。`chrome.debugger` 不需要那个端口，官方写明不支持面向终端用户的扩展。

这条路仍然进不了 Safari 的 CDP。Safari 只有 WebDriver 和 `do JavaScript`。主文档上的 `querySelectorAll` 也仍然进不了另一棵 shadow tree（🌐 [DOM](https://dom.spec.whatwg.org/#dom-parentnode-queryselectorall) 的 `querySelectorAll` 在当前节点的 root 上做 scope-match；shadow tree 的 root 是 shadow root），也进不了跨源 iframe（那个 iframe 在 CDP 里是另一个 target，Playwright 要 `frameLocator`）。Playwright 的 locator 能穿过 **open** shadow root；closed shadow root 官方写明不支持。`CGEvent` 能移动真光标，但要辅助功能授权，点的是屏幕坐标，不该为了「看见点击」换成它。

---

## 10. 文档索引

| 主题 | URL |
|---|---|
| Playwright `locator.highlight` / `hideHighlight` | https://playwright.dev/docs/api/class-locator#locator-highlight |
| Playwright headed、`slowMo`、Inspector | https://playwright.dev/docs/debug#headed-mode |
| `connectOverCDP` | https://playwright.dev/docs/api/class-browsertype#browser-type-connect-over-cdp |
| `BrowserType.launch` 的 `headless` | https://playwright.dev/docs/api/class-browsertype#browser-type-launch |
| Mouse（含 Trace Viewer 红点） | https://playwright.dev/docs/api/class-mouse |
| Page.mouse / locator.click 使用 page.mouse | https://playwright.dev/docs/api/class-page |
| Tracing | https://playwright.dev/docs/api/class-tracing |
| Trace viewer | https://playwright.dev/docs/trace-viewer-intro |
| Frames | https://playwright.dev/docs/frames |
| Shadow DOM | https://playwright.dev/docs/locators#locate-in-shadow-dom |
| CDP 总览、端口、`chrome.debugger` | https://chromedevtools.github.io/devtools-protocol/ |
| 协议 JSON（Overlay / Input / Target / Page / DOM） | https://github.com/ChromeDevTools/devtools-protocol/blob/master/json/browser_protocol.json |
| Chrome 136 调试端口 | https://developer.chrome.com/blog/remote-debugging-port |
| browser-use 1.0 角标 | https://github.com/browser-use/browser-use/blob/main/browser_use/repl/highlight.py |
| browser-use 1.0 迁移说明 | https://github.com/browser-use/browser-use/blob/main/MIGRATING.md |
| browser-use 0.13 截图序号 | https://github.com/browser-use/browser-use/blob/0.13.14/browser_use/browser/python_highlights.py |
| browser-use 0.13 鼠标 | https://github.com/browser-use/browser-use/blob/0.13.14/browser_use/actor/mouse.py |
| OpenAI computer use | https://platform.openai.com/docs/guides/tools-computer-use |
| Operator | https://openai.com/index/introducing-operator/ |
| ChatGPT / Codex 浏览器 | https://learn.chatgpt.com/docs/browser |
| Anthropic computer use | https://docs.anthropic.com/en/docs/agents-and-tools/computer-use |
| Safari WebDriver | https://developer.apple.com/documentation/webkit/testing-with-webdriver-in-safari |
| WebDriver Element Click | https://www.w3.org/TR/webdriver2/#element-click |
| DOM `querySelectorAll` | https://dom.spec.whatwg.org/#dom-parentnode-queryselectorall |
| Apple Events 权限 | https://developer.apple.com/documentation/bundleresources/entitlements/com.apple.security.automation.apple-events |
| `CGEvent` | https://developer.apple.com/documentation/coregraphics/cgevent |
| `AXIsProcessTrustedWithOptions` | https://developer.apple.com/documentation/applicationservices/1459186-axisprocesstrustedwithoptions |
| `CGEvent` 投递与辅助功能（Apple Frameworks Engineer） | https://developer.apple.com/forums/thread/103992 |

本机脚本词典（无单独网页）：`/Applications/Safari.app/Contents/Resources/Safari.sdef` 的 `do JavaScript`；`/Applications/Google Chrome.app/Contents/Resources/scripting.sdef` 的 `execute`。

---

## 11. 未核对

1. 🔶 `CGEventPost` 的现行符号页打不开（404）。辅助功能要求来自 Developer Forums 上 Frameworks Engineer 的回复，以及该帖对 App Sandbox Design Guide 的引文。指南原页本次抓取超时，没有逐段重读。
2. 🔶 没有逐个提交翻 browser-use 在 0.13 与 1.0 之间是否短期存在过一个 DOM 假光标。已读的 `highlight.py`、`python_highlights.py`、`actor/mouse.py`、`demo_mode.py` 里没有这种光标。
3. 🔶 Anthropic 同页提到的 browser use 工具只记了「与 computer use 坐标各自独立」，没有展开它的成员表。
4. 🔶 ChatGPT 浏览器扩展如何点进已有标签，文档只说「用扩展」，没有公开点击协议。
