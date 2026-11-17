# thinking-mode 决定

- 级别：L1
- 原话（一字不改）：还有我发现一个问题 好像 这个 并没显示 思考模式 是不是可以进行添加一下 ？ 支持设置 ？
- 我理解你要的是：DeepSeek 的思考模式要能在对话里看见推理，并且这场会话可以自己打开或关掉。
- 可能理解错的地方：开关放在会话底栏、跟模型放在一起，不放进「连接」设置。思考强度固定 high，不做档位。
- 本质：请求没带思考开关，返回的推理也被丢掉了，所以界面上没有思考模式。
- 怎样算满足：见下方可执行条件。deepseek 打开时请求带 thinking.type=enabled，关掉时带 disabled，回复里的 reasoning_content 进到消息的 reasoning 并在气泡里显示。
- 未知：无。空的 thinking 对 deepseek 按打开处理，和厂商默认一致。
- 这次不做：思考强度档位、非 deepseek 厂商、把推理流式逐字打出来。
- 现有能力：`internal/llm/chat.go:Turn` 已证实只解析 content 和 tool_calls。`src/components/AgentView.tsx:ModelPicker` 已证实模型选择在会话里。
- 旧决定：无。
- 最小充分方案：会话配置加 thinking；deepseek 请求带开关；reasoning_content 单独保存并回传；会话底栏加「思考」开关；气泡和进行中的区域显示推理。
- 为什么更小的做法不够：只显示、不给开关，关不掉；只加开关、不解析 reasoning_content，界面仍然是空的。
- 相关的债：C 暂缓。流式推理以后再做。
- 风险：不碰契约、持久化、权限、状态、回滚。reasoning 是消息上的新可选字段，旧会话读得出来。

## 怎样算满足（可执行）

1. 命令：go test -count=1 ./internal/llm/ -run 'TestDeepSeekThinkingRequestAndReasoning|TestDeepSeekThinkingOffDisablesAndDropsReasoning|TestNonDeepSeekOmitsThinking'
   点名用例：TestDeepSeekThinkingRequestAndReasoning
   期望输出：ok，且请求 thinking.type 为 enabled，回复 reasoning 为「先比整数部分」
   覆盖：打开时请求带思考，推理进 Turn.Reasoning
2. 命令：同上
   点名用例：TestDeepSeekThinkingOffDisablesAndDropsReasoning
   期望输出：ok，thinking.type 为 disabled，请求里没有 reasoning_content
   覆盖：关掉思考
3. 命令：同上
   点名用例：TestNonDeepSeekOmitsThinking
   期望输出：ok，请求体没有 thinking
   覆盖：其它厂商不带这个字段
