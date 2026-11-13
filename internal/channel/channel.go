package channel

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Message 是频道里的一条消息。
type Message struct {
	ID        string `json:"id"`
	ChannelID string `json:"channelId"`
	FromID    string `json:"fromId"` // agent ID 或 "user"
	FromName  string `json:"fromName"`
	Text      string `json:"text"`
	At        int64  `json:"at"`
	Kind      string `json:"kind,omitempty"` // user / agent / system
	
	// Deliveries 记录投递结果：谁收到了、谁跳过了
	Deliveries []Delivery `json:"deliveries,omitempty"`
}

// Delivery 是消息的投递记录。
type Delivery struct {
	AgentID string `json:"agentId"`
	Status  string `json:"status"` // delivered / skipped / failed
	Reason  string `json:"reason,omitempty"` // 跳过原因
}

// TurnPolicy 回合策略。
type TurnPolicy string

const (
	// PolicyDirect 只有被点名的人回（单聊）
	PolicyDirect TurnPolicy = "direct"
	// PolicyDiscuss 所有成员都能看到上下文，按 @ 或显式指派决定谁回
	PolicyDiscuss TurnPolicy = "discuss"
	// PolicyRoundRobin 轮流发言，用于评审场景
	PolicyRoundRobin TurnPolicy = "roundrobin"
)

// Channel 是一个共享消息频道。
type Channel struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Members  []string  `json:"members"` // agent IDs
	Messages []Message `json:"messages"`
	Policy   TurnPolicy `json:"policy,omitempty"` // 回合策略，默认 discuss
}

// Manager 管理所有频道。
type Manager struct {
	mu       sync.RWMutex
	channels map[string]*Channel
	order    []string
	store    string // 持久化文件路径，空则不持久化

	// saveMu 串行化落盘：Create/AddMessage/RemoveMember 都会 go m.Save()。
	saveMu sync.Mutex
	// saveWG 跟踪异步 Save，测试可用 waitSaves 等待落盘结束。
	saveWG sync.WaitGroup
}

func NewManager() *Manager {
	return &Manager{
		channels: make(map[string]*Channel),
	}
}

// SetStore 设置持久化文件路径，并尝试加载。
func (m *Manager) SetStore(path string) error {
	m.mu.Lock()
	m.store = path
	m.mu.Unlock()
	return m.Load()
}

// goSave 异步落盘，并登记到 saveWG 便于测试同步等待。
func (m *Manager) goSave() {
	m.saveWG.Add(1)
	go func() {
		defer m.saveWG.Done()
		_ = m.Save()
	}()
}

// waitSaves 阻塞直到所有异步落盘结束（测试用）。
func (m *Manager) waitSaves() { m.saveWG.Wait() }

// Save 把所有频道序列化到磁盘。
// 串行写 + 写临时文件再 rename，保证读者永远看到完整 JSON。
func (m *Manager) Save() error {
	m.mu.RLock()
	store := m.store
	m.mu.RUnlock()
	if store == "" {
		return nil
	}

	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	m.mu.RLock()
	data := make([]Channel, 0, len(m.order))
	for _, id := range m.order {
		if ch, ok := m.channels[id]; ok {
			data = append(data, Channel{
				ID:       ch.ID,
				Name:     ch.Name,
				Members:  append([]string(nil), ch.Members...),
				Messages: append([]Message(nil), ch.Messages...),
			})
		}
	}
	m.mu.RUnlock()

	buf, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := store + ".tmp"
	if err := os.WriteFile(tmp, buf, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, store)
}

