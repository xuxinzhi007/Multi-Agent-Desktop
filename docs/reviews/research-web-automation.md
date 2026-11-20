# 研究：重复的网页自动化，模型该在哪一步停手

> 调研日期：2026-11-20
> 范围：同一类页面反复操作（打开日志、选时间范围、读结果）。对照的是本仓库现在的内置浏览器：无障碍控件表、按 ref 点击、`steps` 把已出现的连续动作一次做完、只在最后一步交回页面。
> 资料：Playwright 官方 MCP 文档、Playwright 源码测试与已合并 PR、Anthropic Computer use 文档、Browser Use 自己的文章与系统提示、Stagehand 官方文档。每条下面挂拥有该说法的页面。
>
> **置信度**
> - ✅ 官方文档、一方法源码或已合并 PR
> - 🔶 厂商博客里的测量，文档没有把方法写死

本文只记各家把「推理」和「点击」拆在哪。不是可粘贴的实现。

---

## 0. 现在慢在哪

一次模型往返要等整段生成结束，工具结果回去之后下一句才会开始。`steps` 只解决「控件已经在这张表里」的连续点击。下拉菜单、时间范围这类控件，要点完第一下才会出现，编号在点击之前不存在，批次到这里必须停下来再问模型。

日志页每次还把整段正文交回去。页面文本一长，下一轮预填充就变慢。

---

## 1. 探索和回放是两件事

