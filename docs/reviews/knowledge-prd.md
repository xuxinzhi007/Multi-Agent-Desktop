# 知识库功能 PRD

## 背景

当前知识库只有基础 CRUD，需要完善功能方便多 agent 协作。

## 目标

1. **知识分类管理** — 按项目/主题分类
2. **自动注入** — 创建 agent 时自动注入相关知识
3. **手动附加** — 运行时给 agent 附加知识
4. **智能检索** — 根据内容自动匹配相关知识
5. **知识共享** — 群聊中共享知识

## 功能需求

### P0 核心功能

| 功能 | 描述 | 实现 |
|------|------|------|
| 知识分类 | 按标签/项目分类 | Tags + Scope |
| 自动注入 | 创建 agent 时注入相关知识 | 已存在，需完善匹配逻辑 |
| 手动附加 | 运行时给 agent 附加知识 | AttachNoteToAgent |
| 知识搜索 | 全文搜索 + 标签过滤 | Query 已存在，需前端完善 |

### P1 增强功能

| 功能 | 描述 |
|------|------|
| 智能匹配 | 根据 agent 角色自动匹配相关知识 |
| 知识推荐 | 群聊中自动推荐相关知识 |
| 版本历史 | 笔记修改历史 |
| 协作编辑 | 多个 agent 共同维护知识 |

### P2 高级功能

| 功能 | 描述 |
|------|------|
| 知识图谱 | 笔记之间的关联 |
| 自动摘要 | 长文本自动摘要 |
| 知识导入 | 从文件/URL 导入 |

## 技术方案

### 数据模型

```go
// Note 知识笔记
type Note struct {
    ID       string   `json:"id"`
    Title    string   `json:"title"`
    Body     string   `json:"body"`
    Tags     []string `json:"tags"`      // 分类标签
    Scope    Scope    `json:"scope"`     // global / agentID
    Source   string   `json:"source"`    // manual / auto / imported
    Related  []string `json:"related"`   // 关联笔记 ID
    At       int64    `json:"at"`
    UpdatedAt int64   `json:"updatedAt"`
}

// Scope 知识作用域
type Scope string
const (
    ScopeGlobal Scope = "global"  // 所有 agent 可用
    ScopeAgent  Scope = "agent"   // 特定 agent 可用
    ScopeChannel Scope = "channel" // 特定频道可用
)
```

### API 设计

```go
// 基础 CRUD
UpsertNote(n Note) (Note, error)
QueryNotes(q Query) []Note
DeleteNote(id string) bool
GetNote(id string) (Note, bool)

// 高级功能
SearchNotes(text string, limit int) []Note        // 全文搜索
MatchNotesForAgent(agentId string) []Note          // 根据角色匹配
AttachNoteToAgent(noteId, agentId string) error    // 附加到 agent
DetachNoteFromAgent(noteId, agentId string) error  // 从 agent 移除
ListNotesByTag(tag string) []Note                  // 按标签列出

// 知识注入
GetNotesForSystemPrompt(agentId string) []string   // 获取注入的笔记
```

### 智能匹配规则

```go
// 根据 agent 角色匹配相关知识
func MatchNotesForAgent(agentId string, role string, notes []Note) []Note {
    var matched []Note
    
    // 1. 全局知识
    for _, n := range notes {
        if n.Scope == ScopeGlobal {
            matched = append(matched, n)
        }
    }
    
    // 2. 角色相关标签
    roleKeywords := map[string][]string{
        "后端": {"api", "database", "server", "backend"},
        "前端": {"ui", "frontend", "react", "css"},
        "测试": {"test", "qa", "bug"},
    }
    
    for _, n := range notes {
        for _, tag := range n.Tags {
            for _, kw := range roleKeywords[role] {
                if strings.Contains(strings.ToLower(tag), kw) {
                    matched = append(matched, n)
                    break
                }
            }
        }
    }
    
    return matched
}
```

### 前端界面

```
┌─────────────────────────────────────────┐
│ 知识库                    [+ 新建笔记]   │
├─────────────────────────────────────────┤
│ [搜索...] [标签▼] [作用域▼]             │
├─────────────────────────────────────────┤
│ ┌─────────────────────────────────────┐ │
│ │ 📌 API 设计规范                      │ │
│ │ #api #backend                       │ │
│ │ RESTful 风格，版本号放 URL...        │ │
│ │                                     │ │
│ │ [附加到 agent] [编辑] [删除]         │ │
│ └─────────────────────────────────────┘ │
│ ┌─────────────────────────────────────┐ │
│ │ 📌 数据库优化指南                    │ │
│ │ #database #backend                  │ │
│ │ 索引设计原则...                      │ │
│ │                                     │ │
│ │ [附加到 agent] [编辑] [删除]         │ │
│ └─────────────────────────────────────┘ │
└─────────────────────────────────────────┘
```

## 实现计划

| 阶段 | 功能 | 工作量 |
|------|------|--------|
| Phase 1 | 完善数据模型 + API | 2h |
| Phase 2 | 智能匹配 + 自动注入 | 2h |
| Phase 3 | 前端界面优化 | 2h |
| Phase 4 | 测试 + 文档 | 1h |

## 验收标准

- [ ] 创建 agent 时自动注入相关知识
- [ ] 可以手动给 agent 附加知识
- [ ] 支持全文搜索和标签过滤
- [ ] 支持知识分类（全局/agent/频道）
- [ ] 前端界面友好，操作流畅
