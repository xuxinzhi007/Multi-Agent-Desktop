package feishu

import "sync"

// Bot 是一个要维持长连接的机器人。
type Bot struct {
	ID     string
	Config Config
}

// Hub 让每个机器人各走一条长连接，会话记录共用一份。
type Hub struct {
	hooks Hooks
	log   *Log
	mu    sync.Mutex
	bots  map[string]*Service
}

// NewHub 构造一组飞书机器人。
func NewHub(h Hooks) *Hub {
	return &Hub{hooks: h, log: NewLog(), bots: map[string]*Service{}}
}

// SetLog 把会话记录落到 path。
func (h *Hub) SetLog(path string) {
	if h == nil {
		return
	}
	h.log.SetPath(path)
}

// Sync 让正在跑的连接和这份名单一致。配置没变的机器人不重新连接。
func (h *Hub) Sync(list []Bot) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	keep := map[string]struct{}{}
	for _, b := range list {
		if b.ID == "" {
			continue
		}
		keep[b.ID] = struct{}{}
		svc := h.bots[b.ID]
		if svc == nil {
			svc = New(h.hooks)
			svc.ShareLog(h.log)
			svc.SetOwner(b.ID)
			h.bots[b.ID] = svc
			svc.Apply(b.Config)
			continue
		}
		if svc.Current() != b.Config {
			svc.Apply(b.Config)
		}
	}
	for id, svc := range h.bots {
		if _, ok := keep[id]; ok {
			continue
		}
		svc.Apply(Config{})
		delete(h.bots, id)
	}
}

// Status 返回这个机器人的连接状态。
func (h *Hub) Status(id string) View {
	if h == nil {
		return View{State: "stopped"}
	}
	h.mu.Lock()
	svc := h.bots[id]
	h.mu.Unlock()
	if svc == nil {
		return View{State: "stopped"}
	}
	return svc.Status()
}

// Rooms 返回飞书会话记录。
func (h *Hub) Rooms() []Room {
	if h == nil {
		return nil
	}
	return h.log.Rooms()
}

// BindRoom 从下一条消息起，这个飞书群或私聊改由另一个本机会话回答。
func (h *Hub) BindRoom(chatID, agentID string) {
	if h == nil {
		return
	}
	h.log.Bind(chatID, agentID)
}

// StopRun 停掉这个会话正在为飞书写的那一轮。
func (h *Hub) StopRun(agentID string) {
	for _, svc := range h.snapshot() {
		svc.StopRun(agentID)
	}
}

// NoteDelta 把模型已经说出的句子写进对应机器人的卡片。
func (h *Hub) NoteDelta(data any) {
	for _, svc := range h.snapshot() {
		svc.NoteDelta(data)
	}
}

// NoteTool 接收会话的工具事件，写给正在回答的那个机器人。
func (h *Hub) NoteTool(data any) {
	for _, svc := range h.snapshot() {
		svc.NoteTool(data)
	}
}

func (h *Hub) snapshot() []*Service {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Service, 0, len(h.bots))
	for _, svc := range h.bots {
		out = append(out, svc)
	}
	return out
}
