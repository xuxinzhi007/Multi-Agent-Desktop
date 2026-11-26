package feishu

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const roomCap = 200

// Media 是消息里带的一张图或一个文件。Key 用来调消息资源接口下载。
type Media struct {
	Kind string `json:"kind"`           // image / file / audio / video
	Key  string `json:"key,omitempty"`  // 图片或文件的 file_key
	Name string `json:"name,omitempty"` // 文件名
}

// Line 是飞书某个会话里的一条记录。
type Line struct {
	ID     string `json:"id"`
	ChatID string `json:"chatId"`
	From   string `json:"from"`
	FromID string `json:"fromId,omitempty"`
	Text   string `json:"text"`
	At     int64  `json:"at"`
	Self   bool   `json:"self,omitempty"`
	// Kind 是消息类型：text / post / image / file / card / system / unsupported 等。
	// 界面据此决定怎么画；空表示老数据，按文本处理。
	Kind  string  `json:"kind,omitempty"`
	Media []Media `json:"media,omitempty"`
}

// Room 是一个飞书会话的记录，给界面看。
type Room struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind,omitempty"`
	AgentID   string `json:"agentId,omitempty"`
	AgentName string `json:"agentName,omitempty"`
	BotID     string `json:"botId,omitempty"`
	// BotName 是飞书后台配置的机器人名字，界面显示发送人时用它。
	BotName string `json:"botName,omitempty"`
	// ReadOnly 表示机器人不在这个群里，只能手动读取，不会自动同步也不会回答。
	ReadOnly bool `json:"readOnly,omitempty"`
	// Mode 是群级三态：off / mention / active。空表示沿用全局旧配置。
	Mode string `json:"mode,omitempty"`
	// MuteUntil 是静音截止时间（Unix 秒），0 表示未静音。
	MuteUntil int64  `json:"muteUntil,omitempty"`
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
	Name      string
	Kind      string
	AgentID   string
	BotID     string
	ReadOnly  bool
	Mode      string
	MuteUntil int64
	Lines     []Line
	Seen      map[string]struct{}
}

func NewLog() *Log {
	return &Log{rooms: map[string]*room{}}
}

// Remember 记下还挂着「停止」的那张卡片。同时可以有多张。进程掉了之后，下次连上再收掉。
func (l *Log) Remember(id string) {
	if l == nil || id == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	path := livePath(l.path)
	if path == "" {
		return
	}
	ids := readLive(path)
	for _, have := range ids {
		if have == id {
			return
		}
	}
	ids = append(ids, id)
	_ = os.WriteFile(path, []byte(strings.Join(ids, "\n")+"\n"), 0o600)
}

// OpenCards 返回还没收掉的卡片。
func (l *Log) OpenCards() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return readLive(livePath(l.path))
}

