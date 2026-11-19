package feishu

import (
	"context"
	"log"
	"sync"
)

// Hooks 是飞书包向外要的两件事：让本机 agent 跑完一句，以及把文字发回飞书。
type Hooks struct {
	Run   func(ctx context.Context, agentID, text string) (string, error)
	Reply func(ctx context.Context, appID, secret, messageID, text string) error
	Dial  func(ctx context.Context, appID, secret string, on func(Turn), mark func(state, detail string)) error
}

// Service 维持一条长连接。事件处理只入队，模型调用在 worker 里。
type Service struct {
	hooks Hooks

	mu     sync.Mutex
	cfg    Config
	cancel context.CancelFunc
	gen    int
	state  string
	detail string
	gate   Gate

	once sync.Once
	jobs chan Turn
}

// Config 是当前生效的机器人配置。
type Config struct {
	Enabled   bool
	AppID     string
	AppSecret string
	AgentID   string
}

// New 构造服务。Dial 为空时 Apply 不会去连飞书。
func New(h Hooks) *Service {
	return &Service{hooks: h, state: "stopped"}
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

func (s *Service) loop() {
	for t := range s.jobs {
		s.dispatch(t)
	}
}

func (s *Service) dispatch(t Turn) {
	s.mu.Lock()
	d := s.gate.Decide(t)
	cfg := s.cfg
	s.mu.Unlock()
	if d.Reply != "" {
		s.send(cfg.AppID, cfg.AppSecret, t.MessageID, d.Reply)
		return
	}
	if d.Prompt == "" {
		return
	}
	if s.hooks.Run == nil {
		s.send(cfg.AppID, cfg.AppSecret, t.MessageID, "本机会话没有接上")
		return
	}
	text, err := s.hooks.Run(context.Background(), cfg.AgentID, d.Prompt)
	if err != nil {
		s.send(cfg.AppID, cfg.AppSecret, t.MessageID, err.Error())
		return
	}
	if text == "" {
		text = "没有给出文字回复"
	}
	s.send(cfg.AppID, cfg.AppSecret, t.MessageID, text)
}

func (s *Service) send(appID, secret, messageID, text string) {
	if s.hooks.Reply == nil || messageID == "" || text == "" {
		return
	}
	if err := s.hooks.Reply(context.Background(), appID, secret, messageID, text); err != nil {
		log.Printf("feishu reply: %v", err)
	}
}
