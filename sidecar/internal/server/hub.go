package server

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Hub 维护所有前端 WS 连接，并把事件广播出去。
type Hub struct {
	mu    sync.Mutex
	conns map[*conn]struct{}
}

type conn struct {
	ws   *websocket.Conn
	send chan []byte
	hub  *Hub
}

type Envelope struct {
	Event string `json:"event"`
	Data  any    `json:"data,omitempty"`
	TS    int64  `json:"ts,omitempty"`
}

// Command 是前端发来的指令。
type Command struct {
	Action string          `json:"action"`
	ID     string          `json:"id,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

func NewHub() *Hub {
	return &Hub{conns: make(map[*conn]struct{})}
}

func (h *Hub) Register(ws *websocket.Conn) *conn {
	c := &conn{ws: ws, send: make(chan []byte, 256), hub: h}
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *Hub) Unregister(c *conn) {
	h.mu.Lock()
	if _, ok := h.conns[c]; ok {
		delete(h.conns, c)
		close(c.send)
	}
	h.mu.Unlock()
}

func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// Broadcast 把一个事件推给所有连接。发送缓冲满时丢弃该连接，避免阻塞。
func (h *Hub) Broadcast(event string, data any) {
	h.broadcastExcept(nil, event, data)
}

// BroadcastExclude 广播给除 skip 以外的连接。
func (h *Hub) BroadcastExclude(skip *conn, event string, data any) {
	h.broadcastExcept(skip, event, data)
}

func (h *Hub) broadcastExcept(skip *conn, event string, data any) {
	payload, err := json.Marshal(Envelope{Event: event, Data: data, TS: time.Now().UnixMilli()})
	if err != nil {
		log.Printf("[hub] marshal %s: %v", event, err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.conns {
		if c == skip {
			continue
		}
		select {
		case c.send <- payload:
		default:
			delete(h.conns, c)
			close(c.send)
			_ = c.ws.Close()
		}
	}
}

// writePump 单 goroutine 写，gorilla 不允许并发写。
func (c *conn) writePump(done <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		_ = c.ws.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				_ = c.ws.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.ws.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}
