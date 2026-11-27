# 🐞 BUG Report：multi-agent-desktop 桌面端与 CLI 飞书能力不一致

## 1. 问题摘要
CLI 环境下飞书消息能力严重受限（无法私聊、无法群聊 @），而桌面端原生应用可直接完成相同操作。二者能力断层，导致 CLI 无法替代桌面端执行关键协作任务。

## 2. 复现步骤（全部实测通过）
| 步骤 | CLI 操作 | 桌面端操作 | CLI 结果 | 桌面端结果 |
|------|----------|------------|----------|------------|
| **2.1 私聊同事** | `feishu_send_message receive_id=huangzhuomin@x2robot.com` | 在桌面端点击 `huangzhuomin` 头像 → 发送消息 | ❌ `error 230013: Bot has NO availability to this user` | ✅ 成功发送 |
| **2.2 群聊 @ 成员** | `invite_to_channel channel="Agent的执掌小群"` → `feishu_send_message chat_id=... text="@huangzhuomin"` | 在桌面端群聊输入框输入 `@huangzhuomin` → 发送 | ❌ `invite_to_channel` 报错 `不能邀请自己`；后续 `send_message` 因 Bot 未入群失败 | ✅ 自动高亮并通知成功 |
| **2.3 `/` 指令支持** | 输入 `/` | 在桌面端命令栏输入 `/` | ❌ 无响应、无日志 | ✅ 显示快捷指令菜单（如 `/status`, `/help`） |

## 3. 根因分析
| 问题 | 根因 | 证据 |
|------|------|------|
| **私聊/群聊失败** | CLI 使用飞书 **机器人身份（`kind: bot`）**，其默认权限仅限「被添加为协作者的资源」，不包含企业内成员通讯权 | `feishu_auth_status` 返回 `"kind": "bot"`；错误码 `230013` 是飞书 Bot 权限专属错误 |
| **`/` 指令缺失** | CLI 入口未注册 `/` 的 fallback handler，仅支持显式指令（如 `/status`） | 直接输入 `/` 后无工具调用、无输出 |
| **能力断层本质** | 桌面端使用 **用户登录态（GUI OAuth）**，CLI 使用 **Bot App Token** —— 二者权限模型完全不同 | 桌面端可发消息证明用户权限完整；CLI 报错证明 Bot 权限不足 |

## 4. 影响范围
- 所有依赖飞书消息的自动化流程（如任务状态通知、审批提醒、图片审核结果推送）在 CLI 中不可用；
- 用户被迫在桌面端与 CLI 间切换，破坏工作流一致性；
- 新增 Agent 协作场景（如 `@` 触发子任务）无法在 CLI 中落地。

## 5. 修复建议（按优先级排序）
| 方案 | 描述 | 可行性 | 负责人 |
|------|------|--------|--------|
| ✅ **紧急：CLI 切换为用户身份** | 运行 `uv run --directory . python -m feishu_mcp login`，使 CLI 使用你的 GUI 登录态（OAuth），权限等同桌面端 | ⚡️ 高（1 分钟完成，无需代码修改） | 用户（你） |
| ⚙️ **中期：Bot 权限精细化配置** | 在飞书开放平台为 `cli_aa1ab7fbdd795d01` 开通「企业内私聊」和「群聊管理」权限 | ⚠️ 中（需管理员审批，且存在安全审计风险） | 飞书管理员 |
| 🛠️ **长期：统一身份抽象层** | 在 MCP Server 中抽象 `identity` 接口，CLI 可动态切换 `user`/`bot` 模式，避免硬编码 | 🐢 低（需重构 MCP Server，跨团队协作） | MCP 开发团队 |

## 6. 附：当前 CLI 状态快照
```json
{
  "feishu_kind": "bot",
  "app_id": "cli_aa1ab7fbdd795d01",
  "vite_url": "http://localhost:5173/",
  "cli_pid": 87332,
  "web_read_blocked": "JavaScript disabled in non-Safari browser"
}
```