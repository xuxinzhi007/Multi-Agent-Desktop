package feishu

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
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
	// chat 是飞书会话 ID，用来优先复用上次答同一个会话的执行体（保住上下文）。
	// 主会话空闲时返回它。都在忙时返回内容为 busy 的错误，调用方等一轮结束再要。
	Claim func(parentID, chat string, busy map[string]bool) (string, error)
	// Roster 是本机可以接手的会话。/status 和 /use 用它。
	Roster func() []Named
	// Root 把子会话 id 映射到它所属的根会话。审批卡挂在根会话的 run 上，
	// 子会话自己通常没有飞书 run；为空时按原 id 处理。
	Root func(agentID string) string
	// Actions 回复一张带按钮的卡片。批准写操作时用。
	Actions func(ctx context.Context, appID, secret, messageID, text string, buttons []CardButton) (string, error)
	// Decide 把飞书卡片上的允许或拒绝送回审批。
	Decide func(id string, allow, remember bool) error
}

// HistoryMsg 是桥接到频道表的一条历史消息，字段对齐 Line。
type HistoryMsg struct {
	ExtID    string
	FromID   string
	FromName string
	Text     string
	At       int64
	Kind     string
}

// HistorySink 把飞书历史落进频道表。由 API 层接 channel.Manager；
// 飞书包不直接依赖 channel 包，避免反向耦合。
type HistorySink interface {
	// HistorySince 返回这个会话已入库到的游标（Unix 秒）；零值表示从没拉过。
	HistorySince(chatID string) (since int64, lastID string)
	// IngestHistory 只入库一批历史，返回新增条数；不广播、不触发 agent。
	IngestHistory(chatID, name string, msgs []HistoryMsg) int
}

// Named 是一条指令里能点名的本机会话。
type Named struct {
	ID     string
	Name   string
	Status string
}

// CardButton 是飞书卡片上的一个按钮。
type CardButton struct {
	Text  string
	Act   string
	ID    string
	Style string
}

// liveRun 是一条飞书消息正在由哪个会话回答。多条消息各有一轮。
type liveRun struct {
	agent  string
	card   string
	key    string
	chat   string
	cancel context.CancelFunc
	lines  []string
	say    string
	closed bool
}

const (
	taskLead    = "这是从飞书来的一件任务。可以用这台电脑上的工具完成。\n"
	workingLine = "已在这台电脑上处理"
	// processingEmoji 是飞书表情类型。Typing 是「敲键盘」，表示这条还在处理。
	processingEmoji = "Typing"
	// doneEmoji 表示这一轮已经答完。群聊用它在用户原消息上做完成提醒，
	// 不再另发一条带 @ 的新消息冒泡。
	doneEmoji = "DONE"
)

const (
	blindReply   = "这个会话还不能看图"
	missingImage = "没拿到图"
)

// 卡片按钮的 action。stop 是既有停止按钮；retry/expand 是第二批加的
// 重试与展开全文，都复用现有 card.action 通路，不新造协议。
const (
	retryAct  = "retry"
	expandAct = "expand"
)

// 超长答案折叠：超过 foldThreshold 个 runes 时卡片只显示前 foldHeadRunes 字，
// 点「展开全文」再 patch 同一张卡显示全文。阈值按 rune 算，不切半中文。
const (
	foldThreshold = 1500
	foldHeadRunes = 600
)

// 群级三态：off 不理；mention 只 @ 或回复才答（现状）；active 不 @ 也应答。
const (
	modeOff     = "off"
	modeMention = "mention"
	modeActive  = "active"
)

// defaultGroupRoundsPerMinute 是同一个群一分钟内默认最多跑的轮数。
// 取 6：约每 10 秒一轮，够正常问答，又压住连续刷屏；后续可按真机观感调。
const defaultGroupRoundsPerMinute = 6

// groupRateWindow 是限速滑动窗口长度。
const groupRateWindow = time.Minute

// defaultRequeueDelay 是超限消息延后多久重新入队。
const defaultRequeueDelay = 10 * time.Second

// plusOneRe 匹配 +1 / ＋1 这类纯附和，容忍中间空格。
var plusOneRe = regexp.MustCompile(`^[+＋]\s*1$`)

// actionID 把按钮 action 和卡片 id 编进 stopID 通道，传给卡片模板解析。
func actionID(act, id string) string { return act + ":" + id }

// humanError 把底层错误分类映射成一句人话。原始 err.Error() 只进日志和
// 状态详情（noteFault），不直接怼到卡片上。
func humanError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "审批") || strings.Contains(msg, "approval") || strings.Contains(msg, "等待被取消"):
		return "等审批等太久了，已自动拒绝；重试会重新发审批卡"
	case strings.Contains(msg, "预算") || strings.Contains(msg, "budget") || strings.Contains(msg, "用尽") ||
		strings.Contains(msg, "耗尽") || strings.Contains(msg, "轮次") || strings.Contains(msg, "exhaust"):
		return "这轮预算用完了，任务没做完；重试会从头再跑"
	case strings.Contains(msg, "network") || strings.Contains(msg, "connection") || strings.Contains(msg, "dial") ||
		strings.Contains(msg, "refused") || strings.Contains(msg, "no such host") || strings.Contains(msg, "tls") ||
		strings.Contains(msg, "eof") || strings.Contains(msg, "401") || strings.Contains(msg, "403") ||
		strings.Contains(msg, "429") || strings.Contains(msg, "timeout") || strings.Contains(msg, "超时") ||
		strings.Contains(msg, "unauthorized") || strings.Contains(msg, "密钥"):
		return "连不上模型服务，检查网络或密钥后重试"
	case strings.Contains(msg, "verify") || strings.Contains(msg, "tool") || strings.Contains(msg, "工具") ||
		strings.Contains(msg, "命令") || strings.Contains(msg, "bash") || strings.Contains(msg, "exit status") ||
		strings.Contains(msg, "fail") || strings.Contains(msg, "失败") || strings.Contains(msg, "cannot"):
		return "命令跑失败了，卡片里有输出；可重试或换个做法"
	default:
		return "这一轮没跑成，可重试或换个做法"
	}
}

