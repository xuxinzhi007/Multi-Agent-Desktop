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
	// React 给用户刚发的那条消息加表情。处理还没结束时先让对方看见标记。
	React func(ctx context.Context, appID, secret, messageID, emoji string) error
	// Claim 给这条消息挑一个会话。busy 是还在答飞书的会话。
	// 主会话空闲时返回它。都在忙时返回内容为 busy 的错误，调用方等一轮结束再要。
	Claim func(parentID string, busy map[string]bool) (string, error)
}

// liveRun 是一条飞书消息正在由哪个会话回答。多条消息各有一轮。
type liveRun struct {
	agent  string
	card   string
	key    string
	cancel context.CancelFunc
	lines  []string
	say    string
	closed bool
}

const (
	taskLead    = "这是从飞书来的一件任务。可以用这台电脑上的工具完成。\n"
	workingLine = "已在这台电脑上处理"
	repliedLine = "已回复"
	// processingEmoji 是飞书表情类型。Typing 是「敲键盘」，表示这条还在处理。
	processingEmoji = "Typing"
)

const (
	blindReply   = "这个会话还不能看图"
	missingImage = "没拿到图"
)

// Service 维持一条长连接。事件处理只入队，模型调用在 worker 里。
type Service struct {
	hooks Hooks

	mu         sync.Mutex
	cfg        Config
	cancel     context.CancelFunc
	gen        int
	state      string
	detail     string
	gate       Gate
	holds      map[string]held
	log        *Log
	botID      string
	owner      string
	history    func(ctx context.Context, appID, secret, chatID string, since int64) ([]Line, error)
	listChats  func(ctx context.Context, appID, secret string) ([]listedChat, error)
	scopes     func(ctx context.Context, appID, secret string) (map[string]bool, error)
	stops      func(ctx context.Context, appID, secret, chatID string) ([]string, error)
	scopeEvery time.Duration
	load       func(ctx context.Context, appID, secret, messageID string) ([]Item, error)
	grab       func(ctx context.Context, cfg Config, messageID, key string) ([]byte, string, error)

	once  sync.Once
	jobs  chan Turn
	runs  map[string]*liveRun
	wake  *sync.Cond
	epoch int
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

// ShareLog 让多个机器人共用一份会话记录。
func (s *Service) ShareLog(book *Log) {
	if s == nil || book == nil {
		return
	}
	s.mu.Lock()
	s.log = book
	s.mu.Unlock()
}

// SetOwner 记下这份连接属于哪个机器人，写进会话记录。
func (s *Service) SetOwner(id string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.owner = id
	s.mu.Unlock()
}

// Current 返回当前生效的配置。
func (s *Service) Current() Config {
	if s == nil {
		return Config{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
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
	// 飞书要求收到事件后 3 秒内返回。这里只入队，模型调用在 loop 里。
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
			s.dropRun()
			return
		}
		s.mu.Lock()
		s.state = state
		s.detail = detail
		s.mu.Unlock()
		if state == "connected" {
			go s.closeStale(appID, secret)
			go s.watchScopes(ctx, appID, secret)
		} else {
			s.dropRun()
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
	run := s.runs[agentID]
	var cancel context.CancelFunc
	if run != nil {
		cancel = run.cancel
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Service) loop() {
	for t := range s.jobs {
		t := t
		if t.Action != "" || t.Joined {
			s.dispatch(t)
			continue
		}
		go s.dispatch(t)
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
	if fresh && !strings.HasPrefix(t.EventID, "catch-") {
		go s.replayChat(cfg, listedChat{ID: t.ChatID, Name: t.ChatName, Kind: t.ChatType}, time.Now().Unix())
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
	s.markSeen(cfg, t.MessageID)
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
	parent := s.AgentFor(t.ChatID)
	run, pickErr := s.reserve(parent, t.MessageID, cancel)
	if pickErr != nil {
		s.send(cfg.AppID, cfg.AppSecret, t.MessageID, pickErr.Error())
		return
	}
	defer s.release(run)
	cardID := s.sendWorking(cfg.AppID, cfg.AppSecret, t.MessageID, workingLine, t.MessageID)
	if cardID != "" && s.log != nil {
		s.log.Remember(cardID)
	}
	s.mu.Lock()
	run.card = cardID
	s.mu.Unlock()
	agentID := run.agent
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
	run.closed = true
	cardID = run.card
	s.mu.Unlock()
	sent := ""
	if final != "已停止" {
		sent = s.send(cfg.AppID, cfg.AppSecret, t.MessageID, withMentions(t, final))
	}
	if cardID == "" {
		// 没有进度卡片时，上面这条新消息就是回复。
	} else if sent == "" {
		if s.paint(cfg, cardID, final, "") == nil && s.log != nil {
			s.log.Release(cardID)
		}
	} else if s.paint(cfg, cardID, repliedLine, "") == nil && s.log != nil {
		s.log.Release(cardID)
	}
	if s.log != nil && t.ChatID != "" {
		id := sent
		if id == "" {
			id = cardID
		}
		if id == "" {
			id = "self:" + t.MessageID
		}
		s.log.Add(Line{ID: id, ChatID: t.ChatID, From: "你", Text: final, At: time.Now().Unix(), Self: true})
	}
}

// withMentions 在群里的最终回复前 @ 提问的人，以及这条消息里点到的其他成员。
// 进度仍改原来的卡片。答完另发一条，群里才会有提醒。
func withMentions(t Turn, text string) string {
	if t.ChatType != "group" {
		return text
	}
	var tags []string
	seen := map[string]bool{}
	add := func(id string) {
		if !openID(id) || seen[id] {
			return
		}
		seen[id] = true
		tags = append(tags, "<at id="+id+"></at>")
	}
	if t.SenderType == "" || t.SenderType == "user" {
		add(t.SenderID)
	}
	for _, m := range t.Mentions {
		if m.Bot {
			continue
		}
		add(m.OpenID)
	}
	if len(tags) == 0 {
		return text
	}
	return strings.Join(tags, " ") + "\n" + text
}

func openID(id string) bool {
	if len(id) < 4 || !strings.HasPrefix(id, "ou_") {
		return false
	}
	for _, r := range id[3:] {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		default:
			return false
		}
	}
	return true
}

func (s *Service) halt(id string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	var run *liveRun
	for _, cur := range s.runs {
		if cur.key == id && cur.cancel != nil {
			run = cur
			break
		}
	}
	if run == nil {
		s.mu.Unlock()
		return
	}
	run.closed = true
	cancel := run.cancel
	cardID := run.card
	cfg := s.cfg
	s.mu.Unlock()
	cancel()
	if cardID != "" && s.paint(cfg, cardID, "已停止", "") == nil && s.log != nil {
		s.log.Release(cardID)
	}
}

const droppedLine = "服务已经停了，这一轮没有做完"

// dropRun 连接不在时，把还挂着停止按钮的卡片收掉，并停掉这一轮。
func (s *Service) dropRun() {
	if s == nil {
		return
	}
	s.mu.Lock()
	cfg := s.cfg
	var runs []*liveRun
	for _, run := range s.runs {
		if run.closed {
			continue
		}
		run.closed = true
		runs = append(runs, run)
	}
	s.epoch++
	if s.wake != nil {
		s.wake.Broadcast()
	}
	s.mu.Unlock()
	for _, run := range runs {
		if run.cancel != nil {
			run.cancel()
		}
		if run.card != "" && s.paint(cfg, run.card, droppedLine, "") == nil && s.log != nil {
			s.log.Release(run.card)
		}
	}
}

// closeStale 上次进程停掉时没来得及改的卡片，连上后补成已结束。
func (s *Service) closeStale(appID, secret string) {
	if s == nil || s.log == nil {
		return
	}
	s.mu.Lock()
	open := map[string]bool{}
	for _, run := range s.runs {
		if run.card != "" {
			open[run.card] = true
		}
	}
	s.mu.Unlock()
	cfg := Config{AppID: appID, AppSecret: secret}
	for _, id := range s.log.OpenCards() {
		if id == "" || open[id] {
			continue
		}
		if s.paint(cfg, id, droppedLine, "") == nil {
			s.log.Release(id)
		}
	}
	s.closeHung(appID, secret, open)
}

func (s *Service) closeHung(appID, secret string, open map[string]bool) {
	if s.log == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pull := s.stops
	if pull == nil {
		pull = OpenStops
	}
	cfg := Config{AppID: appID, AppSecret: secret}
	for _, room := range s.log.Rooms() {
		ids, err := pull(ctx, appID, secret, room.ID)
		if err != nil {
			log.Printf("feishu stuck %s: %v", room.ID, err)
			continue
		}
		for _, id := range ids {
			if id == "" || open[id] {
				continue
			}
			_ = s.paint(cfg, id, droppedLine, "")
		}
	}
}

// Progress 把当前工具收成一句，追加到正在处理的那张卡片。
func (s *Service) Progress(agentID, tool string) {
	s.addLine(agentID, toolLine(tool))
}

// NoteDelta 把模型已经说出的句子写进卡片，不等整轮结束。
func (s *Service) NoteDelta(data any) {
	m, ok := data.(map[string]any)
	if !ok {
		return
	}
	id, _ := m["id"].(string)
	text, _ := m["delta"].(string)
	if id == "" || text == "" {
		return
	}
	s.mu.Lock()
	run := s.runs[id]
	if run == nil || run.closed || run.card == "" {
		s.mu.Unlock()
		return
	}
	run.say += text
	var ready []string
	for {
		i := strings.IndexAny(run.say, "\n。")
		if i < 0 {
			break
		}
		ready = append(ready, run.say[:i+1])
		run.say = run.say[i+1:]
	}
	s.mu.Unlock()
	for _, line := range ready {
		s.addLine(id, line)
	}
}

// NoteTool 接收会话的工具事件。开始时写正在做什么，结束时把这一步的结果写上。
func (s *Service) NoteTool(data any) {
	m, ok := data.(map[string]any)
	if !ok {
		return
	}
	id, _ := m["id"].(string)
	tool, _ := m["tool"].(string)
	if m["phase"] == "start" {
		s.flushSay(id)
		s.Progress(id, tool)
		return
	}
	if m["phase"] != "end" {
		return
	}
	content, _ := m["content"].(string)
	s.addLine(id, resultLine(tool, content))
}

func (s *Service) flushSay(agentID string) {
	s.mu.Lock()
	run := s.runs[agentID]
	text := ""
	if run != nil {
		text = strings.TrimSpace(run.say)
		run.say = ""
	}
	s.mu.Unlock()
	if text != "" {
		s.addLine(agentID, text)
	}
}

func (s *Service) addLine(agentID, line string) {
	if s == nil || agentID == "" {
		return
	}
	line = clipLine(line)
	if line == "" {
		return
	}
	s.mu.Lock()
	run := s.runs[agentID]
	if run == nil || run.closed || run.card == "" {
		s.mu.Unlock()
		return
	}
	run.lines = append(run.lines, line)
	if len(run.lines) > 8 {
		run.lines = run.lines[len(run.lines)-8:]
	}
	text := strings.Join(run.lines, "\n")
	cfg := s.cfg
	card := run.card
	key := run.key
	s.mu.Unlock()
	s.paint(cfg, card, text, key)
}

func busyClaim(err error) bool {
	return err != nil && err.Error() == "busy"
}

// reserve 占住一个还没在答飞书的会话。主会话忙时由 Claim 交给它的助手。
func (s *Service) reserve(parent, key string, cancel context.CancelFunc) (*liveRun, error) {
	for {
		s.mu.Lock()
		busy := map[string]bool{}
		for id, run := range s.runs {
			if run != nil {
				busy[id] = true
			}
		}
		claim := s.hooks.Claim
		seen := s.epoch
		s.mu.Unlock()

		var id string
		if claim == nil {
			if busy[parent] {
				s.waitEpoch(seen)
				continue
			}
			id = parent
		} else {
			got, err := claim(parent, busy)
			if busyClaim(err) || (err == nil && (got == "" || busy[got])) {
				s.waitEpoch(seen)
				continue
			}
			if err != nil {
				return nil, err
			}
			id = got
		}

		s.mu.Lock()
		if s.runs[id] != nil {
			s.mu.Unlock()
			continue
		}
		if s.runs == nil {
			s.runs = map[string]*liveRun{}
		}
		run := &liveRun{agent: id, key: key, cancel: cancel}
		s.runs[id] = run
		s.mu.Unlock()
		return run, nil
	}
}

func (s *Service) waitEpoch(seen int) {
	s.mu.Lock()
	if s.wake == nil {
		s.wake = sync.NewCond(&s.mu)
	}
	for s.epoch == seen {
		s.wake.Wait()
	}
	s.mu.Unlock()
}

func (s *Service) release(run *liveRun) {
	if s == nil || run == nil {
		return
	}
	s.mu.Lock()
	if cur := s.runs[run.agent]; cur == run {
		delete(s.runs, run.agent)
	}
	s.epoch++
	if s.wake != nil {
		s.wake.Broadcast()
	}
	s.mu.Unlock()
}

func clipLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}

func resultLine(tool, content string) string {
	content = clipLine(content)
	if content == "" {
		return ""
	}
	switch tool {
	case "bash":
		return "命令结果 " + content
	case "read_file":
		return "读到 " + content
	case "web":
		return "页面 " + content
	default:
		return content
	}
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

func (s *Service) markSeen(cfg Config, messageID string) {
	if s.hooks.React == nil || messageID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := s.hooks.React(ctx, cfg.AppID, cfg.AppSecret, messageID, processingEmoji); err != nil {
		s.noteFault(err)
	}
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
		s.noteFault(err)
		return ""
	}
	return id
}

func (s *Service) paint(cfg Config, cardID, text, stopID string) error {
	if cardID == "" || text == "" {
		return nil
	}
	if s.hooks.Patch == nil {
		s.send(cfg.AppID, cfg.AppSecret, cardID, text)
		return nil
	}
	if err := s.hooks.Patch(context.Background(), cfg.AppID, cfg.AppSecret, cardID, text, stopID); err != nil {
		log.Printf("feishu card: %v", err)
		return err
	}
	return nil
}

func (s *Service) send(appID, secret, messageID, text string) string {
	if s.hooks.Reply == nil || messageID == "" || text == "" {
		return ""
	}
	id, err := s.hooks.Reply(context.Background(), appID, secret, messageID, text)
	if err != nil {
		s.noteFault(err)
		return ""
	}
	return id
}

func (s *Service) watchScopes(ctx context.Context, appID, secret string) {
	wait := s.scopeEvery
	if wait <= 0 {
		wait = 20 * time.Second
	}
	pulled := ""
	blind := false
	for {
		got, err := s.loadScopes(ctx, appID, secret)
		if err != nil {
			log.Printf("feishu scopes: %v", err)
			if !blind {
				s.catchUp(ctx, appID, secret)
				blind = true
			}
		} else if historyReady(got) {
			stamp := scopeStamp(got)
			if stamp != pulled {
				s.catchUp(ctx, appID, secret)
				pulled = stamp
			}
			s.mu.Lock()
			miss := MissingScopes(got)
			if len(miss) == 0 && strings.HasPrefix(s.detail, "还缺这些权限") {
				s.detail = ""
			} else if len(miss) > 0 {
				s.detail = scopeDetail(miss)
			}
			s.mu.Unlock()
		} else {
			s.mu.Lock()
			s.detail = scopeDetail(MissingScopes(got))
			s.mu.Unlock()
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Service) loadScopes(ctx context.Context, appID, secret string) (map[string]bool, error) {
	if s.scopes != nil {
		return s.scopes(ctx, appID, secret)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return AppScopes(ctx, appID, secret)
}

func (s *Service) noteFault(err error) {
	if err == nil {
		return
	}
	log.Printf("feishu: %v", err)
	msg := err.Error()
	if strings.Contains(msg, "Access denied") || strings.Contains(msg, "权限") {
		msg = "飞书没给这个应用发消息和标表情的权限。开通「获取与发送单聊、群组消息」并发布版本后，会自己把还没回复的消息拉下来。"
	}
	s.mu.Lock()
	s.detail = msg
	s.mu.Unlock()
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
	s.mu.Lock()
	owner := s.owner
	s.mu.Unlock()
	if owner != "" {
		s.log.SetBot(t.ChatID, owner)
	}
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
	s.replayChat(cfg, listedChat{ID: t.ChatID, Name: t.ChatName, Kind: "group"}, time.Now().Unix())
}

const catchWindow = 7 * 24 * time.Hour

// catchUp 把已经见过的会话，以及机器人所在的群里还没回复的消息补进队列。
// 权限是后来才有的时候也会再跑一遍，不必等一个新会话。
// 飞书群列表不含单聊；从没进过本机记录的单聊，仍然要等这个会话里的下一条。
func (s *Service) catchUp(ctx context.Context, appID, secret string) {
	s.ensureBot(ctx, appID, secret)
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	cfg.AppID = appID
	cfg.AppSecret = secret
	if s.jobs == nil {
		s.start()
	}
	seen := map[string]listedChat{}
	if s.log != nil {
		for _, room := range s.log.Rooms() {
			seen[room.ID] = listedChat{ID: room.ID, Name: room.Name, Kind: room.Kind}
		}
	}
	pull := s.listChats
	if pull == nil {
		pull = ListChats
	}
	more, err := pull(ctx, appID, secret)
	if err != nil {
		log.Printf("feishu chats: %v", err)
	} else {
		for _, c := range more {
			prev := seen[c.ID]
			if c.Name == "" {
				c.Name = prev.Name
			}
			if c.Kind == "" {
				c.Kind = prev.Kind
			}
			if c.Kind == "" {
				c.Kind = "group"
			}
			seen[c.ID] = c
		}
	}
	now := time.Now().Unix()
	for _, c := range seen {
		s.replayChat(cfg, c, now)
	}
}

func (s *Service) replayChat(cfg Config, c listedChat, now int64) {
	if c.ID == "" || cfg.AppID == "" {
		return
	}
	kind := c.Kind
	if kind == "topic" {
		kind = "group"
	}
	if cfg.MentionOnly && kind == "group" {
		return
	}
	if s.jobs == nil {
		s.start()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pull := s.history
	if pull == nil {
		pull = History
	}
	lines, err := pull(ctx, cfg.AppID, cfg.AppSecret, c.ID, 0)
	if err != nil {
		log.Printf("feishu history %s: %v", c.ID, err)
		return
	}
	if s.log != nil {
		if c.Name != "" {
			s.log.Name(c.ID, c.Name)
		}
		if kind != "" {
			s.log.Kind(c.ID, kind)
		}
		for _, line := range lines {
			if line.ChatID == "" {
				line.ChatID = c.ID
			}
			if line.At == 0 {
				line.At = now
			}
			s.log.Add(line)
		}
	}
	if kind == "" {
		kind = "p2p"
	}
	for _, line := range unanswered(lines, now) {
		t := Turn{
			EventID:    "catch-" + line.ID,
			ChatType:   kind,
			MsgType:    "text",
			Text:       line.Text,
			MessageID:  line.ID,
			ChatID:     c.ID,
			ChatName:   c.Name,
			Sender:     line.From,
			SenderID:   line.FromID,
			SenderType: "user",
			At:         line.At,
		}
		select {
		case s.jobs <- t:
		default:
			log.Printf("feishu catch-up queue full")
			return
		}
	}
}

// unanswered 取最后一条机器人回复之后、还没人接的用户消息。超过 7 天或超过 5 条的不补。
func unanswered(lines []Line, now int64) []Line {
	end := len(lines)
	for end > 0 && !lines[end-1].Self && strings.TrimSpace(lines[end-1].Text) == "" {
		end--
	}
	if end == 0 || lines[end-1].Self {
		return nil
	}
	start := end - 1
	for start > 0 && !lines[start-1].Self {
		start--
	}
	var out []Line
	for _, line := range lines[start:end] {
		if line.Self || strings.TrimSpace(line.Text) == "" || line.ID == "" {
			continue
		}
		out = append(out, line)
	}
	if len(out) > 5 {
		out = out[len(out)-5:]
	}
	if len(out) == 0 {
		return nil
	}
	newest := out[len(out)-1].At
	if newest == 0 {
		newest = now
	}
	if now > 0 && now-newest > int64(catchWindow/time.Second) {
		return nil
	}
	return out
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