✅ [Stagehand：Speed optimization](https://docs.stagehand.dev/v4/best-practices/speed-optimization)：一次 `observe()` 列出多个动作，再把每个 `Action` 交给 `act()`。回放用记录下来的方法和参数，不再推理。文档把这写成多步流程的推荐方式：一次规划，后面的执行不再调用模型。

✅ [Stagehand：act](https://docs.stagehand.dev/v4/basics/act)：把「打开筛选、选四星、点应用」写进同一次 `act()` 的自然语言，文档标明不可靠，要求调用方自己拆成单步。单步指令走模型；已经观察到的 `Action` 再交给 `act()` 则是确定性回放。

✅ [Stagehand：Caching actions](https://docs.stagehand.dev/v4/best-practices/caching)：相同指令、页面内容和调用选项再次出现时，服务端直接返回记录的动作，不再消耗模型 token。选择器对不上时才退回完整推理。缓存键含页面 URL；动态 URL 会错过。

✅ [Playwright MCP：Code execution](https://playwright.dev/mcp/tools/code-execution)：点一个按钮用 `browser_click`，多步逻辑用 `browser_run_code_unsafe`。后者是一段拿到 `page` 的函数，一次跑完，返回值序列化进工具结果。官方测试里同一段代码连续点两次按钮，中间没有新的模型往返（[run-code.spec.ts](https://github.com/microsoft/playwright/blob/5790370e/tests/mcp/run-code.spec.ts)）。文档同时写明：这段代码在 Playwright 进程里执行，等价于远程代码执行。

结论：第一次面对不认识的页面，用模型看控件表。同一条路径第二次跑，应直接执行已经记下的角色名或脚本，模型只在失败时回来。

---

## 2. 一次往返里可以排好几下，但要按顺序，失败就停

✅ [Anthropic：Computer use tool](https://platform.claude.com/docs/en/agents-and-tools/tool-use/computer-use-tool)：一次回复里可以有多块 `tool_use`（点击、输入、截图），称为 batch action。执行方按出现顺序跑，不要并行。某一步失败就不再跑后面的；没跑的块也要回结果，否则下一次请求会被拒。模型通常用截图收尾。应用也可以把截图贴在这一批最后一个结果上，省掉模型再要一次画面的往返。

✅ 同一页「Add action delays」：界面需要时间响应时，点击之后再等，示例等待 0.5 秒再截图。官方 demo 里截图前的等待是 2 秒（[computer.py](https://github.com/anthropics/anthropic-quickstarts/blob/main/computer-use-demo/computer_use_demo/tools/computer.py)）。

这和本仓库的 `steps` 是同一层：一批已知动作，最后看一次页面，中途失败就停。它解决不了「下一项还没出现在控件表里」。

---

## 3. 看页面用控件树，画面只在树上看不见时用

✅ [Playwright MCP：Introduction](https://playwright.dev/mcp/introduction)：用无障碍快照，不用视觉模型。模型读快照里的 ref，再作为下一次工具的 target。

✅ [Playwright MCP：Snapshots](https://playwright.dev/mcp/snapshots)：交互工具默认在动作后附一份新快照。ref 只在这一份快照里有效，页面一变就失效。`target` 也可以是 Playwright 选择器，例如 `getByRole('button', { name: 'Submit' })`，已知元素时可以跳过一次快照往返。`--snapshot-mode=none` 让工具响应不再附带快照。`browser_find` 只返回匹配节点和几行上下文，大页面上比整份快照便宜。`depth` 和 `target` 可以只截子树。

✅ [Playwright MCP：Screenshots](https://playwright.dev/mcp/tools/screenshots)：截图用来看，不用来点。点元素用快照。截图留给版式、画布、图表和留证。

✅ [Playwright MCP：Vision mode](https://playwright.dev/mcp/vision-mode)：默认快照。坐标点击只给无障碍树盖不住的画布、地图、图表、没有 ARIA 的控件。要坐标时优先 `browser_snapshot { boxes: true }`，框是视口内的 CSS 像素，不必从截图上估。

✅ [Playwright PR 41923](https://github.com/microsoft/playwright/pull/41923)（已合并）：响应里本来就要丢掉快照时，不再走无障碍树。说明里写，2500 行表格上整树大约 300ms、序列化大约 1MB。

🔶 [Browser Use：Speed Matters](https://browser-use.com/posts/speed-matters)：多数导航靠 DOM 文本；每次截图在他们的实验里给推理加上约 0.8 秒图像编码。长文用单独的 `extract`，只把问句的答案交回，不把两万 token 的页面塞进上下文。历史放在浏览器状态前面，让前缀能命中 KV cache，每步只换掉最新的页面状态。

✅ [Browser Use 系统提示](https://github.com/browser-use/browser-use/blob/933e28c5/browser_use/agent/system_prompts/system_prompt.md)：只列出可见视口里的可交互元素。URL 没变时，新出现的元素打星号，提示上一步造成了这些变化。自动完成要先等建议出现再点，不要立刻回车。`search_page`、`find_elements` 标成即时、不耗模型。`extract` 标明昂贵，信息已经在状态里就不要调。

✅ [Anthropic：同一文档的截图历史](https://platform.claude.com/docs/en/agents-and-tools/tool-use/computer-use-tool)：一张截图大约 1000–1800 输入 token。单次请求超过 20 张图后，每张图的边长限制更严。修剪要成批做，不要每回合删一张，否则前缀每次都变，缓存失效。示例是保留最近三张，每 25 回合修剪一次。

✅ [Claude：Best practices for computer and browser use](https://claude.com/resources/articles/best-practices-for-computer-and-browser-use-with-claude)：截图超过模型的边长或总像素会被内部缩小，点击坐标和画面就对不齐。网页任务从 1280×720 或 1280×800 起。macOS 截图常常是逻辑坐标的 2 倍，要先缩小再送，或把返回坐标减半。说明文字放在图片前面。

---

## 4. 对本仓库这条日志流程，对应关系

| 做法 | 谁在用 | 本仓库现在 |
|---|---|---|
| 第一次看无障碍树，不看截图 | Playwright MCP、Browser Use | 已是控件表；截图只在 snapshot 和 open |
| 已知的连续 ref 一次做完，最后看一次 | Anthropic batch、本仓库 `steps` | 已做。下拉项还没出现时仍要再问模型 |
| 用角色和名字，或一小段页面脚本，自己等到下一项出现 | Playwright 的选择器 target、`browser_run_code_unsafe` | 还没有。编号过期就整页再看 |
| 第一次探索记下动作，以后直接回放 | Stagehand observe + act、缓存 | 还没有。同一条「选最近 24h」每次都重新推理 |
| 长正文按问题抽取，旧观察不要留在下一轮 | Browser Use extract；Anthropic 成批删截图 | 还没有。日志正文整段回到模型 |

重复测试的最短路径是第三行加第四行：模型只在第一条路径上读控件表，记下「时间范围」和「最近 24h」这种角色名；下一次由浏览器自己点开、等到菜单、点中、再把一小段结果交回。模型在失败或页面结构变了时才重新看。
