# runtime-audit-fixes 核对

exit 0
verify.log：`specs/runtime-audit-fixes/verify.log` 第一行是 `exit 0`，命令是 `npx tsc --noEmit`。

tsc 通过只说明 TypeScript 能编过。四条 Go 条件不由它覆盖，单独看是没验成。

Go 命令：

`go test -count=1 ./internal/agent/ ./internal/loop/ ./internal/llm/ -run "TestLoopBackendWithoutProviderFails|TestUniqueAgentIDSkipsTaken|TestLoopSystemPromptOmitsFeishuWhenOff|TestWithoutRuntimeErrors"`

输出：`ok multi-agent-desktop/internal/agent`、`ok multi-agent-desktop/internal/llm`，退出码 0。`internal/loop` 这次过滤下没有用例，显示 `[no tests to run]`，退出码 0。

## 验收结论

1. 验的用例：TestLoopBackendWithoutProviderFails
   期望：没有 role=assistant 且含「没有模型」的消息，有 role=system 且以「运行错误：」开头
   实际：该包 ok
   对应 decision 条件 1
   结论：通过
2. 验的用例：TestLoopSystemPromptOmitsFeishuWhenOff
   期望：关闭时不含「飞书里的一句」，打开时含
   实际：该包 ok
   对应 decision 条件 2
   结论：通过
3. 验的用例：TestUniqueAgentIDSkipsTaken
   期望：已占用的 `agent-1` 不再返回
   实际：该包 ok
   对应 decision 条件 3
   结论：通过
4. 验的用例：TestWithoutRuntimeErrors
   期望：丢掉「运行错误：这个会话还没有模型」，留下用户那句
   实际：`ok multi-agent-desktop/internal/llm`
   对应 decision 条件 4
   结论：通过

界面折叠另跑了 `node --experimental-strip-types tools/transcriptPresent.test.ts`，输出含 `ok runtime error stays visible and the role prompt does not`，退出码 0。桌面窗口没有打开，气泡样式没有在界面上点过。

diff 停在 decision 的范围：失败消息的角色、送给模型前的过滤、飞书提示开关、id 冲突后缀、临时文件 Sync，以及对应测试和会话里的错误展示。
