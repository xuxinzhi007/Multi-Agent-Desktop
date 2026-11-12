# 群聊体系设计方案

## 当前问题

### 1. @ 提及逻辑混乱
- 用户 @ 某个 agent，其他 agent 也会回复
- 没有区分「@ 我」和「@ 别人」
- 群聊消息应该是广播，但 @ 应该是定向

### 2. 消息路由不清晰
- 用户发消息 → 所有成员收到（正确）
- 但成员回复时，没有判断是否需要回复
- 导致所有成员都回复，群聊变成噪音

### 3. 缺乏消息过滤机制
- 没有「是否需要我回复」的判断
- 没有「@ 提及」的解析
- 没有「线程/话题」的概念

## 设计方案

### 核心原则

1. **@ 优先**：被 @ 的 agent 必须回复
2. **广播默认**：没被 @ 的 agent 可以选择性回复
3. **智能过滤**：agent 自己判断是否相关
4. **话题隔离**：不同话题不互相干扰

### 消息类型

```
用户消息
├── @agentA 具体问题     → 只有 agentA 回复
├── @agentA @agentB 讨论  → agentA 和 agentB 回复
└── 普通消息              → 所有成员可选择性回复

agent 消息
├── @agentB 请帮忙        → agentB 回复
├── @all 通知             → 所有成员回复
└── 普通回复              → 不触发其他 agent
```

### 路由规则

| 场景 | 触发者 | 接收者 | 行为 |
|------|--------|--------|------|
| 用户 @agentA | 用户 | agentA | 必须回复 |
| 用户 @agentA @agentB | 用户 | agentA, agentB | 两者都回复 |
| 用户普通消息 | 用户 | 所有成员 | 可选择性回复 |
| agentA @agentB | agentA | agentB | agentB 回复 |
| agentA 普通回复 | agentA | 不触发 | 仅记录 |

### 实现方案

#### 1. 消息解析（前端）

```typescript
// 解析 @提及
function parseMentions(text: string): string[] {
  const regex = /@(\S+)/g
  const mentions: string[] = []
  let match
  while ((match = regex.exec(text)) !== null) {
    mentions.push(match[1])
  }
  return mentions
}

// 判断是否需要回复
function shouldReply(
  text: string,
  myName: string,
  myRole: string
): boolean {
  const mentions = parseMentions(text)
  
  // 被 @ 必须回复
  if (mentions.includes(myName)) return true
  
  // @all 必须回复
  if (mentions.includes('all')) return true
  
  // 普通消息：根据角色判断相关性（可选）
  return isRelevantToRole(text, myRole)
}
```

#### 2. 消息路由（后端）

```go
// SendToChannel 改进
func (s *Sidecar) SendToChannel(channelId, fromId, fromName, text string) error {
    msg, recipients, err := s.channels.Broadcast(channelId, fromId, fromName, text)
    if err != nil {
        return err
    }
    
    ChannelMessage.Emit(s.win, *msg)
    
    // 解析 @提及
    mentions := parseMentions(text)
    
    for _, agentId := range recipients {
        agent := s.mgr.Get(agentId)
        if agent == nil {
            continue
        }
        
        // 判断是否需要回复
        shouldReply := false
        
        // 被 @ 必须回复
        for _, mention := range mentions {
            if mention == agent.Name || mention == agentId || mention == "all" {
                shouldReply = true
                break
            }
        }
        
        // 没被 @：根据角色相关性判断（可选）
        if !shouldReply && len(mentions) == 0 {
            shouldReply = isRelevantToRole(text, agent.Role)
        }
        
        if shouldReply {
            prompt := fmt.Sprintf("[channel:%s] %s: %s", channelId, fromName, text)
            s.mgr.Prompt(context.Background(), agent.PromptRequest{
                AgentID: agentId,
                Text:    prompt,
            })
        }
    }
    
    return nil
}
```

#### 3. 角色相关性判断

