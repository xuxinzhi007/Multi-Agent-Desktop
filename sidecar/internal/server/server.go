package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"multi-agent-desktop/sidecar/internal/agent"
	"multi-agent-desktop/sidecar/internal/llm"
	"multi-agent-desktop/sidecar/internal/pty"
)

type Server struct {
	Hub     *Hub
	Agents  *agent.Manager
	Runner  *pty.Runner
	mu      sync.RWMutex
	llmCfg  llm.Config
	workDir string
}

func New(workDir string, cfg llm.Config) *Server {
	hub := NewHub()

	// 用闭包的 self 引用，agent 事件直接走 hub 广播
	s := &Server{Hub: hub, Runner: pty.NewRunner(workDir), llmCfg: cfg, workDir: workDir}
	s.Agents = agent.NewManager(cfg, func(event string, data any) {
		hub.Broadcast(event, data)
	})
	return s
}

func (s *Server) WorkDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.workDir
}

func (s *Server) SetLLMConfig(cfg llm.Config) {
	s.mu.Lock()
	s.llmCfg = cfg
	s.mu.Unlock()
	s.Agents.SetLLMConfig(cfg)
}

func (s *Server) LLMConfig() llm.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.llmCfg
}

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] upgrade: %v", err)
		return
	}

	c := s.Hub.Register(ws)
	done := make(chan struct{})

	go c.writePump(done)

	// 连上先推一份完整快照，前端不用再发 list 请求
	c.send <- mustJSON(Envelope{Event: "bootstrap", Data: s.bootstrap(), TS: time.Now().UnixMilli()})

	log.Printf("[ws] client connected, total=%d", s.Hub.Count())

	go func() {
		defer func() {
			close(done)
			s.Hub.Unregister(c)
			_ = ws.Close()
			log.Printf("[ws] client gone, total=%d", s.Hub.Count())
		}()

		ws.SetReadLimit(1 << 20)
		for {
			_, raw, err := ws.ReadMessage()
			if err != nil {
				return
			}
			var cmd Command
			if err := json.Unmarshal(raw, &cmd); err != nil {
				s.replyError(c, "", "invalid json")
				continue
			}
			s.dispatch(c, cmd)
		}
	}()
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"event":"error","data":{"error":"marshal"}}`)
	}
	return b
}

func (c *conn) push(event string, data any) {
	c.send <- mustJSON(Envelope{Event: event, Data: data, TS: time.Now().UnixMilli()})
}

func (s *Server) replyError(c *conn, id, msg string) {
	c.push("error", map[string]any{"id": id, "error": msg})
}

func (s *Server) bootstrap() map[string]any {
	return map[string]any{
		"agents":  s.Agents.List(),
		"llm":     s.LLMConfig(),
		"workDir": s.WorkDir(),
		"version": "0.2.0",
	}
}

func (s *Server) dispatch(c *conn, cmd Command) {
	switch cmd.Action {
	case "agent.create":
		var p struct {
			Name string `json:"name"`
			Role string `json:"role"`
		}
		_ = json.Unmarshal(cmd.Data, &p)
		s.Agents.Create("", p.Name, p.Role)

	case "agent.delete":
		var p struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(cmd.Data, &p)
		if !s.Agents.Delete(p.ID) {
			s.replyError(c, p.ID, "agent not found")
		}

	case "agent.stop":
		var p struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(cmd.Data, &p)
		s.Agents.Stop(p.ID)

	case "agent.prompt":
		var p agent.PromptRequest
		if err := json.Unmarshal(cmd.Data, &p); err != nil {
			s.replyError(c, cmd.ID, "invalid prompt payload")
			return
		}
		if err := s.Agents.Prompt(context.Background(), p); err != nil {
			s.replyError(c, cmd.ID, err.Error())
		}

	case "shell.run":
		var p struct {
			AgentID string `json:"agentId"`
			Command string `json:"command"`
		}
		_ = json.Unmarshal(cmd.Data, &p)
		go func() {
			s.Hub.Broadcast("shell.start", map[string]any{"agentId": p.AgentID, "command": p.Command})
			res := s.Runner.Run(context.Background(), p.Command)
			s.Hub.Broadcast("shell.result", map[string]any{
				"agentId": p.AgentID,
				"command": p.Command,
				"result":  res,
			})
		}()

	case "llm.config":
		var p llm.Config
		_ = json.Unmarshal(cmd.Data, &p)
		s.SetLLMConfig(p)
		// 单播确认给发起者，广播给其他客户端（避免发起者自激回环）
		c.push("llm.config", map[string]any{"cfg": s.LLMConfig(), "origin": true})
		s.Hub.BroadcastExclude(c, "llm.config", map[string]any{"cfg": s.LLMConfig()})

	case "workdir.set":
		var p struct {
			Dir string `json:"dir"`
		}
		_ = json.Unmarshal(cmd.Data, &p)
		if p.Dir != "" {
			s.mu.Lock()
			s.workDir = p.Dir
			s.mu.Unlock()
			s.Runner.SetDir(p.Dir)
			s.Hub.Broadcast("workdir", map[string]any{"dir": p.Dir})
		}

	case "ping":
		c.push("pong", map[string]any{"ts": time.Now().UnixMilli()})

	default:
		s.replyError(c, cmd.ID, "unknown action: "+cmd.Action)
	}
}
