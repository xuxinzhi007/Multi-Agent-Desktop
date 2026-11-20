# profile-learn-source 核对

exit 0
verify.log：`specs/profile-learn-source/verify.log` 第一行是 `exit 0`，命令是 `npx tsc --noEmit`。

这条命令只查 TypeScript，没有跑到简介和学习摘要的 Go 逻辑。拿它单独说这三条，是没验成。

下面三条用 decision 里的命令验过：

`go test -count=1 ./internal/agent/ -run "TestSettleDoesNotCopyUserText|TestStudyDropsPathMissingFromMessages|TestGroundLearnKeepsPathPresentInMessages"`

输出：`ok multi-agent-desktop/internal/agent`，退出码 0。

## 验收结论

1. 验的用例：TestSettleDoesNotCopyUserText
   期望：role 仍是「Go 专家，关注 API 设计、并发安全与查询效率」，proposal 为空
   实际：该包 ok，用例没有失败
   对应 decision 条件 1
   结论：通过
2. 验的用例：TestStudyDropsPathMissingFromMessages
   期望：learnText 不含 cmd/serve/main.go，仍含 cmd/gen/main.go
   实际：该包 ok，用例没有失败
   对应 decision 条件 2
   结论：通过
3. 验的用例：TestGroundLearnKeepsPathPresentInMessages
   期望：输出仍含 cmd/gen/main.go，不含 cmd/serve/main.go，并写明已覆盖前 81 条、不是时间
   实际：该包 ok，用例没有失败
   对应 decision 条件 3
   结论：通过

diff 停在 decision 写的范围：`internal/agent/manager.go` 的 settle、`internal/agent/learn.go` 的摘要过滤、`internal/agent/loop_backend.go` 的注入，以及这两个包的测试。工作区里其它已改文件不是这次动的。