// Recall 返回还没收掉的第一张卡片。
func (l *Log) Recall() string {
	ids := l.OpenCards()
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// Release 这一张已经改完。还有别的卡片时，文件留下。
func (l *Log) Release(id string) {
	if l == nil || id == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	path := livePath(l.path)
	if path == "" {
		return
	}
	var keep []string
	for _, have := range readLive(path) {
		if have != id {
			keep = append(keep, have)
		}
	}
	if len(keep) == 0 {
		_ = os.Remove(path)
		return
	}
	_ = os.WriteFile(path, []byte(strings.Join(keep, "\n")+"\n"), 0o600)
}

// Forget 全部卡片都已经改完，不再补收。
func (l *Log) Forget() {
	if l == nil {
		return
	}
	l.mu.Lock()
	path := livePath(l.path)
	l.mu.Unlock()
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

func readLive(path string) []string {
	if path == "" {
		return nil
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var ids []string
	for _, line := range strings.Split(string(buf), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			ids = append(ids, line)
		}
	}
	return ids
}

func livePath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(path), "feishu-live.json")
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
	for _, r := range saved {
		if r.ID == "" {
			continue
		}
		if emptyRoom(r.Name, len(r.Messages)) {
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
		if r.BotID != "" {
			dst.BotID = r.BotID
		}
		if r.ReadOnly {
			dst.ReadOnly = true
		}
		if r.Mode != "" {
			dst.Mode = r.Mode
		}
		if r.MuteUntil != 0 {
			dst.MuteUntil = r.MuteUntil
		}
		for _, line := range r.Messages {
			// 旧版本留下的「有发送人、无内容」空行丢掉，也不记进 Seen，
			// 下次拉取会按新的类型解析重新落库。
			if strings.TrimSpace(line.Text) == "" && len(line.Media) == 0 {
				continue
			}
			// 旧数据把卡片降级文案当成了正文，丢掉重新拉取，能把标题补回来。
			if isCardFallback(line.Text) && (line.Kind == "card" || line.Kind == "") {
				continue
			}
			l.addLocked(line)
		}
	}
	l.mu.Unlock()
	// 顺手把文件整理一遍：旧版本留下的空壳房间、空行不会再写回去。
	l.save()
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

// SetBot 记下这个会话是哪个机器人收到的。
func (l *Log) SetBot(id, botID string) {
	if l == nil || id == "" || strings.TrimSpace(botID) == "" {
		return
	}
	botID = strings.TrimSpace(botID)
	l.mu.Lock()
	r := l.room(id)
	if r.BotID == botID {
		l.mu.Unlock()
		return
	}
	r.BotID = botID
	l.mu.Unlock()
	l.save()
}

// Bot 返回记下这个会话的机器人。空表示还没有。
func (l *Log) Bot(id string) string {
	if l == nil || id == "" {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.rooms[id]
	if r == nil {
		return ""
	}
	return r.BotID
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

// SetReadOnly 记下这个会话机器人不在群里，只能手动读取。
func (l *Log) SetReadOnly(id string, v bool) {
	if l == nil || id == "" {
		return
	}
	l.mu.Lock()
	r := l.rooms[id]
	// 没有记录时不建房间：补历史会遍历用户所在的所有群，不能因此灌出一堆空房间。
	if r == nil || r.ReadOnly == v {
		l.mu.Unlock()
		return
	}
	r.ReadOnly = v
	l.mu.Unlock()
	l.save()
}

// Mode 返回群级三态；空表示还没单独设过，沿用全局旧配置。
func (l *Log) Mode(id string) string {
	if l == nil || id == "" {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.rooms[id]
	if r == nil {
		return ""
	}
	return r.Mode
}

// SetMode 记下群级三态；只接受 off / mention / active。
func (l *Log) SetMode(id, mode string) {
	if l == nil || id == "" {
		return
	}
	switch mode {
	case modeOff, modeMention, modeActive:
	default:
		return
	}
	l.mu.Lock()
	r := l.room(id)
	if r.Mode == mode {
		l.mu.Unlock()
		return
	}
	r.Mode = mode
	l.mu.Unlock()
	l.save()
}

// MuteUntil 返回静音截止时间（Unix 秒）；0 表示未静音。
func (l *Log) MuteUntil(id string) int64 {
	if l == nil || id == "" {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.rooms[id]
	if r == nil {
		return 0
	}
	return r.MuteUntil
}

// SetMuteUntil 记下静音截止时间；传 0 解除静音。
func (l *Log) SetMuteUntil(id string, until int64) {
	if l == nil || id == "" {
		return
	}
	l.mu.Lock()
	r := l.room(id)
	if r.MuteUntil == until {
		l.mu.Unlock()
		return
	}
	r.MuteUntil = until
	l.mu.Unlock()
	l.save()
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
		out = append(out, Room{ID: id, Name: roomTitle(r), Kind: r.Kind, AgentID: r.AgentID, BotID: r.BotID, ReadOnly: r.ReadOnly, Messages: lines})
	}
	return out
}

// emptyRoom 判断一个会话是不是既没名字也没内容。补历史会遍历用户所在的所有群，
// 这种空壳不该出现在会话列表里。
func emptyRoom(name string, lines int) bool {
	return strings.TrimSpace(name) == "" && lines == 0
}

// Prompt 把这个会话最近的对话拼成给模型的正文。
// promptLineLimit 是塞进 prompt 的最大条数。房间历史本身不截断，只截注入的部分。
const promptLineLimit = 40

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
		b.WriteString("」。这是你和他之前的对话记录。接着上文回答最后一句，不要讨论本应用怎么实现。\n")
	}
	// 只带最近一段。房间历史无上限，全塞进去会撑爆上下文窗口，
	// 把真正有用的指令挤掉。
	if len(lines) > promptLineLimit {
		lines = lines[len(lines)-promptLineLimit:]
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
		if emptyRoom(r.Name, len(r.Lines)) {
			continue
		}
		rooms = append(rooms, Room{ID: id, Name: r.Name, Kind: r.Kind, AgentID: r.AgentID, BotID: r.BotID, ReadOnly: r.ReadOnly, Mode: r.Mode, MuteUntil: r.MuteUntil, Messages: append([]Line(nil), r.Lines...)})
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