// Load 从磁盘恢复频道。
func (m *Manager) Load() error {
	m.mu.RLock()
	store := m.store
	m.mu.RUnlock()
	if store == "" {
		return nil
	}

	// 与 Save 互斥，避免读到正在被替换的文件。
	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	buf, err := os.ReadFile(store)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var data []*Channel
	if err := json.Unmarshal(buf, &data); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, saved := range data {
		if _, ok := m.channels[saved.ID]; ok {
			continue // 已存在则不覆盖（避免重复加载）
		}
		// 防御：确保 Messages 非 nil，避免前端 null 崩溃
		if saved.Messages == nil {
			saved.Messages = []Message{}
		}
		m.channels[saved.ID] = saved
		m.order = append(m.order, saved.ID)
	}
	return nil
}

// Create 新建频道。
func (m *Manager) Create(id, name string, members []string) *Channel {
	m.mu.Lock()
	if id == "" {
		id = fmt.Sprintf("ch-%d", time.Now().UnixNano())
	}

	ch := &Channel{
		ID:       id,
		Name:     name,
		Members:  append([]string(nil), members...),
		Messages: []Message{},
	}
	m.channels[id] = ch
	m.order = append(m.order, id)
	m.mu.Unlock()

	m.goSave()
	return ch
}

// Get 返回频道。
func (m *Manager) Get(id string) (*Channel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ch, ok := m.channels[id]
	return ch, ok
}

// List 返回所有频道。
func (m *Manager) List() []*Channel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Channel, 0, len(m.order))
	for _, id := range m.order {
		if ch, ok := m.channels[id]; ok {
			out = append(out, ch)
		}
	}
	return out
}

// RemoveMember 把某个 agent 从所有频道移除（agent 被删除时调用），避免残留接收者。
func (m *Manager) RemoveMember(agentId string) {
	m.mu.Lock()
	for _, ch := range m.channels {
		// 用新切片避免 aliasing 修改原数组
		kept := make([]string, 0, len(ch.Members))
		for _, id := range ch.Members {
			if id != agentId {
				kept = append(kept, id)
			}
		}
		ch.Members = kept
	}
	m.mu.Unlock()
	m.goSave()
}

// AddMessage 添加消息到频道，返回创建的消息。
func (m *Manager) AddMessage(channelId, fromId, fromName, text string) (*Message, error) {
	m.mu.Lock()

	ch, ok := m.channels[channelId]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("channel %s not found", channelId)
	}

	msg := Message{
		ID:        fmt.Sprintf("msg-%d", time.Now().UnixNano()),
		ChannelID: channelId,
		FromID:    fromId,
		FromName:  fromName,
		Text:      text,
		At:        time.Now().Unix(),
	}
	ch.Messages = append(ch.Messages, msg)
	m.mu.Unlock()
	m.goSave()
	return &msg, nil
}

// UpdateMessage 按 ID 覆盖已有消息（用于回填投递记录），并落盘。
func (m *Manager) UpdateMessage(msg Message) error {
	m.mu.Lock()
	ch, ok := m.channels[msg.ChannelID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("channel %s not found", msg.ChannelID)
	}
	found := false
	for i := range ch.Messages {
		if ch.Messages[i].ID == msg.ID {
			ch.Messages[i] = msg
			found = true
			break
		}
	}
	m.mu.Unlock()
	if !found {
		return fmt.Errorf("message %s not found", msg.ID)
	}
	m.goSave()
	return nil
}

// Broadcast 发消息到频道，返回创建的消息与所有成员的 agent ID（用于触发执行）。
// 消息总会被记录，即使用户是唯一发送者；接收者排除发送者自身。
func (m *Manager) Broadcast(channelId, fromId, fromName, text string) (*Message, []string, error) {
	msg, err := m.AddMessage(channelId, fromId, fromName, text)
	if err != nil {
		return nil, nil, err
	}

	m.mu.RLock()
	ch := m.channels[channelId]
	var recipients []string
	if ch != nil {
		for _, memberId := range ch.Members {
			if memberId != fromId {
				recipients = append(recipients, memberId)
			}
		}
	}
	m.mu.RUnlock()

	return msg, recipients, nil
}

