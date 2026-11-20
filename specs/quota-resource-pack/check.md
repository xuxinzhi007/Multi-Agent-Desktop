# quota-resource-pack 核对

## verify 结果

```
exit 0
cmd npx tsc --noEmit
```

`specs/quota-resource-pack/verify.log` 的 exit 是 0。这条命令只跑 TypeScript，没有执行额度文案。额度文案的判定在下面两条 Go 测试。

## 验收条件核对

| # | 条件 | 状态 | 证据 |
|---|---|---|---|
| 1 | 订阅用完仍写出资源包剩余与上限 | 通过 | `go test ./internal/llm -count=1 -run "TestQoderIdentityUsesSavedLoginWhenKeyIsStale\|TestQoderQuotaIncludesResourcePackage"` 输出 `ok multi-agent-desktop/internal/llm`。验的用例：`TestQoderQuotaIncludesResourcePackage`。期望 `订阅剩余 0 / 3000 credits；资源包剩余 211 / 11000 credits`，对不上会 `t.Fatalf`。对应 decision 条件 1。 |
| 2 | 只有订阅额度时文案不变 | 通过 | 同一条命令里的 `TestQoderIdentityUsesSavedLoginWhenKeyIsStale`。期望 `剩余 12 / 100 Credits`。对应 decision 条件 2。 |

## diff 范围

改动文件：`internal/llm/qoder_auth.go`、`internal/llm/qoder_test.go`。设置页仍原样渲染 `row.quota`，没有改布局。

## 未覆盖

正在跑的桌面窗口还是旧进程，这次没有重新打开设置页看那一行字。
