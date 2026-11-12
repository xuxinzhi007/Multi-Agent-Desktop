# 会话级配置 + 知识库 + 群聊 — 核对

## verify 结果
```
exit 0
cmd go vet -p 1 ./...
cmd go build -p 1 -o /tmp/mad-build2 ./
cmd npx tsc --noEmit
cmd go test -p 1 ./internal/... ./
cmd go test -race -p 1 ./internal/... ./
```

```
ok   multi-agent-desktop/internal/agent
ok   multi-agent-desktop/internal/channel
ok   multi-agent-desktop/internal/knowledge
ok   multi-agent-desktop/internal/llm
```

## 验收条件核对

| 条件 | 状态 | 验证方式 |
|------|------|----------|
| 会话内改工作目录 | ✅ | `Agent.WorkDir` + `Sidecar.SetAgentWorkDir` + AgentView 📁 chip；`TestEffectiveConfigFallback` |
| 会话内改模型 | ✅ | `Agent.LLMCfg` + `Sidecar.SetAgentLLM` + AgentView ✦ chip |
| 模型列表在会话内加载 | ✅ | `Sidecar.ListModelsFor`（mock/baseURL 短路）；`TestListModelsEndpoint` |
| 设置去掉「悬浮窗口」 | ✅ | `SettingsModal.tsx` 删除 `<Field label="窗口">` 与 `alwaysOnTop` state |
| 设置去掉「工作目录」 | ✅ | 删除 `<Field label="工作目录">`、`dir` state、`onWorkDir` prop |
| 知识库 | ✅ | `internal/knowledge` JSONL 存储；`KnowledgePanel.tsx` 作为检查器第 4 个 Tab |
| 知识注入 system prompt | ✅ | `Manager.SetKnowledge(KnowledgeProvider)`；`TestKnowledgeInjection` |
| 群聊可创建/可发消息 | ✅ | `internal/channel` + `ChannelView.tsx` + Sidebar「频道」分组 + `channel.*` 事件 |
| 群聊持久化 | ✅ | `channels.json`；`TestPersistenceRoundTrip` |
| 删 agent 清理频道成员 | ✅ | `Sidecar.DeleteAgent` → `channels.RemoveMember`；`TestRemoveMember` |
| 会话配置持久化往返 | ✅ | `TestSaveLoadRoundTripSessionConfig`（守住 4 处白名单） |
| 死代码删除 | ✅ | `internal/events/` 已删，`grep` 零引用 |

## diff 范围
- `internal/agent/manager.go`：`WorkDir`/`LLMCfg` 字段、`Effective*`、`SetAgent*`、`AppendSystemMessage`、知识注入
- `internal/channel/channel.go`：持久化、`Broadcast` 返回值、`RemoveMember`
- `internal/knowledge/knowledge.go`（新）：JSONL store + Render
- `main.go`：`AgentView` 新字段、会话/频道/知识 Sidecar 方法、新事件、`emitToWindow` 拆成 `toXxx`
- `src/components/{AgentView,Sidebar,CreateAgentModal,SettingsModal,Inspector}.tsx`
- `src/components/{ChannelView,KnowledgePanel}.tsx`（新）
- `src/{App.tsx,lib/useSidecar.ts,lib/presets.ts}`
- `src/mygo.ts`（生成）
- `docs/{design,architecture}.md`
- 测试：`internal/{agent,channel,knowledge,llm}/*_test.go`

## 未覆盖条件（已在 decision.md 声明）
- 频道不做「讨论收敛」编排，只是广播触发
- `LLMCfg.APIKey` 明文落盘（`// TODO(secrets)`）
- 前端无自动化测试（verify 只跑 `tsc --noEmit`）

## 手测清单
1. 新建 agent 时填工作目录 → 头显示该目录；另建一个 agent 仍显示全局默认
2. 头部 ✦ chip 选 openai → 填 baseUrl/model → 点「列表」→ 选模型 → 应用
3. 设置弹窗只剩「外观 / 默认模型提供方 / Base URL / 模型名 / API Key」
4. 检查器「知识」Tab 新增一条全局笔记 → 新建 agent → 该 agent 首条 system 消息含该笔记
5. Sidebar「频道 +」→ 命名 + 勾两个 agent → 发消息 → 两个 agent 都开始运行，消息出现在频道里
6. 重启 App → 会话配置、频道消息、知识库都在

