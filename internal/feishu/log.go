package feishu

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
)

const roomCap = 200

// Line 是飞书某个会话里的一条记录。
type Line struct {
	ID     string `json:"id"`
	ChatID string `json:"chatId"`
	From   string `json:"from"`
	FromID string `json:"fromId,omitempty"`
	Text   string `json:"text"`
	At     int64  `json:"at"`
	Self   bool   `json:"self,omitempty"`
}

// Room 是一个飞书会话的记录，给界面看。
type Room struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind,omitempty"`
	AgentID   string `json:"agentId,omitempty"`
	AgentName string `json:"agentName,omitempty"`
	Messages  []Line `json:"messages"`
}

// Log 按飞书会话记下消息。同一条 message_id 只留一次。
type Log struct {
	mu    sync.Mutex
	path  string
	order []string
	rooms map[string]*room
}

type room struct {
	Name    string
	Kind    string
	AgentID string
	Lines   []Line
	Seen    map[string]struct{}
}

func NewLog() *Log {
	return &Log{rooms: map[string]*room{}}
}

func (l *Log) SetPath(path string) {
	if l == nil || path == "" {
		return
	}
	l.mu.Lock()
	l.path = path
	l.mu.Unlock()
	l.load()
}

func (l *Log) load() {
	l.mu.Lock()
	path := l.path
	l.mu.Unlock()
	buf, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var saved []Room
	if json.Unmarshal(buf, &saved) != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range saved {
		if r.ID == "" {
			continue
		}
		dst := l.room(r.ID)
		if r.Name != "" {
			dst.Name = r.Name
		}
		if r.Kind != "" {
			dst.Kind = r.Kind
		}
		if r.AgentID != "" {
			dst.AgentID = r.AgentID
		}
		for _, line := range r.Messages {
			l.addLocked(line)
		}
	}
}

func (l *Log) room(id string) *room {
	r := l.rooms[id]
	if r == nil {
		r = &room{Seen: map[string]struct{}{}}
		l.rooms[id] = r
		l.order = append(l.order, id)
	}
	return r
}

// Known 表示这个会话是否已经有记录。
func (l *Log) Known(id string) bool {
	if l == nil || id == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.rooms[id]
	return ok
}

// Name 记下群名。已经有名字时不覆盖空值。
func (l *Log) Name(id, name string) {
	if l == nil || id == "" || strings.TrimSpace(name) == "" {
		return
	}
	l.mu.Lock()
	l.room(id).Name = strings.TrimSpace(name)
	l.mu.Unlock()
	l.save()
}

// Kind 记下这是群还是私聊。
func (l *Log) Kind(id, kind string) {
	if l == nil || id == "" || strings.TrimSpace(kind) == "" {
		return
	}
	l.mu.Lock()
	r := l.room(id)
	if r.Kind == kind {
		l.mu.Unlock()
		return
	}
	r.Kind = kind
	l.mu.Unlock()
	l.save()
}

// Bind 指定这个飞书会话由哪个本机会话回答。
func (l *Log) Bind(id, agentID string) {
	if l == nil || id == "" || strings.TrimSpace(agentID) == "" {
		return
	}
	l.mu.Lock()
	l.room(id).AgentID = strings.TrimSpace(agentID)
	l.mu.Unlock()
	l.save()
}

// Agent 返回单独指定的本机会话。空表示沿用默认。
func (l *Log) Agent(id string) string {
	if l == nil || id == "" {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.rooms[id]
	if r == nil {
		return ""
	}
	return r.AgentID
}

// Add 写入一条。已经有同一 ID 时返回 false。
func (l *Log) Add(line Line) bool {
	if l == nil || line.ChatID == "" || line.ID == "" {
		return false
	}
	l.mu.Lock()
	ok := l.addLocked(line)
	l.mu.Unlock()
	if ok {
		l.save()
	}
	return ok
}

func (l *Log) addLocked(line Line) bool {
	r := l.room(line.ChatID)
	if _, ok := r.Seen[line.ID]; ok {
		return false
	}
	r.Seen[line.ID] = struct{}{}
	r.Lines = append(r.Lines, line)
	if len(r.Lines) > roomCap {
		drop := r.Lines[:len(r.Lines)-roomCap]
		r.Lines = r.Lines[len(r.Lines)-roomCap:]
		for _, old := range drop {
			delete(r.Seen, old.ID)
		}
	}
	return true
}

// Self 判断这条是不是机器人自己发出的。
func (l *Log) Self(id string) bool {
	if l == nil || id == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.rooms {
		for _, line := range r.Lines {
			if line.ID == id && line.Self {
				return true
			}
		}
	}
	return false
}

// Chats 返回已经见过的会话和各自最后一条的时间。
func (l *Log) Chats() []Room {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Room, 0, len(l.order))
	for _, id := range l.order {
		r := l.rooms[id]
		name := r.Name
		if name == "" {
			name = id
		}
		var last int64
		if n := len(r.Lines); n > 0 {
			last = r.Lines[n-1].At
		}
		out = append(out, Room{ID: id, Name: name, Messages: []Line{{At: last}}})
	}
	return out
}

// Rooms 返回给界面的完整记录。
func (l *Log) Rooms() []Room {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Room, 0, len(l.order))
	for _, id := range l.order {
		r := l.rooms[id]
		lines := append([]Line(nil), r.Lines...)
		out = append(out, Room{ID: id, Name: roomTitle(r), Kind: r.Kind, AgentID: r.AgentID, Messages: lines})
	}
	return out
}

// Prompt 把这个会话最近的对话拼成给模型的正文。
func (l *Log) Prompt(chatID, name string, join bool) string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	r := l.rooms[chatID]
	var lines []Line
	if r != nil {
		lines = append([]Line(nil), r.Lines...)
		if name == "" {
			name = r.Name
		}
	}
	l.mu.Unlock()
	if name == "" {
		name = "飞书"
	}
	var b strings.Builder
	b.WriteString("来自飞书「")
	b.WriteString(name)
	if join {
		b.WriteString("」。你在这个群里。不用别人 @ 你，看到新的一句就接话。只根据这些内容回答最后一句，不要讨论本应用怎么实现。\n")
	} else {
		b.WriteString("」。别人 @ 了你或回复了你。只根据这些内容回答最后一句，不要讨论本应用怎么实现。\n")
	}
	for _, line := range lines {
		who := line.From
		if who == "" {
			who = "有人"
		}
		if line.Self {
			who = "你"
		}
		b.WriteString(who)
		b.WriteString("：")
		b.WriteString(line.Text)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func roomTitle(r *room) string {
	if strings.TrimSpace(r.Name) != "" {
		return strings.TrimSpace(r.Name)
	}
	if n := len(r.Lines); n > 0 {
		s := strings.TrimSpace(r.Lines[n-1].Text)
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[:i]
		}
		s = strings.Join(strings.Fields(s), " ")
		rs := []rune(s)
		if len(rs) > 32 {
			s = string(rs[:32]) + "…"
		}
		if s != "" {
			return s
		}
	}
	return "飞书会话"
}

func (l *Log) save() {
	l.mu.Lock()
	path := l.path
	rooms := make([]Room, 0, len(l.order))
	for _, id := range l.order {
		r := l.rooms[id]
		rooms = append(rooms, Room{ID: id, Name: r.Name, Kind: r.Kind, AgentID: r.AgentID, Messages: append([]Line(nil), r.Lines...)})
	}
	l.mu.Unlock()
	if path == "" {
		return
	}
	buf, err := json.MarshalIndent(rooms, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, buf, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
