// multi-agent-desktop sidecar —— W1 骨架
// 本地 HTTP + WebSocket，给 Tauri 前端推事件。
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type hub struct {
	mu    sync.Mutex
	conns map[*websocket.Conn]struct{}
}

var eventHub = &hub{conns: make(map[*websocket.Conn]struct{})}

func (h *hub) add(c *websocket.Conn) {
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
}

func (h *hub) remove(c *websocket.Conn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
}

func (h *hub) broadcast(event string, data any) {
	payload, err := json.Marshal(map[string]any{"event": event, "data": data, "ts": time.Now().Unix()})
	if err != nil {
		log.Printf("[sidecar] marshal %s: %v", event, err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		if err := c.WriteMessage(websocket.TextMessage, payload); err != nil {
			_ = c.Close()
			delete(h.conns, c)
		}
	}
}

func (h *hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[sidecar] ws upgrade: %v", err)
		return
	}
	eventHub.add(c)
	log.Printf("[sidecar] ws client connected, total=%d", eventHub.count())

	_ = c.WriteJSON(map[string]any{
		"event": "hello",
		"data":  map[string]any{"service": "multi-agent-sidecar", "version": "0.1.0"},
	})

	go func() {
		defer func() {
			eventHub.remove(c)
			_ = c.Close()
			log.Printf("[sidecar] ws client gone, total=%d", eventHub.count())
		}()
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"service": "multi-agent-sidecar",
		"clients": eventHub.count(),
		"ts":      time.Now().Unix(),
	})
}

// heartbeat 每 3 秒给所有连上的前端推一跳，证明链路活。
func heartbeat() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for range t.C {
		if eventHub.count() == 0 {
			continue
		}
		eventHub.broadcast("ping", map[string]any{"clients": eventHub.count()})
	}
}

func main() {
	port := flag.String("port", "17389", "sidecar listen port")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/ws", handleWS)

	// TODO(W3): /api/agents     CRUD 多 agent 会话
	// TODO(W3): /api/worktrees  git worktree 管理

	go heartbeat()

	addr := "127.0.0.1:" + *port
	log.Printf("[sidecar] listening on http://%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("[sidecar] failed: %v", err)
	}
}
