# profile-learn-source 决定

- 级别：L2
- 原话（一字不改）：顺手查出两个真问题
① 我的 role 被你的消息覆盖了

上一轮 list_agents 里我还是「Go 专家，关注 API 设计、并发安全与查询效率」，这一轮变成了 「奇怪你刚刚还创建了一个agent 吗 ？」——你刚发的那句话被原样写进了我的简介字段。已经用 set_profile 改回来了。

对应系统提示里那句「最近几轮如果明显集中在同一块领域，简介会自己收成这块」，那个自动归纳简介的逻辑这次抓错了源，把用户输入当成了 role。

② learnText 里有不存在的内容

摘要里写着 autotest 项目有 cmd/serve/main.go（约 16KB，"新增的本地测试服务器，经多次编辑定型"）。但实际上：

我从没建过 cmd/serve/main.go，只写过 cmd/gen/main.go
autotest 整个目录上一轮已删，现在工作目录只剩 9 项原始文件（live2d、林欣悦相关、api_aa1_cn 抓取结果），没有 autotest
这份摘要有幻觉，而它会被当作「已确认事实」注入我后续每一轮的上下文。如果你后面发现我提到某个不存在的文件，根因在这儿。

另外 learnAt = 81 不是时间戳（换算出来是 1970 年），应该是消息条数计数器，和摘要里说的「前 61 条」是同一类东西但数值对不上——猜测 81 是学习时的总消息数，61 是被压掉的部分。

要不要我把 后端的学习 删掉？它是一次性的压缩器，产物已经落到我的 learnText 里了，删掉不影响历史；但下次上下文再涨满，应用还会重新生成一个同名的。
这块 agent 发现的 bug 应该是需要进行修复的
- 我理解你要的是：简介不要再被用户刚说的那句话盖掉；学习摘要不要再把没出现过的文件写进后续每一轮；learnAt 按消息条数说明白。学习助手本身留着。
- 可能理解错的地方：系统提示那句「简介会自己收成这块」说的是领域词匹配（evolveProfile），它只会写成「后端开发，关注 API…」这种固定句，写不出「奇怪你刚刚还创建了一个agent 吗 ？」。原样覆盖来自一轮结束时把用户输入写进简介（settle → noteBio）。两处都留着，只改写错源的那条。不把已有 agents.json 里的简介或摘要清掉。
- 本质：未钉住的简介被当成「上一句用户的话」，学习摘要被模型补全后又整段当作已学过的事实注入。
- 怎样算满足：见下方可执行条件。用户原话不再进入 role 或 proposal；摘要里的路径必须在被学习的消息里出现过，否则那一句不进 learnText，注入前再滤一次；注入说明写明 learnAt 是已覆盖的消息条数，不是时间。
- 未知：无。learnAt=81 按代码是已折叠进摘要的消息下标（从 0 累加），不是总条数，也不是时间；摘要里的「前 61 条」是模型自己写的，和计数器不是同一个数。
- 这次不做：不删学习子会话，不删 studyHistory。不改 learnAt 的字段名。不清用户数据目录里已经落盘的 learnText（注入时会滤掉对不上的路径）。不改领域词匹配那条自动收简介。
- 现有能力：`internal/agent/manager.go:settle` 已证实在回合成功时调用 `noteBio(req.Text)`，未钉住时 `noteBio` 把这段文字写成 Role。`internal/agent/evolve.go:evolveProfile` 已证实只写专业方向表里的固定简介。`internal/agent/learn.go:studyWith` 已证实 LearnAt 写成已消费的消息下标，LearnText 原样收模型输出。`internal/agent/loop_backend.go` 已证实用「已学习前 %d 条」把 LearnText 接到系统提示前。
- 旧决定：无。
- 最小充分方案：settle 不再把用户输入交给 noteBio。学习提示禁止补写片段里没有的路径、大小和条数。收下摘要前，以及注入前，丢掉含有「原文里没出现过的路径」的句子。注入语改成按条数说明，并写明摘要不是文件清单，和后文或工作目录冲突时以后者为准。
- 为什么更小的做法不够：只改提示，settle 仍会把原话写进简介。只改注入说明，模型仍会把 cmd/serve/main.go 写进 learnText，下一轮还是当事实读。只删学习助手，父会话上的 learnText 还在，下次满了还会再建。
- 相关的债：B 局部偿还。只停掉「用户原话当简介」和「摘要路径对不上原文仍注入」。领域自动改简介、提案按钮的数据结构留着。
- 风险：碰到会话状态的写入规则（简介、learnText）。这个仓库不是 data-service，不改 JSON 字段，不迁移旧文件，继续走 change。

## 怎样算满足（可执行）

1. 命令：go test -count=1 ./internal/agent/ -run 'TestSettleDoesNotCopyUserText|TestStudyDropsPathMissingFromMessages|TestGroundLearnKeepsPathPresentInMessages'
   点名用例：TestSettleDoesNotCopyUserText
   期望输出：ok，role 仍是「Go 专家，关注 API 设计、并发安全与查询效率」，proposal 为空
   覆盖：用户原话不写进简介
2. 命令：同上
   点名用例：TestStudyDropsPathMissingFromMessages
   期望输出：ok，learnText 不含 cmd/serve/main.go，仍保留片段里有依据的句子
   覆盖：编出来的路径不进摘要
3. 命令：同上
   点名用例：TestGroundLearnKeepsPathPresentInMessages
   期望输出：ok，输出仍含 cmd/gen/main.go
   覆盖：原文里有的路径保留；注入用的是同一道过滤
