package feishu

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// Piece 是交给本机会话的一段。有 Data 时是一张图的像素。
type Piece struct {
	Text string
	MIME string
	Data []byte
}

// RunRequest 是飞书这条消息要在桌面会话里跑的内容。
type RunRequest struct {
	AgentID string
	Text    string
	Pieces  []Piece
}

// Hooks 是飞书包向外要的事。
type Hooks struct {
	Run     func(ctx context.Context, req RunRequest) (string, error)
	Show    func(ctx context.Context, req RunRequest, reply string) error
	Sees    func(agentID string) bool
	Reply   func(ctx context.Context, appID, secret, messageID, text string) (string, error)
	Working func(ctx context.Context, appID, secret, messageID, text, stopID string) (string, error)
	Patch   func(ctx context.Context, appID, secret, messageID, text, stopID string) error
	Dial    func(ctx context.Context, appID, secret string, on func(Turn), mark func(state, detail string)) error
}

const (
	taskLead    = "这是从飞书来的一件任务。可以用这台电脑上的工具完成。\n"
	workingLine = "已在这台电脑上处理"
)

const (
	blindReply   = "这个会话还不能看图"
	missingImage = "没拿到图"
)

// Service 维持一条长连接。事件处理只入队，模型调用在 worker 里。
type Service struct {
	hooks Hooks

	mu      sync.Mutex
	cfg     Config
	cancel  context.CancelFunc
	gen     int
	state   string
	detail  string
	gate    Gate
	holds   map[string]held
	log     *Log
	botID   string
	history func(ctx context.Context, appID, secret, chatID string, since int64) ([]Line, error)
	load    func(ctx context.Context, appID, secret, messageID string) ([]Item, error)
	grab    func(ctx context.Context, cfg Config, messageID, key string) ([]byte, string, error)

	once      sync.Once
	jobs      chan Turn
	runCancel context.CancelFunc
	runAgent  string
	runKey    string
	cardID    string
	closed    bool
}

// Config 是当前生效的机器人配置。
type Config struct {
	Enabled   bool
	AppID     string
	AppSecret string
	AgentID   string
	// MentionOnly 为真时，群里只有被 @ 或被回复才回答。
	MentionOnly bool
	// UserAccess 是用户授权后的令牌。空表示还没有授权。
	UserAccess string
}

// New 构造服务。Dial 为空时 Apply 不会去连飞书。
func New(h Hooks) *Service {
	return &Service{hooks: h, state: "stopped", log: NewLog()}
}

// SetLog 把飞书会话记录落到 path。
func (s *Service) SetLog(path string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.log == nil {
		s.log = NewLog()
	}
	book := s.log
	s.mu.Unlock()
	book.SetPath(path)
}

// Rooms 返回飞书会话记录。
func (s *Service) Rooms() []Room {
	if s == nil || s.log == nil {
		return nil
	}
	return s.log.Rooms()
}

// View 是给界面看的连接状态。
type View struct {
	State  string
	Detail string
}

