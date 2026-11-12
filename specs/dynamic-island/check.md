# 悬浮窗灵动岛改造 — 核对

## verify 结果
```
exit 0
cmd npx tsc --noEmit
```

## 验收条件核对

| 条件 | 状态 | 验证方式 |
|------|------|----------|
| hover 状态点显示 tooltip | ✅ | mini.tsx 加了 onMouseEnter/onMouseLeave，hovered state 控制 tooltip 显示 |
| 运行中显示任务摘要 | ✅ | MiniAgentStatus 加 Task 字段，extractTask 从最后一条 user 消息提取前 40 字 |
| 错误红色脉冲 + 角标 | ✅ | errorPulse 动画（红色 box-shadow 脉冲）+ 右上角红色 ! 角标 |
| 展开面板动画 | ✅ | max-height 0.3s cubic-bezier + opacity 0.25s transition |
| 灵动岛视觉 | ✅ | 黑色胶囊 + backdrop-filter blur(20px) saturate(180%) + 圆角 24px |

## diff 范围
- main.go: MiniAgentStatus 加 Task 字段 + extractTask 函数
- src/mini.tsx: 重写为灵动岛设计
- src/mygo.ts: 自动生成（task 字段）

未超出 decision.md 范围。

## 未覆盖条件
- 任务摘要提取规则：当前从 user 消息提取，未从 tool_use 事件提取（decision.md 已声明未知）
- 多 agent 同时显示：当前最多显示 6 个状态点（decision.md 已声明未知）
