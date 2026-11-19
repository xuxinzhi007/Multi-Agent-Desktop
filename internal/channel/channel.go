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
	Status  string `json:"status"`           // delivered / skipped / failed
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
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Members  []string   `json:"members"` // agent IDs
	Messages []Message  `json:"messages"`
	Policy   TurnPolicy `json:"policy,omitempty"` // 回合策略，默认 discuss
	// Speaker 是没人被 @ 时的默认接话人（agent ID）。空则这一条没人接。
	Speaker string `json:"speaker,omitempty"`
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
				Members:  append([]string{}, ch.Members...),
				Messages: append([]Message{}, ch.Messages...),
				Policy:   ch.Policy,
				Speaker:  ch.Speaker,
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
		// 防御：确保切片非 nil，避免前端拿到 null 后崩
		if saved.Messages == nil {
			saved.Messages = []Message{}
		}
		if saved.Members == nil {
			saved.Members = []string{}
		}
		// 老版本写入的消息可能缺 fromId / fromName / channelId。
		// 这些字段是 string，反序列化后是空串，前端拿它做 .slice(0,1)
		// 就会崩。在加载时就补齐，让“缺字段”只在一个地方被处理。
		for i := range saved.Messages {
			msg := &saved.Messages[i]
			if msg.ChannelID == "" {
				msg.ChannelID = saved.ID
			}
			if msg.FromID == "" {
				msg.FromID = "unknown"
			}
			if msg.FromName == "" {
				msg.FromName = msg.FromID
			}
		}
		m.channels[saved.ID] = saved
		m.order = append(m.order, saved.ID)
	}
	return nil
}

// clone 深拷贝一个频道，返回与存储完全解耦的值。
// Manager 的所有对外接口都必须返回副本：调用方会在锁外读取，
// 而写路径的 append 会让底层数组 realloc，返回指针即构成数据竞争。
func (c *Channel) clone() Channel {
	out := Channel{
		ID:      c.ID,
		Name:    c.Name,
		Members: append([]string{}, c.Members...),
		Policy:  c.Policy,
		Speaker: c.Speaker,
	}
	if c.Messages == nil {
		out.Messages = []Message{}
		return out
	}
	out.Messages = make([]Message, len(c.Messages))
	for i := range c.Messages {
		out.Messages[i] = c.Messages[i].clone()
	}
	return out
}

// clone 深拷贝一条消息（Deliveries 也要断开，否则回填会写到别人的切片上）。
func (msg Message) clone() Message {
	out := msg
	if msg.Deliveries != nil {
		out.Deliveries = append([]Delivery(nil), msg.Deliveries...)
	}
	return out
}

// Create 新建频道，返回快照副本。
func (m *Manager) Create(id, name string, members []string, policy TurnPolicy) Channel {
	m.mu.Lock()
	if id == "" {
		id = fmt.Sprintf("ch-%d", time.Now().UnixNano())
	}
	if policy == "" {
		policy = PolicyDiscuss
	}

	ch := &Channel{
		ID:       id,
		Name:     name,
		Members:  append([]string(nil), members...),
		Messages: []Message{},
		Policy:   policy,
	}
	m.channels[id] = ch
	m.order = append(m.order, id)
	snapshot := ch.clone()
	m.mu.Unlock()

	m.goSave()
	return snapshot
}

// Get 返回频道快照副本。
func (m *Manager) Get(id string) (Channel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ch, ok := m.channels[id]
	if !ok {
		return Channel{}, false
	}
	return ch.clone(), true
}

// List 返回所有频道的快照副本。
func (m *Manager) List() []Channel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Channel, 0, len(m.order))
	for _, id := range m.order {
		if ch, ok := m.channels[id]; ok {
			out = append(out, ch.clone())
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
		if ch.Speaker == agentId {
			ch.Speaker = ""
		}
	}
	m.mu.Unlock()
	m.goSave()
}

// AddMessage 添加消息到频道，返回创建的消息副本。
func (m *Manager) AddMessage(channelId, fromId, fromName, text string) (Message, error) {
	m.mu.Lock()

	ch, ok := m.channels[channelId]
	if !ok {
		m.mu.Unlock()
		return Message{}, fmt.Errorf("channel %s not found", channelId)
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
	return msg, nil
}

// SetDeliveries 按 ID 回填投递记录，并落盘。
// 只接受 ID + deliveries，避免调用方拿着旧快照整体覆盖（会丢掉并发写入的新字段）。
func (m *Manager) SetDeliveries(channelId, msgID string, deliveries []Delivery) error {
	m.mu.Lock()
	ch, ok := m.channels[channelId]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("channel %s not found", channelId)
	}
	found := false
	for i := range ch.Messages {
		if ch.Messages[i].ID == msgID {
			ch.Messages[i].Deliveries = append([]Delivery(nil), deliveries...)
			found = true
			break
		}
	}
	m.mu.Unlock()
	if !found {
		return fmt.Errorf("message %s not found", msgID)
	}
	m.goSave()
	return nil
}

