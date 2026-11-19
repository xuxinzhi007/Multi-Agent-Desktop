# 研究：用户不打开 Chrome「Allow JavaScript from Apple Events」时，内置浏览器能不能操作他正在看的页

> 调研日期：2026-11-19
> 范围：macOS 菜单栏 / 悬浮窗应用（本仓库，Go + React，无 cgo）。用户要读、要点**已经打开、已经登录的那个标签**，并且不会去勾 View > Developer > Allow JavaScript from Apple Events。问题是：嵌一个 WKWebView 是不是正确的产品形态。
> 资料：Chromium 源码与 chromium.org、Chrome 开发者文档、Apple Developer Documentation、WWDC15 讲稿、OpenAI 帮助中心与产品文档。每条下面挂拥有该说法的页面。不引用二手综述。相邻的接管路线见 [research-web-takeover.md](research-web-takeover.md)。
>
> **置信度标注**
> - ✅ **官方文档或一方法源码** —— 下面每条都带原始 URL
> - 🔶 **文档没写死** —— 文末单独列出，不当成事实
>
> 本文只记开关存在哪里、各类浏览器各自能看见什么。**不是**可粘贴的实现，也不改代码。

---

## 0. 先对齐这个问题

现有路径是 Apple Events：Safari `do JavaScript`，Chrome 系 `tab.execute({javascript})`。Chrome 要用户自己打开「Allow JavaScript from Apple Events」。应用发得出 Apple Event，翻不了那个勾。

这次要分开的两件事：

| 用户要的 | 实际指什么 |
|---|---|
| 正在看的页 | 前台 Chrome（或别的浏览器）里已经打开的那个标签，登录态就是那个标签的 cookie |
| 应用自己打开的页 | 应用进程里的 web view 用自己的 URL 载入的文档，cookie 存在应用自己的数据仓库里 |

下面每条路线只回答它落在哪一列。

---

## 1. 外部应用打不开这个勾

### 1.1 官方入口只有菜单

