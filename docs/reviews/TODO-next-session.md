# 下次接手待办（本次收工状态）

> **2026-11-26 收工更新**：本文件以下内容是 11-25 的历史快照；当前状态、已完成项与优先风险请看 [`status-2026-11-26.md`](status-2026-11-26.md)。网关已内嵌、记忆/调度已接入；剩余最优先项是网关评审的 #1/#2。
>
> 收工时间：本次会话结束。配套交接单：`docs/reviews/backend-delivery-handoff.md`
> **本次没有重启任何服务**，线上进程未受影响。所有验证走 `go test` / e2e（浏览器 mock）。
>
> **2026-11-25 更新（主 Agent）**：§11 第 1 项已拍板并落地——飞书审批改为 owner + 显式白名单（P0-1 已修），ContextLimit 取 8192（测试已对齐），bash 沿用当前权限模型、不单列高危。下面 §0 的拍板项已不再阻塞；仍有效的剩余项是 P0-2 / P0-3、B 的前端收尾、C/D/E。

## 0. 转交时必须强调的两点

1. **先拍板 §11 四件事**，否则 CI 与审批方案开不了工：
   - ContextLimit 取 4096 还是 8192（决定改代码还是改测试）
   - 产品定位收窄 A 还是 B，或「A 为主 + B 三件事」
   - 审批身份策略：飞书卡仅会话 owner 可点，还是显式白名单？群场景是否保留「@ 机器人的人」作批准人
   - bash 是否单列高危：永不 remember、默认不进 auto 自动放行
2. **P0-1 已修（2026-11-25）**：`decide()` 先 `canApprove` 再 `takeAsk`，只认 owner open_id 与白名单；卡片回调解析 `event.operator`；陌生人点卡不会取走待审批卡。原越权路径保留在下方作历史记录。

## 1. P0-1 已核实为真（历史记录，已在 2026-11-25 修复）

`internal/feishu/service.go:798` `func (s *Service) decide(cfg Config, t Turn)`：

```go
card := s.takeAsk(t.ActionID)
allow := t.Action == "once" || t.Action == "keep"
...
s.hooks.Decide(t.ActionID, allow, t.Action == "keep")
```

**全函数只读 `t.ActionID` 与 `t.Action`，`t.Sender` 从未参与判断。**
后果：飞书群里任何能看到这张卡的人都能点「允许」或「本会话之后都允许」，
放开本机文件写入与命令执行。`keep` 那一下还会写进 remembered，长期生效。

改法（交接单 §2）：卡片回调拿 `t.Sender` 比对会话 owner / 白名单，不匹配则拒绝并回权限提示。
**已按「owner + 显式白名单」落地；群场景不要求 @ 机器人。**

## 2. 本次编译状态（已验证）

```
go build ./...      → 干净
npx tsc --noEmit    → 干净
```

## 3. 本次已完成

| 项 | 状态 | 证据 |
|---|---|---|
| A · 频道场景隔离（`channel-scene-isolation`） | **DONE** | 群回合 history 不再拼私聊全量；检查点键 `a.ID__channelID`；豆包 backend 同改 |
| A 的新测试 | 通过，且**反证过**能抓 bug | `TestChannelTurnPromptExcludesPrivateHistory`、`TestCheckpointKeySeparatesScenes`（逐项回退后变红，已恢复） |
| B · 后端成员/角色/邀请 | **代码完成，测试通过** | `Channel.Owner/Admins`；`AddMember`/`KickMember`（判定在 channel 包）；`InviteToChannel`/`KickFromChannel` RPC；`invite_to_channel` agent 工具；`internal/channel` 全包 0.66s 绿 |
| B 的绑定生成 | 完成 | `src/mygo.ts` 已含 `owner`/`admins`/`inviteToChannel`/`kickFromChannel` |

运行记录：`~/Documents/Obsidian Vault/xxz_obsidian/knowledge/运行记录/multi-agent-desktop/specs/`
下 `channel-scene-isolation`（DONE）、`channel-member-roles`（**仍在 STAGE 2**）。

## 4. 剩余工作

### 4.1 B 项收尾（小，前端热更即可，不需重启）

- `src/components/ChannelView.tsx:659-665` 邀请按钮仍是 `disabled`，
  title 还写着「需要后端新接口」。**后端已就绪**，接 `API.inviteToChannel(channel.id, a.id, 'user')` 即活。
  组件内已有直接调 API 的先例（`API.setAgentPermission`，同文件 625 行附近），沿用同形状，不必加 prop。
- `ProfileCard` 群主/管理徽章未加（grep `Owner|Admins|群主|管理` 在该文件无命中）。
- 做完跑 `channel-member-roles` 的 `done`，补 check.md + confess。
- 验收缺一条：e2e「成员面板邀人后头像区计数变化」尚未写。

### 4.2 交接单剩余项（均需重启，按 A→B→C 顺序）

- **C** @所有人 防刷屏（最小候选：只动 `api_channel.go:postChannel`，短确认类消息追加限字指令）
- **D** 显式回合状态事件 + `StopChannelTurn`
- **E** `ParseMentions` 名字含空格 @ 不到；飞书入站场景隔离

### 4.3 交接单 §2 审批其余项（P0-1 之外）

P0-2 子会话卡静默消失、P0-3 必然超时、P1-4 remember 粒度、P1-5 审计缺批准人。
**全部依赖 §11 拍板。**

## 5. 已知既存失败（已在 2026-11-25 修复）

`internal/llm` · `TestContextLimitUsesWindowOrProvider` 已通过；`compact_test.go` 断言改为 8192，与 `compact.go` 的本地模型默认窗口一致。

## 6. 发布注意

- A 与 B 后端改动**必须重启才生效**。交接单要求 A+B+C 合并一次重启。
- 用户明确：**功能开发调整完成后再重启验证，不要反复重启，不要影响线上正在使用的服务。**
- 因此下次接手：先把 B 前端收尾 + C 做完，再一次性重启验证。
- 不要为已通过的工作重复跑全量测试（`internal/agent` 单包 164s、e2e 全量 2min）。
  只测新写的代码，全量回归留到重启前那一次。