// Status 返回连接状态。stopped / connecting / connected / error。
func (s *Service) Status() View {
	if s == nil {
		return View{State: "stopped"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.state
	if state == "" {
		state = "stopped"
	}
	return View{State: state, Detail: s.detail}
}

// Apply 按配置拉起或停掉长连接。
func (s *Service) Apply(cfg Config) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.cfg = cfg
	if !cfg.Enabled || cfg.AppID == "" || cfg.AppSecret == "" || cfg.AgentID == "" {
		s.state = "stopped"
		s.detail = ""
		if cfg.Enabled {
			s.state = "error"
			s.detail = "填写 App ID、App Secret，并选择一个会话"
		}
		s.mu.Unlock()
		return
	}
	if s.hooks.Dial == nil {
		s.state = "error"
		s.detail = "没有长连接"
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.gen++
	gen := s.gen
	s.state = "connecting"
	s.detail = ""
	appID, secret := cfg.AppID, cfg.AppSecret
	s.mu.Unlock()

	s.start()
	go s.run(ctx, gen, appID, secret)
}

func (s *Service) run(ctx context.Context, gen int, appID, secret string) {
	err := s.hooks.Dial(ctx, appID, secret, func(t Turn) {
		if t.Action == "stop" {
			s.halt(t.ActionID)
			return
		}
		select {
		case s.jobs <- t:
		default:
			go s.send(appID, secret, t.MessageID, "正在处理前面的消息，这条先放下了")
		}
	}, func(state, detail string) {
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		s.state = state
		s.detail = detail
		s.mu.Unlock()
		if state == "connected" {
			go s.catchUp(ctx, appID, secret)
		}
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen {
		return
	}
	s.cancel = nil
	if ctx.Err() != nil {
		s.state = "stopped"
		s.detail = ""
		return
	}
	s.state = "error"
	if err != nil {
		s.detail = err.Error()
	}
}

func (s *Service) start() {
	s.once.Do(func() {
		s.jobs = make(chan Turn, 32)
		go s.loop()
	})
}

// StopRun 停掉这个会话正在为飞书写的那一轮，不再继续请求模型。
func (s *Service) StopRun(agentID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.runAgent != agentID && s.cfg.AgentID != agentID {
		s.mu.Unlock()
		return
	}
	cancel := s.runCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Service) loop() {
	for t := range s.jobs {
		s.dispatch(t)
	}
}

func (s *Service) dispatch(t Turn) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if t.Action != "" {
		s.onAction(cfg, t)
		return
	}
	if t.Joined {
		s.arrive(cfg, t)
		return
	}
	fresh := s.log != nil && t.ChatID != "" && !s.log.Known(t.ChatID)
	s.noteLine(t)
	if fresh {
		go s.fill(cfg, t.ChatID, 0)
	}
	if t.ChatType == "group" && s.fromSelf(t) {
		return
	}
	if t.ChatType == "group" && cfg.MentionOnly && !s.spokenTo(t) {
		return
	}
	s.mu.Lock()
	d := s.gate.Decide(t)
	s.mu.Unlock()
	if d.Reply != "" {
		s.send(cfg.AppID, cfg.AppSecret, t.MessageID, d.Reply)
		return
	}
	if !d.Pass {
		return
	}
	s.expand(cfg, &t)
	pieces, miss := s.attach(cfg, t)
	if miss != "" {
		s.note(cfg, t, textPieces(t), miss)
		return
	}
	if s.hooks.Run == nil {
		s.send(cfg.AppID, cfg.AppSecret, t.MessageID, "本机会话没有接上")
		return
	}
	text := taskLead + joinPieces(pieces)
	if t.ChatType == "group" && s.log != nil && t.ChatID != "" {
		if framed := s.log.Prompt(t.ChatID, t.ChatName, !cfg.MentionOnly); framed != "" {
			text = taskLead + framed
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cardID := s.sendWorking(cfg.AppID, cfg.AppSecret, t.MessageID, workingLine, t.MessageID)
	agentID := s.AgentFor(t.ChatID)
	s.mu.Lock()
	s.closed = false
	s.cardID = cardID
	s.runKey = t.MessageID
	s.runCancel = cancel
	s.runAgent = agentID
	s.mu.Unlock()
	reply, err := s.hooks.Run(ctx, RunRequest{AgentID: agentID, Text: text, Pieces: pieces})
	final := reply
	if ctx.Err() != nil {
		final = "已停止"
	} else if err != nil {
		final = err.Error()
	} else if final == "" {
		final = "没有给出文字回复"
	}
	s.mu.Lock()
	s.closed = true
	s.cardID = ""
	s.runKey = ""
	s.runAgent = ""
	s.runCancel = nil
	s.mu.Unlock()
	if cardID == "" {
		s.send(cfg.AppID, cfg.AppSecret, t.MessageID, final)
	} else {
		s.paint(cfg, cardID, final, "")
	}
	if s.log != nil && t.ChatID != "" {
		id := cardID
		if id == "" {
			id = "self:" + t.MessageID
		}
		s.log.Add(Line{ID: id, ChatID: t.ChatID, From: "你", Text: final, At: time.Now().Unix(), Self: true})
	}
}

func (s *Service) halt(id string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	cancel := s.runCancel
	ok := id == s.runKey && cancel != nil
	s.mu.Unlock()
	if ok {
		cancel()
	}
}

// Progress 把当前工具收成一句，写进正在处理的那张卡片。
func (s *Service) Progress(agentID, tool string) {
	if s == nil || agentID == "" {
		return
	}
	s.mu.Lock()
	if s.closed || s.runAgent != agentID || s.cardID == "" {
		s.mu.Unlock()
		return
	}
	cfg := s.cfg
	card := s.cardID
	key := s.runKey
	s.mu.Unlock()
	s.paint(cfg, card, toolLine(tool), key)
}

// NoteTool 接收会话的工具事件。只在开始时更新卡片。
func (s *Service) NoteTool(data any) {
	m, ok := data.(map[string]any)
	if !ok || m["phase"] != "start" {
		return
	}
	id, _ := m["id"].(string)
	tool, _ := m["tool"].(string)
	s.Progress(id, tool)
}

func toolLine(name string) string {
	switch name {
	case "bash":
		return "正在跑命令"
	case "read_file":
		return "正在读文件"
	case "write_file", "edit_file":
		return "正在改文件"
	case "web":
		return "正在看页面"
	default:
		return "正在处理"
	}
}

func (s *Service) note(cfg Config, t Turn, pieces []Piece, reply string) {
	s.send(cfg.AppID, cfg.AppSecret, t.MessageID, reply)
	if s.hooks.Show == nil {
		return
	}
	_ = s.hooks.Show(context.Background(), RunRequest{AgentID: cfg.AgentID, Text: joinPieces(pieces), Pieces: pieces}, reply)
}

func (s *Service) canSee(agentID string) bool {
	if s.hooks.Sees == nil {
		return false
	}
	return s.hooks.Sees(agentID)
}

func hasPixel(pieces []Piece) bool {
	for _, p := range pieces {
		if len(p.Data) > 0 {
			return true
		}
	}
	return false
}

func joinPieces(pieces []Piece) string {
	var lines []string
	for _, p := range pieces {
		if strings.TrimSpace(p.Text) != "" {
			lines = append(lines, p.Text)
		}
	}
	return strings.Join(lines, "\n")
}

func textPieces(t Turn) []Piece {
	var out []Piece
	if len(t.Parts) == 0 {
		if strings.TrimSpace(t.Text) != "" {
			out = append(out, Piece{Text: t.Text})
		}
		return out
	}
	for _, p := range t.Parts {
		if p.Key == "" && strings.TrimSpace(p.Text) != "" {
			out = append(out, Piece{Text: p.Text})
		}
	}
	return out
}

type held struct {
	messageID string
}

func (s *Service) onAction(cfg Config, t Turn) {
	s.mu.Lock()
	h, ok := s.holds[t.ActionID]
	if ok {
		delete(s.holds, t.ActionID)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	s.send(cfg.AppID, cfg.AppSecret, h.messageID, "已取消")
}

func (s *Service) expand(cfg Config, t *Turn) {
	if t.MsgType == "merge_forward" {
		items, err := s.pull(cfg, t.MessageID)
		if err != nil || len(items) == 0 {
			t.Parts = nil
			t.Text = ""
			t.Images = nil
			t.MsgType = "merge_forward"
			return
		}
		var parts []Part
		for _, it := range items {
			if it.Kind == "merge_forward" {
				continue
			}
			parts = append(parts, stampParts(readParts(it.Kind, it.Body, nil), it.ID, t.MessageID)...)
		}
		t.Parts = parts
		t.Text, t.Images = partsText(parts)
	}
	if t.ParentID == "" || t.ParentID == t.MessageID {
		return
	}
	items, err := s.pull(cfg, t.ParentID)
	if err != nil || len(items) == 0 {
		t.Parts = append([]Part{{Text: "用户在回复一条更早的消息，引用内容没有读到。"}}, t.Parts...)
		return
	}
	var pre []Part
	pre = append(pre, Part{Text: "用户在回复下面这条消息："})
	for _, it := range items {
		pre = append(pre, stampParts(readParts(it.Kind, it.Body, nil), it.ID, t.ParentID)...)
	}
	pre = append(pre, Part{Text: "用户说："})
	t.Parts = append(pre, t.Parts...)
	t.Text, t.Images = partsText(t.Parts)
}

func stampParts(parts []Part, id, fallback string) []Part {
	if id == "" {
		id = fallback
	}
	for i := range parts {
		if parts[i].Key != "" && parts[i].MessageID == "" {
			parts[i].MessageID = id
		}
	}
	return parts
}

func (s *Service) pull(cfg Config, messageID string) ([]Item, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if s.load != nil {
		return s.load(ctx, cfg.AppID, cfg.AppSecret, messageID)
	}
	return FetchItems(ctx, cfg.AppID, cfg.AppSecret, messageID)
}

func (s *Service) attach(cfg Config, t Turn) ([]Piece, string) {
	parts := t.Parts
	if len(parts) == 0 {
		if t.MsgType == "merge_forward" {
			return nil, missingImage
		}
		if strings.TrimSpace(t.Text) != "" {
			parts = append(parts, Part{Text: t.Text})
		}
		for _, key := range t.Images {
			parts = append(parts, Part{Key: key})
		}
	}
	var keys int
	for _, p := range parts {
		if p.Key != "" {
			keys++
		}
	}
	if keys > 8 {
		return nil, missingImage
	}
	var out []Piece
	n := 0
	var miss []string
	for _, p := range parts {
		if p.Key == "" {
			if strings.TrimSpace(p.Text) != "" {
				out = append(out, Piece{Text: p.Text})
			}
			continue
		}
		n++
		mid := p.MessageID
		if mid == "" {
			mid = t.MessageID
		}
		raw, mime, err := s.download(cfg, mid, p.Key)
		if err != nil || len(raw) == 0 {
			miss = append(miss, fmt.Sprintf("第%d张图没拿到", n))
			continue
		}
		out = append(out, Piece{MIME: mime, Data: raw})
	}
	if len(miss) > 0 {
		return nil, missingImage + "\n" + strings.Join(miss, "\n")
	}
	return out, ""
}

func (s *Service) download(cfg Config, messageID, key string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if s.grab != nil {
		return s.grab(ctx, cfg, messageID, key)
	}
	raw, mime, err := FetchImage(ctx, cfg.AppID, cfg.AppSecret, messageID, key)
	if err == nil {
		return raw, mime, nil
	}
	if cfg.UserAccess == "" {
		return nil, "", err
	}
	return FetchImageToken(ctx, cfg.UserAccess, messageID, key)
}

func (s *Service) sendWorking(appID, secret, messageID, text, stopID string) string {
	if messageID == "" || text == "" {
		return ""
	}
	if s.hooks.Working == nil {
		return s.send(appID, secret, messageID, text)
	}
	id, err := s.hooks.Working(context.Background(), appID, secret, messageID, text, stopID)
	if err != nil {
		log.Printf("feishu card: %v", err)
		return ""
	}
	return id
}

func (s *Service) paint(cfg Config, cardID, text, stopID string) {
	if cardID == "" || text == "" {
		return
	}
	if s.hooks.Patch == nil {
		s.send(cfg.AppID, cfg.AppSecret, cardID, text)
		return
	}
	if err := s.hooks.Patch(context.Background(), cfg.AppID, cfg.AppSecret, cardID, text, stopID); err != nil {
		log.Printf("feishu card: %v", err)
	}
}

func (s *Service) send(appID, secret, messageID, text string) string {
	if s.hooks.Reply == nil || messageID == "" || text == "" {
		return ""
	}
	id, err := s.hooks.Reply(context.Background(), appID, secret, messageID, text)
	if err != nil {
		log.Printf("feishu reply: %v", err)
		return ""
	}
	return id
}

func (s *Service) noteLine(t Turn) {
	if s.log == nil || t.ChatID == "" || t.MessageID == "" {
		return
	}
	from := t.Sender
	if from == "" || from == t.SenderID || strings.HasPrefix(from, "ou_") {
		from = "有人"
	}
	at := t.At
	if at == 0 {
		at = time.Now().Unix()
	}
	text := t.Text
	if text == "" && len(t.Images) > 0 {
		text = "［图片］"
	}
	s.log.Add(Line{ID: t.MessageID, ChatID: t.ChatID, From: from, FromID: t.SenderID, Text: text, At: at})
	s.log.Kind(t.ChatID, t.ChatType)
	if t.ChatType == "p2p" && from != "" && from != "有人" && t.ChatName == "" {
		s.log.Name(t.ChatID, from)
	} else if t.ChatName != "" {
		s.log.Name(t.ChatID, t.ChatName)
	}
}

// AgentFor 这个飞书会话下一条消息交给谁。没有单独指定时用默认会话。
func (s *Service) AgentFor(chatID string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	def := s.cfg.AgentID
	s.mu.Unlock()
	if s.log != nil {
		if id := s.log.Agent(chatID); id != "" {
			return id
		}
	}
	return def
}

// BindRoom 从下一条消息起，这个飞书群或私聊改由另一个本机会话回答。
func (s *Service) BindRoom(chatID, agentID string) {
	if s == nil || s.log == nil {
		return
	}
	s.log.Bind(chatID, agentID)
}

func (s *Service) spokenTo(t Turn) bool {
	if s.log != nil && s.log.Self(t.ParentID) {
		return true
	}
	s.mu.Lock()
	id := s.botID
	s.mu.Unlock()
	for _, m := range t.Mentions {
		if !m.Bot {
			continue
		}
		if id == "" || m.OpenID == "" || m.OpenID == id {
			return true
		}
	}
	return false
}

func (s *Service) fromSelf(t Turn) bool {
	if t.SenderType == "app" || t.SenderType == "bot" {
		return true
	}
	s.mu.Lock()
	id := s.botID
	s.mu.Unlock()
	return id != "" && t.SenderID != "" && t.SenderID == id
}

func (s *Service) arrive(cfg Config, t Turn) {
	if s.log != nil {
		s.log.Name(t.ChatID, t.ChatName)
	}
	s.fill(cfg, t.ChatID, 0)
}

func (s *Service) catchUp(ctx context.Context, appID, secret string) {
	s.ensureBot(ctx, appID, secret)
	if s.log == nil {
		return
	}
	cfg := Config{AppID: appID, AppSecret: secret}
	for _, room := range s.log.Chats() {
		var since int64
		if len(room.Messages) > 0 {
			since = room.Messages[0].At
		}
		s.fill(cfg, room.ID, since)
	}
}

func (s *Service) ensureBot(ctx context.Context, appID, secret string) {
	s.mu.Lock()
	have := s.botID
	s.mu.Unlock()
	if have != "" || appID == "" {
		return
	}
	id, err := BotOpenID(ctx, appID, secret)
	if err != nil {
		log.Printf("feishu bot: %v", err)
		return
	}
	s.mu.Lock()
	s.botID = id
	s.mu.Unlock()
}

func (s *Service) fill(cfg Config, chatID string, since int64) {
	if s.log == nil || chatID == "" || cfg.AppID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pull := s.history
	if pull == nil {
		pull = History
	}
	lines, err := pull(ctx, cfg.AppID, cfg.AppSecret, chatID, since)
	if err != nil {
		log.Printf("feishu history %s: %v", chatID, err)
		return
	}
	for _, line := range lines {
		if line.At == 0 {
			line.At = time.Now().Unix()
		}
		s.log.Add(line)
	}
}