// foldAnswer 超长答案折叠成卡片头部；folded 为假时原样返回。
func foldAnswer(text string) (head string, folded bool) {
	rs := []rune(text)
	if len(rs) <= foldThreshold {
		return text, false
	}
	return string(rs[:foldHeadRunes]) + "…", true
}

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
	botName string
	owner   string
	// sink 把拉到的历史桥接到频道表；nil 时只落 feishu-rooms.json。
	sink       HistorySink
	history    func(ctx context.Context, appID, secret, chatID string, since int64) ([]Line, error)
	listChats  func(ctx context.Context, appID, secret string) ([]listedChat, error)
	scopes     func(ctx context.Context, appID, secret string) (map[string]bool, error)
	stops      func(ctx context.Context, appID, secret, chatID string) ([]string, error)
	scopeEvery time.Duration
	// netDisabled 关掉出站：测试/离线时阻止后台补历史、刷权限、认自己等轮询打外网。
	// 显式手动拉取（SyncRoom）不走这个开关。
	netDisabled bool
	load        func(ctx context.Context, appID, secret, messageID string) ([]Item, error)
	grab        func(ctx context.Context, cfg Config, messageID, key string) ([]byte, string, error)

	once  sync.Once
	jobs  chan Turn
	runs  map[string]*liveRun
	asks  map[string]string
	wake  *sync.Cond
	epoch int

	// groupBusy 标记哪个群正在跑一轮；同群消息排队，不并发跑两轮。
	groupBusy   map[string]bool
	groupCond   *sync.Cond
	groupRounds map[string][]time.Time
	// groupRoundsPerMinute 覆盖默认限速；≤0 用默认值。
	groupRoundsPerMinute int
	// requeueDelay 是超限消息延后重入队的时间。
	requeueDelay time.Duration

	// throttleInterval 是同一张卡两次 patch 的最小间隔；≤0 表示不节流。
	throttleInterval time.Duration
	throttles        map[string]*throttleState
	// retries 存失败轮次的原始消息，重试按钮按卡片 id 取回重新入队。
	retries map[string]Turn
	// fullTexts 存超长答案的全文，展开全文按钮按卡片 id 取回 patch 同一张卡。
	fullTexts map[string]string
}

// throttleState 是单张卡的写卡节流状态：合并被压掉的中间帧，
// 只保留最后一帧待发，避免一秒内把同一张卡 patch 多次。
type throttleState struct {
	mu      sync.Mutex
	last    time.Time
	pending string
	key     string
	cfg     Config
	dirty   bool
	waiting bool
}

// Config 是当前生效的机器人配置。
type Config struct {
	Enabled   bool
	AppID     string
	AppSecret string
	AgentID   string
	// MentionOnly 为真时，群里只有被 @ 或被回复才回答。
	MentionOnly bool
	// AllowChats 是用户手动放行过的群。机器人不在的群默认不自动同步；
	// 按 chat_id 主动读过一次、出现在这里的群才会继续补历史（仍然只读）。
	AllowChats []string
	// OwnerOpenID 是会话 owner。审批卡只认这个人和白名单；为空时一律拒绝。
	OwnerOpenID string
	// ApproverOpenIDs 是额外允许批准写操作的 open_id 白名单。
	ApproverOpenIDs []string
	// UserAccess 是用户授权后的令牌。空表示还没有授权。
	UserAccess string
	// UserRefresh 用来在用户令牌过期时换新的。
	UserRefresh string
	// UserExpire 是用户令牌到期时间（Unix 秒）。
	UserExpire int64
	// OnUserRefresh 在换到新令牌时回调，把结果写回配置。
	OnUserRefresh func(access, refresh string, expire int64)
}

// New 构造服务。Dial 为空时 Apply 不会去连飞书。
func New(h Hooks) *Service {
	return &Service{
		hooks:            h,
		state:            "stopped",
		log:              NewLog(),
		throttleInterval: time.Second,
		requeueDelay:     defaultRequeueDelay,
		// 默认离线：测试构造的服务不会让后台补历史/刷权限打到外网；
		// 生产在 Apply(Config) 校验通过后打开。
		netDisabled: true,
	}
}

// cred 返回当前的调用身份。
//
// 有用户令牌就用用户身份：它按这个人本人在群里的可见范围读消息，
// 不需要应用申请（要审核的）im:message.group_msg。
// 没有就退回应用身份，单聊和发消息都不受影响。
func (s *Service) cred(appID, secret string) Cred {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	c := Cred{
		AppID:       appID,
		Secret:      secret,
		UserToken:   cfg.UserAccess,
		UserRefresh: cfg.UserRefresh,
		UserExpire:  cfg.UserExpire,
		OnRefresh:   cfg.OnUserRefresh,
	}
	if c.AppID == "" {
		c.AppID = cfg.AppID
	}
	if c.Secret == "" {
		c.Secret = cfg.AppSecret
	}
	return c
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

// SetHistorySink 挂上频道表桥接；nil 表示不落频道，只记 feishu-rooms.json。
func (s *Service) SetHistorySink(h HistorySink) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.sink = h
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

// HasBotName 返回从飞书问到的机器人名字。空表示还没问到。
func (s *Service) BotName() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.botName
}

// HasUser 表示这个机器人已经授权过用户身份（能读群消息）。
func (s *Service) HasUser() bool {
	return s != nil && s.Current().UserAccess != ""
}

// Rooms 返回飞书会话记录。
func (s *Service) Rooms() []Room {
	if s == nil || s.log == nil {
		return nil
	}
	return s.log.Rooms()
}