✅ [Information for Third-party Applications on Mac](https://www.chromium.org/developers/applescript/)：

> JavaScript injection via AppleScript will soon be disabled by default in Chrome stable. We’re making this change in order to protect users from injection of unwanted content.
>
> Users who need this functionality can re-enable it by going to the menu bar, View > Developer > Allow JavaScript from Apple Events

同一页给第三方应用的替代是：发布扩展，用 Native Messaging 和自己的进程通信。自动化另提 Telemetry。没有「另一个应用用 API 把勾打开」这一条。

### 1.2 开关是当前 profile 的一个布尔偏好，默认 false

✅ [`chrome/common/pref_names.h`](https://chromium.googlesource.com/chromium/src/+/main/chrome/common/pref_names.h)（`BUILDFLAG(IS_MAC)`）：

> Boolean that indicates whether the browser should allow Javascript injection via Apple Events. Mac only.

键名是 `browser.allow_javascript_apple_events`（`prefs::kAllowJavascriptAppleEvents`）。

✅ [`chrome/browser/ui/browser_ui_prefs.cc`](https://chromium.googlesource.com/chromium/src/+/refs/heads/main/chrome/browser/ui/browser_ui_prefs.cc) 把它注册成布尔偏好，**默认 `false`**，并带 `SYNCABLE_PREF`：

```text
registry->RegisterBooleanPref(
    prefs::kAllowJavascriptAppleEvents, false,
    user_prefs::PrefRegistrySyncable::SYNCABLE_PREF);
```

它挂在 profile 的 `PrefService` 上，不是一个系统级开关。换一个 Chrome profile，就是另一份值。

资料目录在哪，✅ [User Data Directory](https://chromium.googlesource.com/chromium/src/+/refs/heads/main/docs/user_data_dir.md)：每个 profile 是用户数据目录下的一个子目录（常见 `Default`）。macOS 上 Chrome 的默认用户数据目录是 `~/Library/Application Support/Google/Chrome`。运行中的实例以 `chrome://version` 的 Profile Path 为准。profile 偏好文件名的常量是 `Preferences`，✅ [`chrome/common/chrome_constants.h`](https://chromium.googlesource.com/chromium/src/+/refs/heads/main/chrome/common/chrome_constants.h) 的 `kPreferencesFilename`。

这只说明值存在 profile 里。Chromium 没有对外的写接口。下面 1.3 是菜单为什么拒绝别的进程。

### 1.3 菜单处理函数丢掉其他进程的事件

✅ [`chrome/browser/ui/browser_commands_mac.h`](https://chromium.googlesource.com/chromium/src/+/refs/heads/main/chrome/browser/ui/browser_commands_mac.h)：`ToggleJavaScriptFromAppleEventsAllowed` 的注释就是「Toggles the "Allow JavaScript from AppleEvents" setting.」

✅ 同名实现 [`browser_commands_mac.mm`](https://chromium.googlesource.com/chromium/src/+/refs/heads/main/chrome/browser/ui/browser_commands_mac.mm)（main）：

- 没有当前 `CGEvent` 就返回。
- 读 `kCGEventSourceUnixProcessID`。发送方 pid 不是 0 且不是 Chrome 自己的 pid，就丢掉，日志是 `Dropping JS AppleScript toggle, event not from browser`。注释写明：事件来自另一个进程时，不允许它切换这个 secure setting。
- 再读 `kCGEventSourceStateID`。不是 `kCGEventSourceStateHIDSystemState` 也丢掉。注释写明：只接受 HID 系统产生的事件。
- 两关都过了，才对当前 profile 的 `prefs::kAllowJavascriptAppleEvents` 取反。

因此：另一个 macOS 应用用 Apple Event、用自己投递的鼠标事件去点这个菜单，Chromium 的处理函数会直接返回，偏好保持原值。本应用也翻不了这个勾。

### 1.4 `execute javascript` 先读这个偏好

✅ [`tab_applescript.mm`](https://chromium.googlesource.com/chromium/src/+/refs/heads/main/chrome/browser/ui/cocoa/applescript/tab_applescript.mm) 的 `handlesExecuteJavascriptScriptCommand:`：

- `isJavaScriptEnabled` 为假时 `AppleScript::SetError(AppleScript::Error::kJavaScriptUnsupported)`，然后返回。
- 为真时，在 **primary main frame** 上调用 `ExecuteJavaScriptInIsolatedWorld`，world id 是 `ISOLATED_WORLD_ID_APPLESCRIPT`。参数键是 `javascript`。

`isJavaScriptEnabled` 转到 ✅ [`apple_event_util.mm`](https://chromium.googlesource.com/chromium/src/+/refs/heads/main/chrome/browser/ui/cocoa/applescript/apple_event_util.mm) 的 `IsJavaScriptEnabledForProfile`：读该 profile 的 `prefs::kAllowJavascriptAppleEvents`。头文件 ✅ [`apple_event_util.h`](https://chromium.googlesource.com/chromium/src/+/refs/heads/main/chrome/browser/ui/cocoa/applescript/apple_event_util.h) 的注释是「Returns true if Javascript in Apple Events is enabled for `profile`.」

偏好为假时，外部 Apple Event 进得了词典里的 `execute`，脚本不会跑。同一函数里还有一次 `DevToolsWindow::AllowDevToolsFor`，失败则 `kDevToolsUnsupported`。本次没有展开这个函数的判定表。

---

## 2. WKWebView：能读、能点它自己载入的文档

### 2.1 它显示的是应用载入的内容

✅ [WKWebView](https://developer.apple.com/documentation/webkit/wkwebview)：平台原生视图，用来把网页嵌进应用 UI。出现在屏幕上之前，用 `URLRequest` 从服务器载入，或从本地文件、HTML 字符串载入。类从 macOS 10.10 起可用，继承 `NSView`。配置、cookie、偏好走 `WKWebViewConfiguration`。

概览里的示例是应用自己 `webView.load(URLRequest)`。没有「附着到已经开着的 Chrome 标签」的方法。

### 2.2 `evaluateJavaScript` 跑在这个 web view 的某一帧里

✅ [`evaluateJavaScript(_:in:in:completionHandler:)`](https://developer.apple.com/documentation/webkit/wkwebview/evaluatejavascript(_:in:in:completionhandler:))（macOS 11+）：在指定 frame 和 content world 里求值一段 JavaScript。`frame` 传 `nil` 就是主 frame。求值结束在主线程回调，带回结果或错误。

文档写明：对底层网页内容的修改，例如文档的 DOM 结构，**不**受 content world 限制，所有脚本都看得见。也就是说，应用可以在自己这个 web view 里读 DOM、改 DOM。点一个按钮就是这段脚本在**该文档**里做的事。脚本看不见别的进程里的标签。

WWDC15 对这个用途的原话，✅ [Introducing Safari View Controller](https://developer.apple.com/videos/play/wwdc2015/504/)：WKWebView 是一块网页矩形；你把页面载入它，可以对**当前页**求值 JavaScript 并拿回结果。同一段把 WKWebView 放在「应用里你拥有或要定制的内容」，把「用户点链接去做一段浏览」分给 Safari / Safari View Controller。

### 2.3 cookie 存在应用自己的 `WKWebsiteDataStore` 里

✅ [WKWebsiteDataStore](https://developer.apple.com/documentation/webkit/wkwebsitedatastore)：管理这个 web view 的 cookie、磁盘和内存缓存，以及其他网站数据。赋给 `WKWebViewConfiguration.websiteDataStore` 要在创建 web view 之前。

文档列出的三种存储：

| API | 文档怎么说 |
|---|---|
| `default()` | 默认数据仓库，持久写到磁盘。不赋值时 configuration 就用它 |
| `nonPersistent()` | 只放内存，不写磁盘。文档把它当作无痕 |
| `init(forIdentifier:)` | 用调用方提供的 UUID 标识的另一份持久仓库，文档称为 profile browsing |

`httpCookieStore` 是这份仓库自己的 `WKHTTPCookieStore`。三个 API 的参数里都没有 Chrome 的用户数据目录、Chrome 的 profile，或 Safari 的 cookie jar。

✅ [WKProcessPool](https://developer.apple.com/documentation/webkit/wkprocesspool)：不透明令牌，用来让**多个 web view** 跑在同一个内容进程里。iOS 15 / macOS 12 起废弃。现行说明是「Creating and using multiple instances of WKProcessPool no longer has any effect.」它从来不是「接到 Safari 或 Chrome」的接口。

### 2.4 和 Safari 共享登录的那个类，不让应用读页面，也不是这个 Mac 应用能嵌的窗口

✅ [SFSafariViewController](https://developer.apple.com/documentation/safariservices/sfsafariviewcontroller)（iOS 9+、iPadOS、Mac Catalyst；类参考的平台列表里没有 macOS AppKit）：

> Interactions with the web interface aren’t visible to your app, and you can’t access AutoFill data, browsing history, or website data.
>
> If you need to customize the controls of the web interface, or you want to interact with content in that interface, display the content using a WKWebView object instead.

同一页：在 Mac Catalyst 里展示这个控制器，会改为用系统默认浏览器打开。要和页面内容交互，Apple 指回 WKWebView。

✅ 同一场 [WWDC15](https://developer.apple.com/videos/play/wwdc2015/504/) 把「与 Safari 共享 cookie 和其他网站数据」说成 **Safari View Controller** 的行为：用户已经在 Safari 登录，点开这个控制器时可能仍是登录态。讲者同时说明，凭据不交给宿主应用，控制器跑在另一个进程。那是 iOS 的 `SFSafariViewController`，不是 `WKWebView`，也不是 Chrome。

所以：本应用若嵌 WKWebView，能读、能点的是它自己 `load` 的那份文档，登录态是它自己的 `WKWebsiteDataStore`。用户在 Chrome 里已经打开的标签、已经登入的会话，不在这个对象上。要再用那些网站，得在这个 web view 里重新登录。这是第二个浏览器。

本仓库是纯 Go、无 cgo。`WKWebView` 是 WebKit 的 `NSView`，调用面是 Swift / Objective-C。即便另做一个原生视图来嵌它，上一节的资料边界不变。

---

## 3. OpenAI：内置浏览器是另一份 profile；现有标签走扩展

### 3.1 ChatGPT 桌面内置浏览器

✅ [Using the built-in browser in the ChatGPT desktop app](https://help.openai.com/en/articles/20001277-using-the-built-in-browser-in-the-chatgpt-desktop-app)：

- 内置浏览器在 ChatGPT 桌面应用里（macOS 与 Windows），有自己的 browser state。
- FAQ「Does the built-in browser use my Chrome profile?」原文：「No. The built-in browser uses its own browser state. Use the ChatGPT browser extension when a task needs your existing Chrome profile, cookies, signed-in session, open tabs, or Chrome extensions.」
- 可以在这个浏览器里重新登录。cookie 和登录态在桌面应用的 Settings 里清除。ChatGPT 可能在扩展能用现有登录时建议改用 Chrome。
- 选择说明：内置浏览器用于本地开发、公开页面、想留在应用里的任务。现有 Chrome profile、已登录会话、已打开标签、Chrome 扩展，用扩展。

✅ [Browser](https://learn.chatgpt.com/docs/browser) 与此一致：

- 「The built-in browser uses a browser profile that is separate from your regular browser. It doesn’t automatically share your existing tabs or browser session.」
- 地址栏历史也是这份 profile 的。「Results don’t automatically include pages from your regular Chrome profile or other browsers.」
- 现有 Chrome、Edge、Brave、Opera、Vivaldi 标签，或常规浏览器 profile，文档写的是改用浏览器扩展。
- Computer Use：在桌面应用里，ChatGPT Work 或 Codex 操作的是**内置浏览器**（打开、点击、输入、看渲染、截图）。`@Browser` 指这个内置浏览器。Developer mode 的完整 CDP 也是 Settings > Browser 里单独打开的。要用到 Chrome，文档写的是装扩展并用 `@Chrome`。
- 「Browser isn’t available in Codex CLI or the Codex IDE extension.」要用内置浏览器，打开 ChatGPT 桌面应用。
- Codex Cloud：任务被授予浏览器权限时，Codex 用的是**云任务里的浏览器**。
- ChatGPT Work 的电脑「running on a separate computer in the cloud, not the browser on your phone or laptop。」它自己持有 cookie 和登录态。「It doesn’t use your personal browser’s open tabs, browsing history, saved passwords, cookies, extensions, or existing signed-in sessions.」

OpenAI 自己把「应用内的浏览器」和「用户已经开着的 Chrome」写成两条产品，中间用扩展连接。

### 3.2 Atlas 是另一个 Chromium，而且已经停更

✅ [Introducing ChatGPT Atlas](https://openai.com/index/introducing-chatgpt-atlas/) 页首写明这篇介绍的产品已经废弃。正文里 Atlas 是「a new web browser」。第一次打开时，从当前浏览器**导入**书签、已存密码和浏览历史。这是一次导入，正文没有写它附着到正在运行的 Chrome，也没有写它接管 Chrome 已经打开的标签。Agent 可以在 logged-out mode 下限制对已登录站点的访问。

✅ [How we built OWL](https://openai.com/index/building-chatgpt-atlas/)：Atlas 的浏览器引擎是嵌在应用里的 Chromium（OWL）。客户端 API 里的 `Profile` 是「Manage browser state for a specific user profile」，`WebView` 是嵌入并控制 web contents。Agent 的 logged-out 上下文用 Chromium 的 `StoragePartition` 做隔离的内存存储：「Instead of sharing the user’s existing Incognito profile」。会话结束时 cookie 和站点数据丢掉。这里的 profile 是 Atlas 自己的，不是用户的 Google Chrome。

✅ [ChatGPT Atlas retirement](https://help.openai.com/en/articles/20001371-chatgpt-atlas-retirement)：Atlas 已停更。打开的标签不自动迁走。cookie 和已登录会话要在新浏览器里重新登录。帮助中心明确不要把 cookie 或会话文件交给别人。

Atlas 不能当作「内置浏览器就等于用户的 Chrome」的现成例子。它是另一只浏览器；停更之后连它自己的登录态都不迁到别处。

---

## 4. 扩展跑在用户那个 Chrome 里；调试端口接不到默认资料目录

### 4.1 `chrome.scripting` 注入的是一个已经存在的 `tabId`

✅ [chrome.scripting](https://developer.chrome.com/docs/extensions/reference/api/scripting)：在不同上下文里执行脚本。manifest 要 `scripting`，再加上 `host_permissions` 或 `activeTab`。`executeScript` 的目标里唯一必填的是 `tabId`。默认打进该标签的主 frame。`allFrames: true` 打进该标签的全部 frame，也可以用 `frameIds`。

执行结果按 frame 返回，主 frame 在数组第一项。脚本若返回 Promise，Chrome 会等它 settle 再把值交回。

`ExecutionWorld`：

- `ISOLATED`：这个扩展独有的隔离世界。默认。
- `MAIN`：DOM 的主世界，和宿主页面的 JavaScript 共享。

同一页写明：**不能**用 `scripting.executeScript()` 执行一段字符串。字符串形式只存在于 `insertCSS` 的 `css`。脚本来自扩展包里的文件，或扩展上下文里的函数（可带 JSON 可序列化的 `args`）。

✅ [activeTab](https://developer.chrome.com/docs/extensions/develop/concepts/activeTab)：用户**调用**扩展时（点 action、上下文菜单、commands 快捷键、接受 omnibox 建议），扩展临时得到当前活动标签的权限。用户还留在同一页（同 origin 的后续导航，文档里的例子是 `example.com` 到 `example.com/foo`）时权限还在；离开该 origin 或关掉标签就收回。在这段时间里，带了 `scripting` 权限就可以对该 tab 调用 `scripting.executeScript()`。安装时不显示 host 权限警告。`chrome://` 这类受限页拿不到。

这条打进的是用户当时看着的那个标签，cookie 和登录态就是该标签所在 Chrome profile 的。它不新开一个资料目录。代价是每次都要一次用户调用；调用之后只覆盖这一页，不是所有已经打开的标签，也不是以后随便一个标签。

要在没有这次调用的情况下对一批站点持续注入，文档要求的是 `host_permissions`。那会在安装时出现权限警告。脚本仍然跑在装了扩展的那个 Chrome 里，仍然是那份 profile 的标签和 cookie。

### 4.2 `chrome.debugger` 也能附到当前标签，官方不支持终端用户产品

✅ [chrome.debugger](https://developer.chrome.com/docs/extensions/reference/api/debugger)：它是远程调试协议的另一条传输。`attach` 的 `Debuggee` 可以是 `tabId`。附上之后用 `sendCommand` 发 CDP 命令。可用域里包括 `DOM`、`Runtime`、`Input`、`Overlay`、`Page`、`Target`。不需要先给 Chrome 加 `--remote-debugging-port`。

✅ [Chrome DevTools Protocol](https://chromedevtools.github.io/devtools-protocol/) 的「DevTools protocol via Chrome extension」：

> Chrome supports `chrome.debugger` only on a best-effort basis for developer-facing debugging extensions. This includes extensions that add a DevTools panel with developer functionality. Non-debugger use cases and extensions aimed at end users rather than developers are unsupported.

同一页：直接连 CDP 的第三方应用不受支持。CDP 不是 Chrome 的公开支持 API。协议命令和事件可以无通知变化。后端还会限制扩展通过 `chrome.debugger` 碰文件系统或逃出沙箱。

✅ [Remotely hosted code](https://developer.chrome.com/docs/extensions/develop/migrate/remote-hosted-code) 把 `browser.debugger` 和 `scripting` 分开写：debugger 可以请求并执行远程代码，**不是** `browser.scripting` 的替代。使用期间窗口上方有警告条；用户关掉或 dismiss 这条，调试会话就结束。文中的图注是「Debugger Extension started debugging this browser」。

User Scripts API 在同一页：给用户脚本管理器用，用户必须打开开发者模式，也写明不是 `executeScript` 的替代。

### 4.3 调试端口仍然进不了默认资料目录

✅ [Changes to remote debugging switches](https://developer.chrome.com/blog/remote-debugging-port)（2025-03-17）：从 Chrome 136 起，`--remote-debugging-port` 和 `--remote-debugging-pipe` 在调试**默认 Chrome 数据目录**时不再生效。必须同时带 `--user-data-dir`，指向非标准目录。浏览器自动化被建议改用 Chrome for Testing。

非标准目录是另一份用户数据、另一套 cookie 和标签。它不是用户已经开着的那个默认 profile。这篇博客约束的是这两个启动开关，没有写扩展 API 失效。扩展走的是 4.1 和 4.2 的 `tabId`，不走这个端口。

### 4.4 Chromium 自己把扩展加 Native Messaging 写成 Apple Event 的替代

✅ [applescript](https://www.chromium.org/developers/applescript/) 在菜单说明之后写：确实需要往 Chrome 里注入 JavaScript 的应用，可以发布扩展，并用 Native Messaging 和自己的进程通信。

✅ [Native messaging](https://developer.chrome.com/docs/extensions/develop/concepts/native-messaging)：Chrome 把 native messaging host 拉起为单独进程，用 stdin / stdout 传 JSON。macOS 上用户级 host 清单的默认位置是 `~/Library/Application Support/Google/Chrome/NativeMessagingHosts/<name>.json`。扩展要声明 `nativeMessaging`。内容脚本不能直接 `connectNative`，要经扩展的 service worker。

这是「Go 进程 + 装在用户 Chrome 里的扩展」的官方形状。扩展在那个 Chrome 的标签里执行，所以看见的是已经打开的页和那份登录态。用户要安装扩展并授予权限。它不需要勾上 Apple Events 的 JavaScript，也不需要在默认资料目录上开调试端口。

和本仓库现状的一个接口差：现在 Go 侧生成一段 JavaScript 再塞进 `execute`。`chrome.scripting` 不接受任意字符串，只接受扩展里已经打包的文件或函数。结构化参数可以走 `args`。`chrome.debugger` 能送任意 CDP 字符串，包括脚本求值，但 4.2 的支持范围和警告条还在。

---

## 5. 对照

| 路线 | 用户已经打开的标签 | 那份登录态 | 用户还要做什么 |
|---|---|---|---|
| Apple Events `execute javascript` | 是。当前 profile 的标签 | 是。就是这个 Chrome | 自己勾上 Allow JavaScript from Apple Events。应用翻不了 |
| 改 `Preferences` 或合成点击去勾菜单 | 没有支持的写接口。菜单函数丢掉其他进程和非 HID 事件 | — | 本文不把它当一条路 |
| WKWebView `evaluateJavaScript` | 否。只看到自己 `load` 的文档 | 否。`WKWebsiteDataStore` 是应用自己的仓库 | 在这个视图里重新打开页面、重新登录 |
| SFSafariViewController | 否。而且应用读不到页面 | 与 Safari 共享被 WWDC15 说过；现行类参考写明应用访问不了网站数据 | 不是 macOS AppKit 可嵌、可脚本的窗口。Catalyst 上会转到默认浏览器 |
| ChatGPT 桌面内置浏览器 / `@Browser` | 否。独立 profile，不带现有标签和会话 | 否。在里面另登 | 接受第二个浏览器 |
| Atlas | 否。另一只 Chromium。导入的是书签、密码、历史 | 否。停更后 cookie 不迁走 | 产品已停更 |
| ChatGPT Work / Codex Cloud 的浏览器 | 否。云上另一台电脑或云任务里的浏览器 | 否 | 与本机这个标签无关 |
| `chrome.scripting` + `activeTab` 或 host 权限 | 是。`tabId` 就是那个 Chrome 里的标签 | 是。脚本跑在该 profile 里 | 安装扩展。`activeTab` 还要一次用户调用，之后只覆盖这一页 |
| `chrome.debugger` | 是。按 `tabId` 附上，不需要调试端口 | 是 | 官方只 best-effort 支持面向开发者的调试扩展。终端用户产品不受支持。窗口上有警告条，关掉即断 |
| `--remote-debugging-port` 接默认资料目录 | Chrome 136 起这个开关不生效 | 要接上就得换 `--user-data-dir`，那是另一份资料 | 不是当前这套标签 |

---

## 6. 建议

用户明确不勾 Chrome 的那个复选框时，继续把 Apple Events 当主路径，就等于这条读和点不可用。应用自己也没有办法把勾打开：菜单处理函数拒绝其他进程，`execute` 在偏好为假时返回 `kJavaScriptUnsupported`。

不要做内置 WKWebView，并把它说成「用户正在看的那个 Chrome」。它只能读和点应用自己载入的文档，cookie 在应用自己的 `WKWebsiteDataStore`。已打开的标签和已有登录都不在里面。OpenAI 的桌面内置浏览器、Codex 的 `@Browser`、已经停更的 Atlas，官方写的都是另一份 profile；要进用户现有的 Chrome 标签，OpenAI 写的是扩展。

要同时满足「不勾那个复选框」和「就是他已经开着、已经登着的那一页」，官方形状是：用户安装一个跑在该 Chrome 里的扩展，本进程用 Native Messaging 跟它说话。读和点用 `chrome.scripting.executeScript` 打进那个 `tabId`。`activeTab` 只在用户调用扩展之后覆盖当前页；要更宽的站点范围就得 host 权限和安装警告。两种都还是那份 Chrome，不是第二个浏览器。`chrome.scripting` 不执行任意字符串，扩展里要有固定的函数，参数用 `args` 传。这和「模型不送任意 JS」是同一侧。

`chrome.debugger` 也能附到当前标签，而且绕开 Chrome 136 对默认资料目录调试端口的限制。CDP 文档写明它不支持面向终端用户的扩展，使用时还有一条用户可以关掉的调试警告。不要把它做成这条产品的正路。

内置浏览器只适合另一类任务：页面就是应用自己打开的，用户愿意在里面重新登录。那不是本题。

---

## 7. 文档索引

| 主题 | URL |
|---|---|
| Chrome：Apple Event JavaScript 默认关闭，替代是扩展 + Native Messaging | https://www.chromium.org/developers/applescript/ |
| 偏好键 `browser.allow_javascript_apple_events` | https://chromium.googlesource.com/chromium/src/+/main/chrome/common/pref_names.h |
| 注册默认 false | https://chromium.googlesource.com/chromium/src/+/refs/heads/main/chrome/browser/ui/browser_ui_prefs.cc |
| 菜单切换拒绝其他进程 | https://chromium.googlesource.com/chromium/src/+/refs/heads/main/chrome/browser/ui/browser_commands_mac.mm |
| `execute` 在偏好关闭时失败 | https://chromium.googlesource.com/chromium/src/+/refs/heads/main/chrome/browser/ui/cocoa/applescript/tab_applescript.mm |
| 偏好读取 | https://chromium.googlesource.com/chromium/src/+/refs/heads/main/chrome/browser/ui/cocoa/applescript/apple_event_util.mm |
| 用户数据目录 | https://chromium.googlesource.com/chromium/src/+/refs/heads/main/docs/user_data_dir.md |
| profile 偏好文件名常量 | https://chromium.googlesource.com/chromium/src/+/refs/heads/main/chrome/common/chrome_constants.h |
| WKWebView | https://developer.apple.com/documentation/webkit/wkwebview |
| `evaluateJavaScript` | https://developer.apple.com/documentation/webkit/wkwebview/evaluatejavascript(_:in:in:completionhandler:) |
| WKWebsiteDataStore | https://developer.apple.com/documentation/webkit/wkwebsitedatastore |
| WKProcessPool（已废弃） | https://developer.apple.com/documentation/webkit/wkprocesspool |
| SFSafariViewController | https://developer.apple.com/documentation/safariservices/sfsafariviewcontroller |
| WWDC15：WKWebView 与 Safari View Controller | https://developer.apple.com/videos/play/wwdc2015/504/ |
| ChatGPT 桌面内置浏览器与 Chrome profile | https://help.openai.com/en/articles/20001277-using-the-built-in-browser-in-the-chatgpt-desktop-app |
| ChatGPT / Codex 浏览器 | https://learn.chatgpt.com/docs/browser |
| Atlas 介绍（页首已标废弃） | https://openai.com/index/introducing-chatgpt-atlas/ |
| Atlas 的 OWL / profile / StoragePartition | https://openai.com/index/building-chatgpt-atlas/ |
| Atlas 停更 | https://help.openai.com/en/articles/20001371-chatgpt-atlas-retirement |
| `chrome.scripting` | https://developer.chrome.com/docs/extensions/reference/api/scripting |
| `activeTab` | https://developer.chrome.com/docs/extensions/develop/concepts/activeTab |
| `chrome.debugger` | https://developer.chrome.com/docs/extensions/reference/api/debugger |
| CDP：`chrome.debugger` 的支持范围 | https://chromedevtools.github.io/devtools-protocol/ |
| debugger 警告条，以及它不是 scripting 的替代 | https://developer.chrome.com/docs/extensions/develop/migrate/remote-hosted-code |
| Native Messaging | https://developer.chrome.com/docs/extensions/develop/concepts/native-messaging |
| Chrome 136 调试端口 | https://developer.chrome.com/blog/remote-debugging-port |

---

## 8. 未核对

1. 🔶 没有读 Secure Preferences 的 MAC 列表，所以不知道 `browser.allow_javascript_apple_events` 是否在 `Secure Preferences` 里被签名保护。无论在不在，都没有支持的外部写接口；菜单函数会丢掉其他进程的事件。本文不把改 `Preferences` 文件算成一种做法。
2. 🔶 `DevToolsWindow::AllowDevToolsFor` 在 `execute` 里是第二道门。只看到调用点，没有展开它在哪些页面返回假。
3. 🔶 `SYNCABLE_PREF` 只说明注册时带了这个标志。没有追 Chrome Sync 是否真的把这个布尔值同步到别的设备。
4. 🔶 OpenAI 文档只说现有标签走官方扩展，没有公开该扩展用的是 `chrome.scripting` 还是 `chrome.debugger`。
5. 🔶 没有为 Safari 的 Develop 菜单找一份对等的「外部应用能否打开」源码。本题的开关是 Chrome 的。
