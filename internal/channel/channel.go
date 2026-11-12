package channel

import (
	"fmt"
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
}

// Channel 是一个共享消息频道。
type Channel struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Members  []string  `json:"members"` // agent IDs
	Messages []Message `json:"messages"`
}

// Manager 管理所有频道。
type Manager struct {
	mu       sync.RWMutex
	channels map[string]*Channel
	order    []string
}

func NewManager() *Manager {
	return &Manager{
		channels: make(map[string]*Channel),
	}
}

// Create 新建频道。
func (m *Manager) Create(id, name string, members []string) *Channel {
	m.mu.Lock()
	defer m.mu.Unlock()

	if id == "" {
		id = fmt.Sprintf("ch-%d", time.Now().UnixNano())
	}

	ch := &Channel{
		ID:      id,
		Name:    name,
		Members: members,
	}
	m.channels[id] = ch
	m.order = append(m.order, id)
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

// AddMessage 添加消息到频道。
func (m *Manager) AddMessage(channelId, fromId, fromName, text string) (*Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch, ok := m.channels[channelId]
	if !ok {
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
	return &msg, nil
}

// Broadcast 发消息到频道，返回所有成员的 agent ID（用于触发执行）。
func (m *Manager) Broadcast(channelId, fromId, fromName, text string) ([]string, error) {
	msg, err := m.AddMessage(channelId, fromId, fromName, text)
	if err != nil {
		return nil, err
	}

	m.mu.RLock()
	ch := m.channels[channelId]
	m.mu.RUnlock()

	// 收集接收者（排除发送者）
	var recipients []string
	for _, memberId := range ch.Members {
		if memberId != fromId {
			recipients = append(recipients, memberId)
		}
	}

	_ = msg // 避免 unused
	return recipients, nil
}