## 测试覆盖对照（TC-001 ~ TC-029）

| 用例 | 测试函数 | 包 |
| :--- | :--- | :--- |
| TC-001/003/004 | `TestSetAgentWorkDir`（表驱动） | `internal/agent` |
| TC-002/006 | `TestSessionConfigPersistenceRoundTrip`、`TestLoadLegacyAgentsJSON`、`TestSaveLoadRoundTripSessionConfig` | `internal/agent` |
| TC-005 | `TestSetAgentWorkDirMissingAgent` | `internal/agent` |
| TC-007~009 | `TestSessionLLMConfig`（表驱动）、`TestSetAgentLLMMissingAgent`、`TestSetAgentLLM` | `internal/agent` / `main` |
| TC-010 | `TestSessionLLMMidRunSnapshot`（`-race`） | `internal/agent` |
| TC-011~013 | `TestListModelsForShortCircuit`、`TestListModelsEndpointNormalization`、`TestListModelsUpstreamError` | `main` / `internal/llm` |
| TC-014 | 前端无测试框架，由 `npx tsc --noEmit` + 本文件手测清单第 3 条承载 | — |
| TC-015~018 | `TestUpsertOverwriteByID`、`TestUpsertOverwriteSurvivesReopen`、`TestQueryFilters`、`TestDeleteRewriteIntegrity`、`TestRender` | `internal/knowledge` |
| TC-019~021 | `TestSystemPromptFor`（表驱动）、`TestCreateInjectsKnowledge` | `internal/agent` |
| TC-022 | `TestAttachNoteToAgent`、`TestAppendSystemMessage` | `main` / `internal/agent` |
| TC-023~025 | `TestBroadcastExcludesSenderAndKeepsOrder`、`TestBroadcastMissingChannel` | `internal/channel` |
| TC-026 | `TestSendToChannelPartialFailureKeepsOthers` | `main` |
| TC-027 | `TestPersistenceRoundTrip`、`TestSaveWritesValidJSONWithoutTempFile` | `internal/channel` |
| TC-028 | `TestDeleteAgentRemovesChannelMember`、`TestRemoveMember` | `main` / `internal/channel` |
| TC-029 | `TestSendToChannelDoesNotDuplicateChannel` | `main` |

### 测试基础设施说明
- Sidecar 级测试用 `&mygo.Window{}` 作事件 sink：零值未销毁、无 native 句柄，`Emit` 是安全 no-op。
  **注意** `Emit(nil, ...)` 会因 `w.destroyed.Load()` 解引用 nil 而 panic，因此不可用真实 nil。
- TC-026 的"单个接收者失败"用"频道成员指向已删除 agent"确定性构造，而非抢时间灌满 32 缓冲队列
  （worker 持续消费，灌满本身有竞态）。
- 异步落盘（`go m.Save()`）统一走 `goSave` + `saveWG`，测试用 `waitSaves` 等待，禁止 `time.Sleep` 竞速。

## 测试期间发现并修复的生产缺陷

1. **持久化文件非原子写**（`internal/agent/manager.go`、`internal/channel/channel.go`）
   - 现象：`Create` / `SetAgent*` / `Delete` / `AddMessage` 都会 `go m.Save()`，而 `Save` 用
     `os.WriteFile`（truncate 后写入）。并发 `Save` 互相截断，`Load` 可能读到
     `invalid character '"' after top-level value`。
   - 修复：`saveMu` 串行化 + 写 `*.tmp` 后 `os.Rename` 原子替换；`Load` 与 `Save` 互斥；
     `store` 字段读写回归 `m.mu`。
2. **`toAgentView` 无锁读 `Agent.Messages`**（`main.go`）
   - 现象：`ListAgents` / `agent.config` 事件在 worker append 历史时会触发 data race
     （`go test -race ./` 复现）。
   - 修复：导出 `Agent.Snapshot()`（锁内拷贝），`toAgentView` 改走它。