// FetchMedia 用能读消息的身份下载一条消息里的资源（图片/文件）。
func (s *Service) FetchMedia(ctx context.Context, messageID, key, kind string) ([]byte, string, error) {
	if s == nil {
		return nil, "", fmt.Errorf("飞书还没接上")
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	return FetchResource(ctx, s.cred(cfg.AppID, cfg.AppSecret), messageID, key, kind)
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
	s.netDisabled = false
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
			// 先认出自己是谁： spokenTo 判断「群里被 @ 了吗」要用 botID，
			// 不在这里取的话，第一条消息到的时候它还是空的。
			go s.ensureBot(ctx, appID, secret)
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
	s.noteLine(cfg, t)
	if fresh && !strings.HasPrefix(t.EventID, "catch-") {
		go s.replayChat(cfg, listedChat{ID: t.ChatID, Name: t.ChatName, Kind: t.ChatType}, time.Now().Unix())
	}
	if t.ChatType == "group" && s.fromSelf(t) {
		return
	}
	// 其它机器人/系统消息不进队列，任何模式都不替它们答话。
	if t.ChatType == "group" && senderNoise(t) {
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
	// 指令先过门控：/unmute 要能在静音期间恢复，/stop 等也不受三态挡住。
	if reply, ok := s.command(t); ok {
		sent := s.send(cfg.AppID, cfg.AppSecret, t.MessageID, reply)
		if s.log != nil && t.ChatID != "" {
			id := sent
			if id == "" {
				id = "self:" + t.MessageID
			}
			s.log.Add(Line{ID: id, ChatID: t.ChatID, From: "你", Text: reply, At: time.Now().Unix(), Self: true})
		}
		return
	}
	mode := modeMention
	if t.ChatType == "group" {
		mode = s.groupMode(cfg, t.ChatID)
		switch mode {
		case modeOff:
			// off 群不进队列、不回复。
			return
		case modeMention:
			if !s.spokenTo(t) {
				return
			}
		case modeActive:
			if contentNoise(t) {
				return
			}
			if s.muted(t.ChatID) {
				return
			}
		}
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
	// 同群串行：同一时间只跑一轮；限速窗口内超轮次不丢消息，延后重入队。
	if t.ChatType == "group" {
		if !s.groupAllow(t.ChatID) {
			s.send(cfg.AppID, cfg.AppSecret, t.MessageID, "这条我先记下，等前面跑完")
			s.requeueLater(t)
			return
		}
		s.groupAcquire(t.ChatID)
		defer s.groupRelease(t.ChatID)
	}
	text := taskLead + joinPieces(pieces)
	// 群和单聊都要带上本会话的对话记录。
	// 不带的话，连发两条时模型看不到上一条，用户会觉得「接不上」。
	// 单聊用 join=false 的措辞：那里不是「群里接话」，是接着同一段对话。
	if s.log != nil && t.ChatID != "" {
		join := t.ChatType == "group" && mode == modeActive
		if framed := s.log.Prompt(t.ChatID, t.ChatName, join); framed != "" {
			text = taskLead + framed
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	parent := s.AgentFor(t.ChatID)
	run, pickErr := s.reserve(parent, t.MessageID, t.ChatID, cancel)
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
	failed := false
	switch {
	case ctx.Err() != nil:
		final = "已停止"
	case err != nil:
		// 原始错误只进日志和状态详情，卡片上放分类后的人话。
		final = humanError(err)
		failed = true
		s.noteFault(err)
	case final == "":
		final = "没有给出文字回复"
	}
	s.mu.Lock()
	run.closed = true
	cardID = run.card
	s.mu.Unlock()

	// 卡片收尾内容与按钮：失败挂重试，超长答案挂展开全文；都在同一张卡上。
	cardText := final
	cardStop := ""
	if cardID != "" {
		switch {
		case failed:
			s.mu.Lock()
			if s.retries == nil {
				s.retries = map[string]Turn{}
			}
			s.retries[cardID] = t
			s.mu.Unlock()
			cardStop = actionID(retryAct, cardID)
		default:
			if head, folded := foldAnswer(final); folded {
				s.mu.Lock()
				if s.fullTexts == nil {
					s.fullTexts = map[string]string{}
				}
				s.fullTexts[cardID] = final
				s.mu.Unlock()
				cardText = head
				cardStop = actionID(expandAct, cardID)
			}
		}
	}

	// 一个回合只留一条消息：答案就地写进原来那张进度卡片，不再另发一条。
	// 收尾立即落地，绕过节流；先清掉被压住的尾包，避免旧帧覆盖最终内容。
	// 只有连进度卡片都没建出来时，才退回发一条文本作为回复。
	s.clearThrottle(cardID)
	sent := ""
	if cardID != "" {
		if s.paint(cfg, cardID, cardText, cardStop) == nil && s.log != nil {
			s.log.Release(cardID)
		}
	} else if final != "已停止" {
		sent = s.send(cfg.AppID, cfg.AppSecret, t.MessageID, final)
	}
	// 群聊的完成提醒打在用户原消息上（reaction）；单聊静默，不另发消息。
	if final != "已停止" {
		s.reactDone(cfg, t)
	}
	if s.log != nil && t.ChatID != "" {
		id := sent
		if id == "" {
			id = cardID
		}
		if id == "" {
			id = "self:" + t.MessageID
		}
		s.log.Add(Line{ID: id, ChatID: t.ChatID, From: "你", Text: final, At: time.Now().Unix(), Self: true, Kind: "text"})
	}
}

// reactDone 在群里给用户刚发的原消息补一个完成表情。
// 群聊靠这个表情冒泡提醒，不再发第二条带 @ 的消息；单聊静默。
// 复用 hooks.React（底层即 link.go 的 POST .../reactions 封装），不另写 HTTP 调用。
func (s *Service) reactDone(cfg Config, t Turn) {
	if s.hooks.React == nil || t.ChatType != "group" || t.MessageID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := s.hooks.React(ctx, cfg.AppID, cfg.AppSecret, t.MessageID, doneEmoji); err != nil {
		s.noteFault(err)
	}
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
	if cardID != "" {
		s.clearThrottle(cardID)
		if s.paint(cfg, cardID, "已停止", "") == nil && s.log != nil {
			s.log.Release(cardID)
		}
	}
}

const commandHelp = "/help  列出这些指令\n/stop  停掉正在处理的这一轮\n/status  看现在交给哪个会话\n/use 名字  从下一条起换一个会话回答\n\n写操作需要批准时，卡片会发在这条对话里，直接点允许或拒绝。"

func slashLine(text string) (name, arg string, ok bool) {
	text = strings.TrimSpace(text)
	for strings.HasPrefix(text, "@") {
		i := strings.IndexAny(text, " \t")
		if i < 0 {
			return "", "", false
		}
		text = strings.TrimSpace(text[i+1:])
	}
	if !strings.HasPrefix(text, "/") || strings.Contains(text, "\n") {
		return "", "", false
	}
	head, rest, _ := strings.Cut(text, " ")
	name = strings.ToLower(strings.TrimPrefix(head, "/"))
	if name == "" {
		return "", "", false
	}
	for _, r := range name {
		if r < 'a' || r > 'z' {
			return "", "", false
		}
	}
	return name, strings.TrimSpace(rest), true
}

func (s *Service) command(t Turn) (string, bool) {
	name, arg, ok := slashLine(t.Text)
	if !ok {
		return "", false
	}
	switch name {
	case "help":
		return commandHelp, true
	case "stop":
		n := s.haltChat(t.ChatID)
		if n == 0 {
			return "现在没有在处理的这一轮。", true
		}
		return "已停止。", true
	case "status":
		return s.statusLine(t.ChatID), true
	case "use":
		return s.useLine(t.ChatID, arg), true
	case "mute":
		if s.log == nil {
			return "现在记不住静音，先聊吧。", true
		}
		dur, ok := parseMuteDuration(arg)
		if !ok {
			return "时长没看懂。发 /mute 30m 或 /mute 2h，/unmute 恢复。", true
		}
		s.log.SetMuteUntil(t.ChatID, time.Now().Add(dur).Unix())
		return "已静音 " + dur.String() + "，/unmute 恢复。", true
	case "unmute":
		if s.log != nil {
			s.log.SetMuteUntil(t.ChatID, 0)
		}
		return "已恢复。", true
	default:
		return "没有这条指令。发 /help 看可用的。", true
	}
}

// muteDurRe 匹配 30m / 2h 这种单单位时长。
var muteDurRe = regexp.MustCompile(`^(\d+)(m|h)$`)

// parseMuteDuration 解析静音时长；非法参数返回 ok=false。
func parseMuteDuration(arg string) (time.Duration, bool) {
	arg = strings.TrimSpace(arg)
	m := muteDurRe.FindStringSubmatch(arg)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	if m[2] == "m" {
		return time.Duration(n) * time.Minute, true
	}
	return time.Duration(n) * time.Hour, true
}

func (s *Service) haltChat(chatID string) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	var runs []*liveRun
	for _, cur := range s.runs {
		if cur == nil || cur.closed || cur.cancel == nil {
			continue
		}
		if cur.chat != chatID {
			continue
		}
		cur.closed = true
		runs = append(runs, cur)
	}
	cfg := s.cfg
	s.mu.Unlock()
	for _, run := range runs {
		run.cancel()
		if run.card != "" {
			s.clearThrottle(run.card)
			if s.paint(cfg, run.card, "已停止", "") == nil && s.log != nil {
				s.log.Release(run.card)
			}
		}
	}
	return len(runs)
}

func (s *Service) statusLine(chatID string) string {
	id := s.AgentFor(chatID)
	list := s.roster()
	for _, n := range list {
		if n.ID == id {
			name := n.Name
			if name == "" {
				name = n.ID
			}
			return "现在交给「" + name + "」。" + statusWord(n.Status)
		}
	}
	if id == "" {
		return "还没有指定本机会话。"
	}
	return "现在交给「" + id + "」。"
}

func (s *Service) useLine(chatID, name string) string {
	name = strings.TrimSpace(name)
	list := s.roster()
	if len(list) == 0 {
		return "还看不到本机会话。"
	}
	if name == "" {
		var b strings.Builder
		b.WriteString("可以换成这些会话：")
		for _, n := range list {
			label := n.Name
			if label == "" {
				label = n.ID
			}
			b.WriteString("\n")
			b.WriteString(label)
		}
		return b.String()
	}
	var hit []Named
	for _, n := range list {
		if n.Name == name || n.ID == name {
			hit = append(hit, n)
		}
	}
	if len(hit) == 0 {
		return "没有叫「" + name + "」的会话。发 /use 看名单。"
	}
	if len(hit) > 1 {
		return "有多个叫「" + name + "」的会话，说不过来换哪一个。"
	}
	s.BindRoom(chatID, hit[0].ID)
	label := hit[0].Name
	if label == "" {
		label = hit[0].ID
	}
	return "从下一条起交给「" + label + "」。"
}

func (s *Service) roster() []Named {
	if s == nil || s.hooks.Roster == nil {
		return nil
	}
	return s.hooks.Roster()
}

func statusWord(status string) string {
	switch status {
	case "running":
		return "它正在处理。"
	case "queued":
		return "它在排队。"
	case "blocked":
		return "它在等你。"
	case "error":
		return "它刚才出错了。"
	default:
		return "它现在空闲。"
	}
}

// Ask 是一次写操作审批。这条飞书对话正在由该会话回答时，把批准卡片发到飞书。
type Ask struct {
	ID      string
	AgentID string
	Tool    string
	Title   string
	Detail  string
}

func (s *Service) Ask(v Ask) {
	if s == nil || v.ID == "" || s.hooks.Actions == nil {
		return
	}
	agentID := v.AgentID
	if s.hooks.Root != nil {
		if root := s.hooks.Root(v.AgentID); root != "" {
			agentID = root
		}
	}
	s.mu.Lock()
	var run *liveRun
	for _, cur := range s.runs {
		if cur != nil && !cur.closed && cur.agent == agentID && cur.key != "" {
			run = cur
			break
		}
	}
	cfg := s.cfg
	s.mu.Unlock()
	if run == nil {
		return
	}
	text := approvalText(v)
	card, err := s.hooks.Actions(context.Background(), cfg.AppID, cfg.AppSecret, run.key, text, []CardButton{
		{Text: "拒绝", Act: "deny", ID: v.ID, Style: "danger"},
		{Text: "允许一次", Act: "once", ID: v.ID, Style: "primary"},
		{Text: "本会话允许", Act: "keep", ID: v.ID},
	})
	if err != nil || card == "" {
		if err != nil {
			log.Printf("feishu approval: %v", err)
		}
		return
	}
	s.mu.Lock()
	if s.asks == nil {
		s.asks = map[string]string{}
	}
	s.asks[v.ID] = card
	s.mu.Unlock()
}

func (s *Service) CloseAsk(id string) {
	card := s.takeAsk(id)
	if card == "" {
		return
	}
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	_ = s.paint(cfg, card, "已经处理过了", "")
}

func (s *Service) decide(cfg Config, t Turn) {
	// 先验身份再看卡。takeAsk 会把待审批卡从表里删掉，陌生人点一下不能把
	// owner 还没处理的审批弄没，也不能替别人做决定。
	if !canApprove(cfg, t) {
		msg := "只有会话 owner 或审批白名单里的人能处理这张卡。请让 owner 操作，或在设置里配置审批人。"
		if t.MessageID != "" {
			s.send(cfg.AppID, cfg.AppSecret, t.MessageID, msg)
		}
		return
	}
	card := s.takeAsk(t.ActionID)
	allow := t.Action == "once" || t.Action == "keep"
	var decideErr error
	if s.hooks.Decide == nil {
		decideErr = fmt.Errorf("批准还没接上")
	} else if err := s.hooks.Decide(t.ActionID, allow, t.Action == "keep"); err != nil {
		decideErr = err
	}
	text := ""
	if decideErr != nil {
		text = decideErr.Error()
	} else {
		// 审批卡就地收尾：已批准/已拒绝 · 谁 · 几点，不另发消息。
		text = approvalResult(allow, t)
	}
	if card != "" {
		_ = s.paint(cfg, card, text, "")
		return
	}
	if t.MessageID != "" {
		s.send(cfg.AppID, cfg.AppSecret, t.MessageID, text)
	}
}

// retry 用失败轮次的原始输入重新入队，复用现有 jobs/dispatch 通路，不新造协议。
func (s *Service) retry(cfg Config, t Turn) {
	s.mu.Lock()
	snap, ok := s.retries[t.ActionID]
	s.mu.Unlock()
	if !ok {
		return
	}
	// 原始消息已被 gate 记过一次，重试要重新跑，先忘掉它的去重记录。
	s.gate.Forget(snap.EventID, snap.MessageID)
	nt := snap
	nt.EventID = "retry-" + fmt.Sprintf("%d", time.Now().UnixNano())
	nt.Action = ""
	nt.ActionID = ""
	if s.jobs == nil {
		s.start()
	}
	select {
	case s.jobs <- nt:
	default:
		s.send(cfg.AppID, cfg.AppSecret, snap.MessageID, "正在处理前面的消息，这条先放下了")
	}
}

// expandAnswer 把超长答案的全文 patch 回同一张卡片。
func (s *Service) expandAnswer(cfg Config, t Turn) {
	s.mu.Lock()
	full, ok := s.fullTexts[t.ActionID]
	s.mu.Unlock()
	if !ok {
		return
	}
	s.clearThrottle(t.ActionID)
	_ = s.paint(cfg, t.ActionID, full, "")
}

// approvalResult 拼审批卡的收尾文案：已批准/已拒绝 · 谁 · 本地几点。
// 人名取事件里已有的 Sender（没有就退回 SenderID），不新增字段；时间用本地 HH:MM。
func approvalResult(allow bool, t Turn) string {
	status := "已拒绝"
	if allow {
		status = "已批准"
	}
	who := strings.TrimSpace(t.Sender)
	if who == "" {
		who = strings.TrimSpace(t.SenderID)
	}
	if who == "" {
		who = "有人"
	}
	return fmt.Sprintf("%s · %s · %s", status, who, time.Now().Format("15:04"))
}

// canApprove 判断这次卡片点击的人有没有资格批准。
//
// 规则按「owner + 显式白名单」：只认 open_id；sender 缺失、是应用/机器人
// 或名单为空都拒绝（fail closed）。群场景不要求 @ 机器人，点卡本身就是
// 明确的意图。
func canApprove(cfg Config, t Turn) bool {
	sender := strings.TrimSpace(t.SenderID)
	if sender == "" {
		return false
	}
	switch t.SenderType {
	case "app", "bot":
		return false
	}
	if strings.TrimSpace(cfg.OwnerOpenID) != "" && sender == strings.TrimSpace(cfg.OwnerOpenID) {
		return true
	}
	for _, id := range cfg.ApproverOpenIDs {
		if sender == strings.TrimSpace(id) {
			return true
		}
	}
	return false
}

func (s *Service) takeAsk(id string) string {
	if s == nil || id == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	card := s.asks[id]
	delete(s.asks, id)
	return card
}

func approvalText(v Ask) string {
	var b strings.Builder
	b.WriteString("需要你的批准")
	if v.Title != "" {
		b.WriteString("\n")
		b.WriteString(v.Title)
	}
	if v.Tool != "" {
		b.WriteString("\n工具：")
		b.WriteString(v.Tool)
	}
	if strings.TrimSpace(v.Detail) != "" {
		b.WriteString("\n\n")
		b.WriteString(v.Detail)
	}
	b.WriteString("\n\n在这里点即可。允许一次只放行这一次；本会话允许只对这个工具生效。")
	return b.String()
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
		if run.card != "" {
			s.clearThrottle(run.card)
			if s.paint(cfg, run.card, droppedLine, "") == nil && s.log != nil {
				s.log.Release(run.card)
			}
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
	if s.netDisabled {
		return
	}
	if s.log == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg := Config{AppID: appID, AppSecret: secret}
	cred := s.cred(appID, secret)
	for _, room := range s.log.Rooms() {
		var ids []string
		var err error
		if s.stops != nil {
			ids, err = s.stops(ctx, appID, secret, room.ID)
		} else {
			ids, err = OpenStopsAs(ctx, cred, room.ID)
		}
		if err != nil {
			s.noteGap(err)
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
	s.throttledPaint(cfg, card, text, key)
}

func busyClaim(err error) bool {
	return err != nil && err.Error() == "busy"
}

// reserve 占住一个还没在答飞书的会话。主会话忙时由 Claim 交给它的助手。
func (s *Service) reserve(parent, key, chat string, cancel context.CancelFunc) (*liveRun, error) {
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
			got, err := claim(parent, chat, busy)
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
		run := &liveRun{agent: id, key: key, chat: chat, cancel: cancel}
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
	switch t.Action {
	case "once", "keep", "deny":
		s.decide(cfg, t)
		return
	case retryAct:
		s.retry(cfg, t)
		return
	case expandAct:
		s.expandAnswer(cfg, t)
		return
	}
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

// throttleFor 取（或建）一张卡的节流状态。
func (s *Service) throttleFor(cardID string) *throttleState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.throttles == nil {
		s.throttles = map[string]*throttleState{}
	}
	th := s.throttles[cardID]
	if th == nil {
		th = &throttleState{}
		s.throttles[cardID] = th
	}
	return th
}

// throttledPaint 把增量写回卡片：同一张卡一秒最多 patch 一次，
// 被压掉的中间帧合并成最后一帧待发；收尾的最终内容请走 paint 绕过节流。
func (s *Service) throttledPaint(cfg Config, cardID, text, stopID string) {
	if cardID == "" || text == "" {
		return
	}
	if s.throttleInterval <= 0 {
		_ = s.paint(cfg, cardID, text, stopID)
		return
	}
	th := s.throttleFor(cardID)
	now := time.Now()
	th.mu.Lock()
	th.cfg = cfg
	th.pending = text
	th.key = stopID
	th.dirty = true
	if !th.waiting && now.Sub(th.last) >= s.throttleInterval {
		th.last = now
		th.dirty = false
		text, stopID := th.pending, th.key
		th.mu.Unlock()
		_ = s.paint(cfg, cardID, text, stopID)
		return
	}
	th.waiting = true
	th.mu.Unlock()
	go s.flushThrottleLater(cardID)
}

func (s *Service) flushThrottleLater(cardID string) {
	time.Sleep(s.throttleInterval)
	s.flushThrottle(cardID)
}

// flushThrottle 把节流压住的最后一帧落地；测试可直接调用以跳过等待。
func (s *Service) flushThrottle(cardID string) {
	th := s.throttleFor(cardID)
	th.mu.Lock()
	th.waiting = false
	if !th.dirty {
		th.mu.Unlock()
		return
	}
	text, key, cfg := th.pending, th.key, th.cfg
	th.dirty = false
	th.last = time.Now()
	th.mu.Unlock()
	_ = s.paint(cfg, cardID, text, key)
}

// clearThrottle 清掉一张卡压住的待发帧。收尾/停止/断线时先调它，
// 避免旧帧晚到覆盖最终卡片内容。
func (s *Service) clearThrottle(cardID string) {
	if cardID == "" {
		return
	}
	th := s.throttleFor(cardID)
	th.mu.Lock()
	th.dirty = false
	th.waiting = false
	th.mu.Unlock()
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
	// failStreak 让连续失败退避。这个接口偶发 token 失效是常态，
	// 每 20 秒刷一行日志只会把真正的错误淹掉。
	failStreak := 0
	for {
		got, err := s.loadScopes(ctx, appID, secret)
		if err != nil {
			failStreak++
			if failStreak == 1 || failStreak%30 == 0 {
				log.Printf("feishu scopes: %v", err)
			}
			if !blind {
				// 权限查不出来不代表消息拉不到，反而先补一次。
				s.catchUp(ctx, appID, secret)
				blind = true
			}
			// 接口挂了不能因此清空已经算出来的提示。
		} else {
			failStreak = 0
			if historyReady(got) {
				stamp := scopeStamp(got)
				if stamp != pulled {
					s.catchUp(ctx, appID, secret)
					pulled = stamp
				}
			}
			s.setScopeDetail(got)
		}
		next := wait
		if failStreak > 3 {
			// 最多退到 5 分钟一次。
			if backoff := wait * time.Duration(min(failStreak, 15)); backoff < 5*time.Minute {
				next = backoff
			} else {
				next = 5 * time.Minute
			}
		}
		timer := time.NewTimer(next)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// setScopeDetail 只在「问题出在权限」时改 detail。
// 之前这里每次都覆盖，会把真实的发消息错误冲掉。
func (s *Service) setScopeDetail(granted map[string]bool) {
	miss := MissingScopes(granted)
	var hard []ScopeGap
	for _, g := range miss {
		if !g.Optional {
			hard = append(hard, g)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(hard) > 0 {
		s.detail = scopeDetail(hard)
		return
	}
	// 必需项都齐了：只清掉自己写的那条，别的错误原样留着。
	if strings.HasPrefix(s.detail, "还缺这些权限") {
		s.detail = ""
	}
}

func (s *Service) loadScopes(ctx context.Context, appID, secret string) (map[string]bool, error) {
	if s.scopes != nil {
		return s.scopes(ctx, appID, secret)
	}
	if s.netDisabled {
		return nil, nil
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
	if scope := missingScopeFromError(msg); scope != "" {
		// 飞书把缺的具体 scope 写在正文里，比任何静态清单都准。
		msg = "还缺权限 " + scope + "，去设置页的飞书里点「检查权限」开通，然后发布版本。"
	} else if strings.Contains(msg, "Access denied") || strings.Contains(msg, "权限") {
		msg = "飞书没给这个应用发消息和标表情的权限。开通「获取与发送单聊、群组消息」并发布版本后，会自己把还没回复的消息拉下来。"
	}
	s.mu.Lock()
	s.detail = msg
	s.mu.Unlock()
}

// noteGap 记下「这个群读不到消息」这类能力缺口，别让它只留在日志里。
func (s *Service) noteGap(err error) {
	if err == nil {
		return
	}
	scope := missingScopeFromError(err.Error())
	if scope == "" {
		return
	}
	s.mu.Lock()
	s.detail = "还缺权限 " + scope + "：补拉群历史失败。开通后要发布版本，飞书才会放行。"
	s.mu.Unlock()
}

func (s *Service) noteLine(cfg Config, t Turn) {
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
	text, kind, media := t.Text, "text", []Media(nil)
	if t.MsgType != "" && t.MsgType != "text" {
		// 卡片、图片、文件这些不能只看 Text：按类型再解析一次，落库才有内容。
		text, kind, media = readMessage(t.MsgType, t.Content, nil)
	} else if strings.TrimSpace(text) == "" {
		if len(t.Images) > 0 {
			text, kind, media = readMessage(t.MsgType, t.Content, nil)
		} else {
			text = "[空消息]"
		}
	}
	if kind == "card" {
		// 实体卡片的事件体里只有 card_id。凭当前身份把正文取回来，
		// 取不到就照旧落占位，不阻塞这条消息。
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c := s.cred(cfg.AppID, cfg.AppSecret)
		if token, err := c.token(cctx); err == nil {
			text = resolveCardText(cctx, token, text, t.Content)
		}
		cancel()
	}
	s.log.Add(Line{ID: t.MessageID, ChatID: t.ChatID, From: from, FromID: t.SenderID, Text: text, At: at, Kind: kind, Media: media})
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
	appID, secret := s.cfg.AppID, s.cfg.AppSecret
	s.mu.Unlock()
	if id == "" && appID != "" {
		// 长连接上 ensureBot 是异步的，第一条消息可能赶在它前面。
		// 这里同步补一次，否则 @ 判断会退化成「见到 bot 就当是在叫我」。
		s.ensureBot(context.Background(), appID, secret)
		s.mu.Lock()
		id = s.botID
		s.mu.Unlock()
	}
	for _, m := range t.Mentions {
		if !m.Bot {
			continue
		}
		// 认不出自己是谁时，见到 @ 了某个 bot 就当作在叫我。
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

// groupMode 取群级三态：房间里单独设过就用它；否则把旧全局 MentionOnly
// 映射成每群模式——true 只 @（mention），false 自己接话（active）。
// cfg 由调用方在锁内拷贝传入，避免直接读 s.cfg 产生竞争。
func (s *Service) groupMode(cfg Config, chatID string) string {
	if s.log != nil {
		if m := s.log.Mode(chatID); m != "" {
			return m
		}
	}
	if cfg.MentionOnly {
		return modeMention
	}
	return modeActive
}

// muted 判断这个群是否在静音期内（到期自动恢复）。
func (s *Service) muted(chatID string) bool {
	if s.log == nil {
		return false
	}
	until := s.log.MuteUntil(chatID)
	return until > 0 && until > time.Now().Unix()
}

// groupAcquire 占住一个群的执行槽；同群消息排队，不并发跑两轮。
func (s *Service) groupAcquire(chatID string) {
	s.mu.Lock()
	if s.groupCond == nil {
		s.groupCond = sync.NewCond(&s.mu)
	}
	if s.groupBusy == nil {
		s.groupBusy = map[string]bool{}
	}
	for s.groupBusy[chatID] {
		s.groupCond.Wait()
	}
	s.groupBusy[chatID] = true
	s.mu.Unlock()
}

func (s *Service) groupRelease(chatID string) {
	s.mu.Lock()
	delete(s.groupBusy, chatID)
	if s.groupCond != nil {
		s.groupCond.Broadcast()
	}
	s.mu.Unlock()
}

// groupAllow 滑动窗口内是否还能再跑一轮；放行时记下这一轮的时间。
func (s *Service) groupAllow(chatID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.groupRounds == nil {
		s.groupRounds = map[string][]time.Time{}
	}
	now := time.Now()
	var kept []time.Time
	for _, ts := range s.groupRounds[chatID] {
		if now.Sub(ts) < groupRateWindow {
			kept = append(kept, ts)
		}
	}
	limit := s.groupRoundsPerMinute
	if limit <= 0 {
		limit = defaultGroupRoundsPerMinute
	}
	if len(kept) >= limit {
		s.groupRounds[chatID] = kept
		return false
	}
	s.groupRounds[chatID] = append(kept, now)
	return true
}

// requeueLater 把超限消息延后重新入队，不丢；Requeued 标记让 gate 跳过自身去重。
func (s *Service) requeueLater(t Turn) {
	delay := s.requeueDelay
	if delay <= 0 {
		delay = defaultRequeueDelay
	}
	go func() {
		time.Sleep(delay)
		t.Requeued = true
		if s.jobs == nil {
			return
		}
		select {
		case s.jobs <- t:
		default:
		}
	}()
}

// senderNoise 判断发送方是不是机器人/系统（自己发的已在 fromSelf 里挡过）。
func senderNoise(t Turn) bool {
	return t.SenderType == "app" || t.SenderType == "bot" || t.SenderType == "system" || t.MsgType == "system"
}

// contentNoise 过滤 active 群里的明确内容噪音。只覆盖可确定性判定的类型，
// 不按长度一刀切——「在吗」「好」这类短问句仍然放行。
func contentNoise(t Turn) bool {
	text := strings.TrimSpace(t.Text)
	if text == "" {
		return false
	}
	if plusOneRe.MatchString(text) {
		return true
	}
	return onlyEmoji(text)
}

// onlyEmoji 判断整句是否只由表情/符号/标点构成（无字母、数字、汉字）。
func onlyEmoji(s string) bool {
	has := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Han, r):
			return false
		case unicode.IsSpace(r):
			continue
		default:
			has = true
		}
	}
	return has
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
	var more []listedChat
	var err error
	if s.listChats != nil {
		more, err = s.listChats(ctx, appID, secret)
	} else if !s.netDisabled {
		more, err = ListChatsAs(ctx, s.cred(appID, secret))
	}
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
	// 机器人自己在哪些群里。用户身份能读到自己所在的所有群，
	// 但机器人不在的群只能看、不能答。
	bots := map[string]bool{}
	known := true
	switch {
	case s.netDisabled:
		// 离线测试/无网时不再查机器人在哪些群，按不在处理（只读兜底）。
		known = false
	default:
		if list, err := ListChatsAs(ctx, Cred{AppID: appID, Secret: secret}); err != nil {
			// 查不到就别乱猜，保持原来的行为。
			log.Printf("feishu bot chats: %v", err)
			known = false
		} else {
			for _, c := range list {
				bots[c.ID] = true
			}
		}
	}
	allowed := map[string]bool{}
	for _, id := range cfg.AllowChats {
		if id != "" {
			allowed[id] = true
		}
	}
	for _, c := range seen {
		// 权限策略：默认只同步机器人在的群和单聊。
		// 机器人不在的群不自动同步、不自动建房间；用户按 chat_id 读过一次
		// 就记进 AllowChats，算显式放行，之后继续补历史，但依然只读。
		inBot := !known || bots[c.ID]
		if known && s.log != nil {
			s.log.SetReadOnly(c.ID, c.Kind != "p2p" && !inBot)
		}
		if known && c.Kind != "p2p" && !inBot && !allowed[c.ID] {
			continue
		}
		s.replay(cfg, c, now, inBot || c.Kind == "p2p")
	}
}

// replayChat 把历史补进记录；能在这个群里回答时，再把还没答的消息排进队列。
func (s *Service) replayChat(cfg Config, c listedChat, now int64) {
	s.replay(cfg, c, now, true)
}

// historyMsgs 把 feishu.Log 的记录投影成桥接到频道表的历史消息。
func historyMsgs(lines []Line) []HistoryMsg {
	out := make([]HistoryMsg, len(lines))
	for i, l := range lines {
		out[i] = HistoryMsg{
			ExtID:    l.ID,
			FromID:   l.FromID,
			FromName: l.From,
			Text:     l.Text,
			At:       l.At,
			Kind:     l.Kind,
		}
	}
	return out
}

// syncPages 是手动拉取时翻几页。一页 50 条，最多 150 条，够看清最近的上下文。
const syncPages = 3

// SyncRoom 按 chat_id 拉一次历史，记进会话记录，返回新增条数。
//
// 读历史用当前身份：授权过用户身份就能读到这个人所在的群，
// 机器人不在群里也不影响。这是「群消息读取」的入口。
// 第一次读到的群会出现在会话列表里，之后每轮补历史会继续更新它。
func (s *Service) SyncRoom(ctx context.Context, chatID, name string) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("飞书还没接上")
	}
	if chatID == "" {
		return 0, fmt.Errorf("缺少 chat_id")
	}
	s.mu.Lock()
	cfg := s.cfg
	owner := s.owner
	sink := s.sink
	s.mu.Unlock()
	if cfg.AppID == "" {
		return 0, fmt.Errorf("还没有可用的飞书机器人")
	}
	// 增量：已入库到的游标作为 since（秒级开区间，接口内部 +1）；零值首次拉一页。
	since := int64(0)
	if sink != nil {
		since, _ = sink.HistorySince(chatID)
	}
	var lines []Line
	var err error
	if s.history != nil {
		lines, err = s.history(ctx, cfg.AppID, cfg.AppSecret, chatID, since)
	} else {
		lines, err = HistoryPages(ctx, s.cred(cfg.AppID, cfg.AppSecret), chatID, since, syncPages)
	}
	if err != nil {
		// 权限缺失记一条可见提示；429/5xx 退避后仍失败则降级。游标都不前移。
		s.noteGap(err)
		log.Printf("feishu sync %s: %v", chatID, err)
		return 0, nil
	}
	if s.log == nil {
		// 没有房间记录时也把历史落频道，避免手动拉过的群在频道视图里空白。
		if sink != nil {
			sink.IngestHistory(chatID, name, historyMsgs(lines))
		}
		return 0, nil
	}
	// 只给新会话定性：已有记录可能是单聊，别翻成群。
	// 注意要先判断再写名字，写名字会把房间创建出来。
	if !s.log.Known(chatID) {
		s.log.Kind(chatID, "group")
	}
	if name != "" {
		s.log.Name(chatID, name)
	}
	// 机器人不在的群只能手动读：标成只读，界面会说明原因，
	// 也不会把它当默认同步范围，更不会自动回答。
	if inBot, ok := s.botInChat(ctx, cfg.AppID, cfg.AppSecret, chatID); ok {
		s.log.SetReadOnly(chatID, !inBot)
		if inBot && owner != "" && s.log.Bot(chatID) == "" {
			s.log.SetBot(chatID, owner)
		}
	}
	added := 0
	for _, line := range lines {
		if line.ChatID == "" {
			line.ChatID = chatID
		}
		if s.log.Add(line) {
			added++
		}
	}
	// feishu-rooms.json 之外，再写一份到频道表，供频道视图看到进群前/停机期的消息。
	if sink != nil {
		sink.IngestHistory(chatID, name, historyMsgs(lines))
	}
	return added, nil
}

// botInChat 查机器人自己是否在这个群里。第二个返回值表示这次查没查出来。
func (s *Service) botInChat(ctx context.Context, appID, secret, chatID string) (bool, bool) {
	if s.netDisabled {
		return false, false
	}
	list, err := ListChatsAs(ctx, Cred{AppID: appID, Secret: secret})
	if err != nil {
		log.Printf("feishu bot chats: %v", err)
		return false, false
	}
	for _, c := range list {
		if c.ID == chatID {
			return true, true
		}
	}
	return false, true
}

func (s *Service) replay(cfg Config, c listedChat, now int64, answer bool) {
	if c.ID == "" || cfg.AppID == "" {
		return
	}
	kind := c.Kind
	if kind == "topic" {
		kind = "group"
	}
	if s.jobs == nil {
		s.start()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s.mu.Lock()
	sink := s.sink
	s.mu.Unlock()
	since := int64(0)
	if sink != nil {
		since, _ = sink.HistorySince(c.ID)
	}
	var lines []Line
	var err error
	switch {
	case s.history != nil:
		lines, err = s.history(ctx, cfg.AppID, cfg.AppSecret, c.ID, since)
	case !s.netDisabled:
		lines, err = HistoryAs(ctx, s.cred(cfg.AppID, cfg.AppSecret), c.ID, since)
	default:
		return
	}
	if err != nil {
		log.Printf("feishu history %s: %v", c.ID, err)
		s.noteGap(err)
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
	// 历史同时落频道表；只入库，不广播、不触发 agent。
	if sink != nil {
		sink.IngestHistory(c.ID, c.Name, historyMsgs(lines))
	}
	if kind == "" {
		kind = "p2p"
	}
	// mention / off 群：补拉回来的历史照旧记进房间，但不主动接话；
	// 只有 active 群才把未回答的历史排进队列。
	if kind == "group" && s.groupMode(cfg, c.ID) != modeActive {
		return
	}
	// 机器人不在群里（用户身份才读得到的群），只能看不能答：
	// 排进队列只会让回答失败一次，还刷一片报错。
	if !answer {
		return
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
	if s.netDisabled {
		return
	}
	s.mu.Lock()
	have := s.botID
	s.mu.Unlock()
	if have != "" || appID == "" {
		return
	}
	id, name, err := BotInfo(ctx, appID, secret)
	if err != nil {
		log.Printf("feishu bot: %v", err)
		return
	}
	s.mu.Lock()
	s.botID = id
	if name != "" {
		s.botName = name
	}
	s.mu.Unlock()
}