// Broadcast 发消息到频道，返回创建的消息副本与所有成员 agent ID。
// 消息总会被记录，即使用户是唯一发送者；接收者排除发送者自身。
func (m *Manager) Broadcast(channelId, fromId, fromName, text string) (Message, []string, error) {
	msg, err := m.AddMessage(channelId, fromId, fromName, text)
	if err != nil {
		return Message{}, nil, err
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
// 按 TurnPolicy 和 @提及决定；role 供 discuss 模式判定相关性（空则不判）。
func ShouldReply(policy TurnPolicy, msg Message, agentID, agentName, role string) (bool, string) {
	// 解析 @提及
	mentions := ParseMentions(msg.Text)

	// 被 @ 必须回复（含 @all）
	for _, m := range mentions {
		if m == agentName || m == agentID || m == "all" || m == "所有人" {
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
		// 单聊：成员就该回，不靠 @ 触发（之前这里返回 false，导致 direct 频道永不回复）
		return true, "direct 模式 · 全员回复"

	case PolicyDiscuss:
		// 讨论模式：没被 @ 时由 DecideReplies 统一选 1 个最相关的成员，
		// 这里返回 false，避免 N 成员 = N 倍成本。
		return false, "discuss 模式 · 未被点名"

	case PolicyRoundRobin:
		return true, "roundrobin 模式"

	default:
		return false, "未识别的策略"
	}
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

// Member 是参与回合判定的成员最小信息。
// channel 包不依赖 agent 包，由调用方把 Agent 投影成这个结构。
type Member struct {
	ID   string
	Name string
	Role string
}

// Decision 是一个成员的回合判定结果。
type Decision struct {
	Member Member
	Reply  bool
	Reason string
}

// DecideReplies 决定一条消息该由哪些成员回复，是「谁该回」的唯一实现。
// 规则：
//   - 被 @ 的必须回（@all / @所有人 命中全员）
//   - 有人被 @ 时，没被点名的成员不打扰
//   - discuss 模式下没人被 @，只由 speaker 接；speaker 为空或不是成员，则没人接
//   - direct / roundrobin 由 ShouldReply 直接给出结论
//
// speaker 是该群当前的默认接话人。调用方每次用最新值，所以改人从下一条生效。
// 返回值按 recipients 顺序排列，便于上层稳定地产生 deliveries。
func DecideReplies(policy TurnPolicy, msg Message, recipients []Member, speaker string) []Decision {
	if len(recipients) == 0 {
		return nil
	}
	if policy == "" {
		policy = PolicyDiscuss
	}

	mentions := ParseMentions(msg.Text)
	addressed := make(map[string]bool)
	for _, m := range recipients {
		if isAddressed(mentions, m) {
			addressed[m.ID] = true
		}
	}

	if policy == PolicyDiscuss && len(addressed) == 0 {
		out := make([]Decision, 0, len(recipients))
		for _, m := range recipients {
			reply := speaker != "" && m.ID == speaker
			reason := "未指定默认接话人"
			if reply {
				reason = "默认接话人"
			}
			out = append(out, Decision{Member: m, Reply: reply, Reason: reason})
		}
		return out
	}

	out := make([]Decision, 0, len(recipients))
	for _, m := range recipients {
		reply, reason := ShouldReply(policy, msg, m.ID, m.Name, m.Role)
		if !reply && len(addressed) > 0 && !addressed[m.ID] {
			reason = "未被点名"
		}
		out = append(out, Decision{Member: m, Reply: reply, Reason: reason})
	}
	return out
}

// isAddressed 判断某成员是否被这条消息点名。
func isAddressed(mentions []string, m Member) bool {
	for _, mention := range mentions {
		if mention == m.Name || mention == m.ID || mention == "all" || mention == "所有人" {
			return true
		}
	}
	return false
}

// SetSpeaker 指定这个群在没人被 @ 时由谁接话。
// agentId 为空表示取消。下一次 DecideReplies 才读到新值。
func (m *Manager) SetSpeaker(channelId, agentId string) (Channel, error) {
	m.mu.Lock()
	ch, ok := m.channels[channelId]
	if !ok {
		m.mu.Unlock()
		return Channel{}, fmt.Errorf("channel %s not found", channelId)
	}
	if agentId != "" {
		member := false
		for _, id := range ch.Members {
			if id == agentId {
				member = true
				break
			}
		}
		if !member {
			m.mu.Unlock()
			return Channel{}, fmt.Errorf("agent %s is not a member of channel %s", agentId, channelId)
		}
	}
	ch.Speaker = agentId
	snap := ch.clone()
	m.mu.Unlock()
	m.goSave()
	return snap, nil
}
