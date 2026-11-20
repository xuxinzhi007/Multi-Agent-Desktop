# quota-resource-pack 决定

- 级别：L1
- 原话（一字不改）：现在的 这个额度展示不完整  这里还有资源包呢
- 我理解你要的是：设置里 Qoder 现在只写「剩余 0 / 3000 credits」，用量页上还有一档组织资源包（已用 10789 / 上限 11000，剩余 211），这一档也要出现在同一处额度文案里。
- 可能理解错的地方：资源包是接口里的 `orgResourcePackage`（截图文案「由组织统一购买，供团队成员共享」），不是个人加量包。展示按两档分开写，不把两档加总成一个剩余数。没有资源包的账号仍只写订阅那一行。
- 本质：额度文案漏了还能用的资源包，订阅用完时看起来像没额度了。
- 怎样算满足：
  1. 命令：`go test ./internal/llm -run "TestQoderIdentityUsesSavedLoginWhenKeyIsStale|TestQoderQuotaIncludesResourcePackage"`
     点名用例：`TestQoderQuotaIncludesResourcePackage`
     期望输出：额度文案为 `订阅剩余 0 / 3000 credits；资源包剩余 211 / 11000 credits`
     覆盖：本文件验收条件 1（订阅用完仍写出资源包剩余与上限）
  2. 命令：同上
     点名用例：`TestQoderIdentityUsesSavedLoginWhenKeyIsStale`
     期望输出：`quota = "剩余 12 / 100 Credits"`
     覆盖：本文件验收条件 2（只有订阅额度时文案不变）
- 未知：无。本机 `GET /api/v2/quota/usage` 已返回 `userQuota`（remaining 0 / total 3000）和 `orgResourcePackage`（remaining 211 / cap 11000，无 total）。
- 这次不做：不改设置页布局；不展示刷新时间、已用百分比、加量包到期；不把额度从字符串改成结构化字段。
- 现有能力：`internal/llm/qoder_auth.go:qoderQuotaText` 只读 `userQuota`，已证实。`src/components/SettingsModal.tsx` 原样渲染 `row.quota`，已证实。对照 `pi-provider-qoder-cn` 的 `orgResourcePackage ?? sharedQuota`，上限取 `total ?? cap`，已证实。
- 旧决定：无。无旧决定。
- 最小充分方案：在 `qoderQuotaText` 里同时读订阅、加量包（有才写）、资源包（`orgResourcePackage`，没有则 `sharedQuota`）。多档时写成「订阅剩余 a / b unit；资源包剩余 c / d unit」。只有订阅时保持「剩余 a / b unit」。全部剩余为 0 且 `isQuotaExceeded` 时仍写额度已用完，并带上各档。
- 为什么更小的做法不够：只改前端加不出资源包，数字只来自这一次额度接口。只把两档剩余相加会丢掉「订阅已用完、资源包还剩 211」。
- 相关的债：C 暂缓。额度仍是一条字符串，这次够用。
- 风险：不碰契约、持久化、权限、状态、回滚
