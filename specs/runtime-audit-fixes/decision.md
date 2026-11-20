# runtime-audit-fixes 决定

- 级别：L2
- 原话（一字不改）：进行排查一下 当前的系统 内 是否还存在其他的 bug ？
（随后贴出应用内会话对 8 个缺陷的汇总，并写）这块是 agent 跑了 一次项目 出现的 问题 bug 汇总 可以进行分析整理一下 进行这次的修复 工作
- 我理解你要的是：先核对那份汇总里哪些是源码里真有的问题，再把这次能收住的修掉。不要按那份清单原样改。
- 可能理解错的地方：汇总把「尾部两个 0」说成 float64 截断。源码里 id 已是字符串 `agent-%d`。Windows 时钟经常只到 100 纳秒，尾部 0 对得上分辨率，对不上 double 的 ULP。这次不把 id 改成另一种编码。
- 本质：汇总里有的判断看错了现有实现；真正会把错误事实送回模型的，是失败被写成 assistant，以及飞书没开也写进系统提示。
- 怎样算满足：见下方可执行条件。没有模型这类失败不再以 assistant 进历史；飞书没开时系统提示不含飞书那句；同一纳秒再次取 id 不会覆盖已有键。
- 未知：无。`agents.json` 已有 `writeFileAtomic` 和 `saveMu`，缺的是落盘前 Sync。
- 这次不做：不把 id 改成 ULID。不把历史拆成 SQLite。不做 DPAPI，也不把各会话里已复制的 apiKey 抽走。不因为没选模型就拒绝创建（选模型可以发生在创建之后）。不删 `feishu.enabled` 旧字段。不改已经写进历史里的旧 assistant 错误句。
- 现有能力：`internal/agent/manager.go:Agent.ID` 已证实是 string。`manager.go:writeFileAtomic` 已证实先写 `.tmp` 再 rename，`saveMu` 串行写。`manager.go:finish` 已证实失败时追加 role=assistant。`loop_backend.go:loopSystemPrompt` 已证实飞书那句无条件在内。`config.go:migrateFeishu` 已证实旧飞书字段会迁到 `feishuBots`。`CreateAgent` 已证实创建时不写 llmCfg，`TestCreateAgentDoesNotPinGlobalLLM` 把这定成当前行为。
- 旧决定：无。
- 最小充分方案：失败记成 `system` 且以「运行错误：」开头，送给模型前丢掉；界面仍单独显示这句话。系统提示只在有启用的飞书机器人时带飞书那句。id 撞上已有键时加后缀。临时文件 Sync 后再 rename，权限 0600。
- 为什么更小的做法不够：只改提示，失败句仍以助手身份留在历史里，下一轮还是当自己说过。只改落盘 Sync，解决不了错误被当成发言。
- 相关的债：B 局部偿还。凭据多份明文、历史和配置同一文件，留着。
- 风险：碰到会话消息怎么落盘。不改 JSON 字段名，旧文件还能读。这个仓库不是 data-service，继续走 change。

## 怎样算满足（可执行）

1. 命令：go test -count=1 ./internal/agent/ ./internal/loop/ ./internal/llm/ -run 'TestLoopBackendWithoutProviderFails|TestUniqueAgentIDSkipsTaken|TestLoopSystemPromptOmitsFeishuWhenOff|TestWithoutRuntimeErrors'
   点名用例：TestLoopBackendWithoutProviderFails
   期望输出：ok，且历史里没有 role=assistant 且含「没有模型」的消息，有 role=system 且以「运行错误：」开头
   覆盖：失败不写成助手发言
2. 命令：同上
   点名用例：TestLoopSystemPromptOmitsFeishuWhenOff
   期望输出：ok，关闭时提示不含「飞书里的一句」，打开时含这句
   覆盖：飞书提示跟着开关
3. 命令：同上
   点名用例：TestUniqueAgentIDSkipsTaken
   期望输出：ok，已占用的 `agent-1` 不会被再次返回
   覆盖：同刻创建不覆盖
4. 命令：同上
   点名用例：TestWithoutRuntimeErrors
   期望输出：ok，输出不含「运行错误：这个会话还没有模型」，仍含用户那句
   覆盖：送给模型前丢掉运行错误