// Transcript 是频道的共享上下文，按预算裁剪后拼入 prompt。
type Transcript struct {
	Messages []Message
	Budget   int // 字符预算
}

// BuildTranscript 从频道消息构建 transcript，按预算裁剪。
// 保留最近的消息，确保预算内。
func BuildTranscript(messages []Message, budget int) Transcript {
	if budget <= 0 || len(messages) == 0 {
		return Transcript{Messages: messages, Budget: budget}
	}

	// 从后往前累积，直到超预算
	var out []Message
	used := 0
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		size := len(m.FromName) + len(m.Text) + 20
		if used+size > budget && len(out) > 0 {
			break
		}
		out = append([]Message{m}, out...)
		used += size
	}
	return Transcript{Messages: out, Budget: budget}
}

// Render 把 transcript 渲染成 prompt 段。
func (t Transcript) Render() string {
	if len(t.Messages) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## 群聊上下文\n")
	for _, m := range t.Messages {
		b.WriteString(m.FromName)
		b.WriteString(": ")
		b.WriteString(m.Text)
		b.WriteString("\n")
	}
	return b.String()
}

// ShouldReply 判断 agent 是否应该回复这条消息。
// 按 TurnPolicy 和 @提及决定。
// ShouldReply 判断 agent 是否应该回复这条消息。
// role 用于 discuss 模式下的相关性判断（空则不判断）。
func ShouldReply(policy TurnPolicy, msg Message, agentID, agentName, role string) (bool, string) {
	// 解析 @提及
	mentions := ParseMentions(msg.Text)

	// 被 @ 必须回复（含 @all）
	for _, m := range mentions {
		if m == agentName || m == agentID || m == "all" {
			return true, "被 @"
		}
	}

	// 有 @ 但没 @ 到我 —— 让被点名的人说，不打扰其他成员
	if len(mentions) > 0 {
		return false, "有 @ 但未点到我"
	}

	// 按策略判断
	switch policy {
	case PolicyDirect:
		return false, "direct 模式未被 @"

	case PolicyDiscuss:
		// 讨论模式：没被 @ 时由 SendToChannel 统一选 1 个最相关的成员，
		// 这里返回 false，避免 N 成员 = N 倍成本。
		return false, "discuss 模式 · 未被点名"

	case PolicyRoundRobin:
		return true, "roundrobin 模式"

	default:
		return true, "默认 discuss 模式"
	}
}

// RoleRelevant 判断消息是否与该角色相关（供 SendToChannel 排序用）。
// 关键词表来自角色名（"后端" / "前端" / "测试" / "运维"）与内容的子串匹配。
func RoleRelevant(text, role string) bool {
	if role == "" {
		return false
	}
	keywords := map[string][]string{
		"后端": {"api", "api", "数据库", "sql", "服务", "接口", "server", "backend"},
		"前端": {"ui", "页面", "组件", "样式", "css", "前端", "react", "frontend"},
		"测试": {"测试", "bug", "错误", "用例", "qa", "test"},
		"运维": {"部署", "监控", "日志", "服务器", "deploy", "ops"},
	}
	lower := strings.ToLower(text)
	lowerRole := strings.ToLower(role)

	// 角色名本身出现在文本里，也算相关
	if strings.Contains(lower, lowerRole) {
		return true
	}
	for key, kws := range keywords {
		if !strings.Contains(role, key) {
			continue
		}
		for _, kw := range kws {
			if strings.Contains(lower, kw) {
				return true
			}
		}
	}
	return false
}

// ParseMentions 解析 @提及（前后端语义唯一来源）。
func ParseMentions(text string) []string {
	var mentions []string
	words := strings.Fields(text)
	for _, word := range words {
		if strings.HasPrefix(word, "@") {
			mention := strings.TrimPrefix(word, "@")
			mention = strings.TrimRight(mention, ",.!?:;，。！？：；")
			if mention != "" {
				mentions = append(mentions, mention)
			}
		}
	}
	return mentions
}