```go
// isRelevantToRole 判断消息是否与角色相关
func isRelevantToRole(text, role string) bool {
    // 简单关键词匹配
    keywords := map[string][]string{
        "后端": {"api", "数据库", "服务", "接口", "sql"},
        "前端": {"ui", "页面", "组件", "样式", "css"},
        "测试": {"测试", "bug", "错误", "验证"},
        "运维": {"部署", "服务器", "日志", "监控"},
    }
    
    textLower := strings.ToLower(text)
    for _, kw := range keywords[role] {
        if strings.Contains(textLower, kw) {
            return true
        }
    }
    
    // 默认：不相关（减少噪音）
    return false
}
```

### 消息流

```
用户: @后端 这个 API 怎么设计？
    │
    ├── 解析 mentions: ["后端"]
    │
    ├── 后端 agent: 被 @ → 必须回复
    │   └── 回复: [channel:xxx] 后端: 建议这样设计...
    │
    ├── 前端 agent: 没被 @ → 判断相关性
    │   └── 关键词 "API" 不相关 → 不回复
    │
    └── 测试 agent: 没被 @ → 判断相关性
        └── 关键词 "API" 不相关 → 不回复

用户: 大家看看这个方案怎么样？
    │
    ├── 解析 mentions: []
    │
    ├── 后端 agent: 普通消息 → 判断相关性
    │   └── 关键词 "方案" 相关 → 回复
    │
    ├── 前端 agent: 普通消息 → 判断相关性
    │   └── 关键词 "方案" 相关 → 回复
    │
    └── 测试 agent: 普通消息 → 判断相关性
        └── 关键词 "方案" 相关 → 回复
```

### UI 改进

#### 1. 消息显示

```
┌─────────────────────────────────────┐
│ # 后端优化群                         │
│ 成员: 后端, 前端, 测试               │
├─────────────────────────────────────┤
│ 你    @后端 这个 API 怎么设计？      │
│                                     │
│ 后端  ┌─────────────────────────┐   │
│       │ 建议用 RESTful 风格...   │   │
│       │ 1. 资源命名用复数        │   │
│       │ 2. 版本号放 URL          │   │
│       └─────────────────────────┘   │
│                                     │
│ 前端  [静音] 不相关，未回复          │
│ 测试  [静音] 不相关，未回复          │
└─────────────────────────────────────┘
```

#### 2. @ 自动补全

输入 `@` 时弹出成员列表：
- 显示成员名称和角色
- 支持多选
- 支持 `@all`

#### 3. 回复状态

- 已回复：显示回复内容
- 未回复：显示 [静音] 或 [思考中]
- 被 @：高亮显示

### 配置选项

```typescript
// 群聊设置
type ChannelSettings = {
  // 回复策略
  replyPolicy: 'mention-only' | 'all' | 'smart'
  
  // 角色相关性过滤
  roleFilter: boolean
  
  // 自动回复延迟（避免瞬间回复）
  replyDelay: number // ms
  
  // 最大同时回复数
  maxConcurrentReplies: number
}
```

### 实现优先级

| 优先级 | 功能 | 说明 |
|--------|------|------|
| P0 | @ 提及过滤 | 修复当前问题 |
| P0 | 角色相关性判断 | 减少噪音 |
| P1 | 回复状态显示 | 提升体验 |
| P1 | @ 自动补全 | 提升体验 |
| P2 | 话题隔离 | 高级功能 |
| P2 | 回复策略配置 | 高级功能 |

### 代码结构

```
internal/channel/
├── channel.go          # 频道基础
├── router.go           # 消息路由（新增）
│   ├── parseMentions   # 解析 @
│   ├── shouldReply     # 判断回复
│   └── isRelevant      # 角色相关性
└── settings.go         # 频道设置（新增）

src/components/
├── ChannelView.tsx     # 群聊视图
├── ChannelSettings.tsx # 设置弹窗（新增）
└── MentionPicker.tsx   # @ 选择器（新增）
```

## 下一步

1. 实现 @ 提及过滤（P0）
2. 实现角色相关性判断（P0）
3. 添加回复状态显示（P1）
4. 测试群聊体验
