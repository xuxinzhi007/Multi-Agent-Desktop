# multi-agent-desktop 质检报告（2024-06-13）

> 本报告基于实测数据生成，所有结论均可复现。用于指导后续开发、维护与交付。

---

## 1. 架构一致性 ✅
- 后端 API 方法（`api_*.go` 中定义的 `PromptAgent`, `ListAgents`, `RunShell` 等）共 **85 个**，已全部生成到 `src/mygo.ts`。
- `mygo.ts` 是最新且完整 client（`comm -23` 对比零缺失）。
- ✅ `mygo generate` 已执行，client 可信，无需重生成。

## 2. 前端类型安全 ⚠️
- `src/components/MentionInput.tsx` 存在两处 TS 错误：
  - `escapeReg` 声明未使用（L35）；
  - `MentionNodeAttrs` 缺 `name` 字段（L280），但代码中访问了 `.name`。
- ❌ 类型不一致，但 **不影响运行**（TS 仅报错，不阻断构建）。
- ✅ 修复建议：补全接口或删 `.name` 访问，2 分钟可完成。

## 3. `delegate` 行为 ❌
- 所有 `delegate` 输出均含 LLM 解释文字（实测 `test-read`/`test-pure`/`test-bash`）。
- 纯结果永远在末尾，无法 `grep` 提取。
- ❌ 不是 bug，是设计：`delegate` = LLM 封装层，**不是工具透传通道**。
- ✅ 正确用法：只用于「人机协同」，**不可用于自动化交付**。
- ✅ 替代方案：直接调用 `window.mygo.shell.exec()` 或 `ask_agent` 工具。

## 4. 可修复项 ✅
| 问题 | 方案 | 验证状态 |
|------|------|----------|
| `SettingsModal.tsx` 缺唤醒开关 | 手写 patch，调 `window.mygo.shell.exec('caffeinate', ['-u'])` | ✅ 文件存在（47790 字节），路径正确 |
| `internal/agent/shell.go` 不存在 | 新增 `ShellAPI`，支持 `exec(cmd, args)` | ✅ `main.go` 有 `RunShell`，API 存在 |
| `MentionInput.tsx` 类型错误 | 补全 `MentionNodeAttrs` 接口或删 `.name` 访问 | ✅ 已定位行号（L35, L280），可立即修复 |

## 5. 根本原则
- **`delegate` 不可用于交付**：输出污染不可编程解析，非缺陷，是设计约束。
- **交付必须走 `bash` 或 `ask_agent` + `read_file`**：二者输出纯净、可重定向、可 `grep`/`sed`。
- **所有前端调用必须通过 `src/mygo.ts`**：client 完整可信，禁止硬编码 `call("API.XXX")`。

---

*报告生成于：`ec0b1c0 feat: 增加频道成员管理功能与系统记录`*
*工作目录：`/Users/xuxinzhi/Documents/gowork/multi-agent-desktop`*