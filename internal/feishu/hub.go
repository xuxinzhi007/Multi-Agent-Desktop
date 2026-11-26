package feishu

import (
	"context"
	"fmt"
	"sync"
)

// Bot 是一个要维持长连接的机器人。
type Bot struct {
	ID     string
	Config Config
}

// Hub 让每个机器人各走一条长连接，会话记录共用一份。
type Hub struct {
	hooks Hooks
	log   *Log
	sink  HistorySink
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

// SetHistorySink 挂上频道表桥接，并同步给已经建好的机器人连接。
func (h *Hub) SetHistorySink(s HistorySink) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.sink = s
	bots := make([]*Service, 0, len(h.bots))
	for _, svc := range h.bots {
		bots = append(bots, svc)
	}
	h.mu.Unlock()
	for _, svc := range bots {
		svc.SetHistorySink(s)
	}
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
			svc.SetHistorySink(h.sink)
			h.bots[b.ID] = svc
			svc.Apply(b.Config)
			continue
		}
		svc.SetHistorySink(h.sink)
		if !sameConfig(svc.Current(), b.Config) {
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

// sameConfig 比较两份配置。Config 里带函数字段，不能直接用 ==。
// 函数字段只影响回调，不影响连接参数，比较时跳过。
func sameConfig(a, b Config) bool {
	return a.Enabled == b.Enabled &&
		a.AppID == b.AppID &&
		a.AppSecret == b.AppSecret &&
		a.AgentID == b.AgentID &&
		a.MentionOnly == b.MentionOnly &&
		a.UserAccess == b.UserAccess &&
		a.UserRefresh == b.UserRefresh &&
		a.UserExpire == b.UserExpire &&
		sameList(a.AllowChats, b.AllowChats)
}

func sameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

// SyncRoom 按 chat_id 拉一次群历史，返回新增条数。
// 优先用已经授权用户身份的那个机器人：群消息本来就得靠用户身份才读得到。
func (h *Hub) SyncRoom(ctx context.Context, chatID string) (int, error) {
	if h == nil {
		return 0, fmt.Errorf("飞书还没接上")
	}
	list := h.snapshot()
	for _, svc := range list {
		if svc.HasUser() {
			return svc.SyncRoom(ctx, chatID, "")
		}
	}
	if len(list) == 0 {
		return 0, fmt.Errorf("还没有飞书机器人")
	}
	return list[0].SyncRoom(ctx, chatID, "")
}

// FetchMedia 用能读消息的身份下载一条消息里的资源（图片/文件）。
func (h *Hub) FetchMedia(ctx context.Context, messageID, key, kind string) ([]byte, string, error) {
	if h == nil {
		return nil, "", fmt.Errorf("飞书还没接上")
	}
	list := h.snapshot()
	for _, svc := range list {
		if svc.HasUser() {
			return svc.FetchMedia(ctx, messageID, key, kind)
		}
	}
	if len(list) == 0 {
		return nil, "", fmt.Errorf("还没有飞书机器人")
	}
	return list[0].FetchMedia(ctx, messageID, key, kind)
}

// Rooms 返回飞书会话记录。
func (h *Hub) Rooms() []Room {
	if h == nil {
		return nil
	}
	return h.log.Rooms()
}

// BotName 返回从飞书问到的机器人名字。问不到时退回配置名，再退空。
func (h *Hub) BotName(botID, fallback string) string {
	if h == nil {
		return fallback
	}
	h.mu.Lock()
	svc := h.bots[botID]
	h.mu.Unlock()
	if name := svc.BotName(); name != "" {
		return name
	}
	return fallback
}

// SetGroupMode 切换一个群的三态（off / mention / active），落到共享群配置。
func (h *Hub) SetGroupMode(chatID, mode string) {
	if h == nil {
		return
	}
	h.log.SetMode(chatID, mode)
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

// Ask 让正在回答的机器人把批准卡片发到对应的飞书对话。
func (h *Hub) Ask(v Ask) {
	for _, svc := range h.snapshot() {
		svc.Ask(v)
	}
}

// CloseAsk 桌面已经处理过这张批准时，收起飞书上还挂着的按钮。
func (h *Hub) CloseAsk(id string) {
	for _, svc := range h.snapshot() {
		svc.CloseAsk(id)
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
