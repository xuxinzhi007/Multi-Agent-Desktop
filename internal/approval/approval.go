// Package approval 把写操作的审批从「同步 if」变成一条可等待的队列。
//
// 循环在 worker goroutine 里跑，审批必须能跨 goroutine：worker 挂起等，
// 用户在界面上点允许或拒绝，再把结果送回。等待期间按 ctx 取消，
// 所以「停止」不会留下一个永远挂着的审批。
package approval

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"multi-agent-desktop/internal/tool"
)

// View 是发给界面的一张审批卡。
type View struct {
	ID        string `json:"id"`
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	Tool      string `json:"tool"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Diff      string `json:"diff,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

// Center 管理所有等待中的审批。
type Center struct {
	mu      sync.Mutex
	emit    func(event string, data any)
	pending map[string]*waiter
	order   []string
	// allowed 记「本会话对这个工具以后都放行」。
	allowed map[string]map[string]bool
	timeout time.Duration
	seq     atomic.Uint64
}

type waiter struct {
	view View
	ch   chan decision
}

type decision struct {
	allow    bool
	remember bool
	reason   string
}

// New 造一个审批中心。emit 为 nil 时不发事件（测试或无人值守）。
func New(emit func(event string, data any)) *Center {
	return &Center{
		emit:    emit,
		pending: map[string]*waiter{},
		allowed: map[string]map[string]bool{},
		timeout: 5 * time.Minute,
	}
}

// SetTimeout 改等待上限，测试用；<=0 表示不改。
func (c *Center) SetTimeout(d time.Duration) {
	if d > 0 {
		c.timeout = d
	}
}

// Approver 返回绑定了某个会话的审批器，交给这个会话的工具注册表。
func (c *Center) Approver(agentID, agentName string) tool.Approver {
	return &sessionApprover{c: c, id: agentID, name: agentName}
}

type sessionApprover struct {
	c    *Center
	id   string
	name string
}

func (a *sessionApprover) Approve(ctx context.Context, call tool.Call) (bool, string) {
	return a.c.Approve(ctx, a.id, a.name, call)
}

// Approve 阻塞到用户决定、ctx 取消或超时。
func (c *Center) Approve(ctx context.Context, agentID, agentName string, call tool.Call) (bool, string) {
	if c == nil {
		return true, ""
	}
	if c.remembered(agentID, call.Name) {
		return true, ""
	}
	id := fmt.Sprintf("ap-%d-%d", time.Now().UnixNano(), c.seq.Add(1))
	view := View{
		ID:        id,
		AgentID:   agentID,
		AgentName: agentName,
		Tool:      call.Name,
		Title:     call.Preview.Title,
		Detail:    call.Preview.Detail,
		Diff:      call.Preview.Diff,
		CreatedAt: time.Now().Unix(),
	}
	if view.Title == "" {
		view.Title = call.Name
	}
	w := &waiter{view: view, ch: make(chan decision, 1)}

	c.mu.Lock()
	c.pending[id] = w
	c.order = append(c.order, id)
	c.mu.Unlock()
	c.emitEvent("tool.approval", view)

	defer c.drop(id)

	select {
	case d := <-w.ch:
		if d.remember && d.allow {
			c.remember(agentID, call.Name)
		}
		return d.allow, d.reason
	case <-ctx.Done():
		return false, "这一轮已停止，审批取消"
	case <-time.After(c.timeout):
		return false, "等待审批超时"
	}
}

// Decide 是界面按下的那一下。remember 表示本会话以后不再问这个工具。
func (c *Center) Decide(id string, allow, remember bool, reason string) error {
	c.mu.Lock()
	w, ok := c.pending[id]
	if !ok {
		c.mu.Unlock()
		return fmt.Errorf("审批已失效或已完成")
	}
	delete(c.pending, id)
	for i, cur := range c.order {
		if cur == id {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.mu.Unlock()

	select {
	case w.ch <- decision{allow: allow, remember: remember, reason: reason}:
	default:
	}
	c.emitEvent("tool.approval.done", map[string]any{"id": id})
	return nil
}

// Pending 返回还没决定的审批，界面打开时用来补状态。
func (c *Center) Pending() []View {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]View, 0, len(c.order))
	for _, id := range c.order {
		if w, ok := c.pending[id]; ok {
			out = append(out, w.view)
		}
	}
	return out
}

// Forget 清掉某个会话的「本会话允许」，删除会话时调用。
func (c *Center) Forget(agentID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.allowed, agentID)
	c.mu.Unlock()
}

func (c *Center) remembered(agentID, toolName string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.allowed[agentID] != nil && c.allowed[agentID][toolName]
}

func (c *Center) remember(agentID, toolName string) {
	c.mu.Lock()
	if c.allowed[agentID] == nil {
		c.allowed[agentID] = map[string]bool{}
	}
	c.allowed[agentID][toolName] = true
	c.mu.Unlock()
}

// drop 在等待结束时清掉卡片，正常 Decide 已经删过，重复删是幂等的。
func (c *Center) drop(id string) {
	c.mu.Lock()
	_, still := c.pending[id]
	if still {
		delete(c.pending, id)
		for i, cur := range c.order {
			if cur == id {
				c.order = append(c.order[:i], c.order[i+1:]...)
				break
			}
		}
	}
	c.mu.Unlock()
	if still {
		c.emitEvent("tool.approval.done", map[string]any{"id": id})
	}
}

func (c *Center) emitEvent(name string, data any) {
	if c == nil || c.emit == nil {
		return
	}
	c.emit(name, data)
}
