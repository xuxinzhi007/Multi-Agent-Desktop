// Package runtime 定义桌面端与 Agent 运行时之间的内部协议。
//
// 这一层只做三件事：
//
//  1. 统一事件：不管背后是进程内 loop、ACP 外部进程还是 CLI，UI 只认一套 Event。
//  2. 统一会话生命周期：Prompt / Cancel / RespondPermission / Close 是唯一的控制点。
//  3. 可替换执行体：Runtime 是适配器接口，native / acp / cli 只是不同实现。
//
// 协议版本只升不降。未知字段必须忽略，未知事件类型必须降级为 notice，
// 不允许因为对端多了一个字段就断开连接。
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Version 是本内部协议的版本号。与 ACP 的 protocolVersion（整数）无关：
// 这一层是我们自己的契约，ACP 只是其中一个适配器。
const Version = "mad.runtime/1"

// EventKind 是事件类型。命名用「对象.动作」，避免以后再出现 tool.start / agent.tool 两种风格。
type EventKind string

const (
	KindTurnStarted  EventKind = "turn.started"
	KindTextDelta    EventKind = "text.delta"
	KindThoughtDelta EventKind = "thought.delta"
	KindToolStarted  EventKind = "tool.started"
	KindToolUpdated  EventKind = "tool.updated"
	KindToolFinished EventKind = "tool.finished"
	KindPlanUpdated  EventKind = "plan.updated"
	KindPermission   EventKind = "permission.request"
	KindNotice       EventKind = "notice"
	KindUsage        EventKind = "usage"
	KindTurnFinished EventKind = "turn.finished"
	KindError        EventKind = "error"
)

// Valid 报告这个 kind 是否属于协议内定义。
func (k EventKind) Valid() bool {
	switch k {
	case KindTurnStarted, KindTextDelta, KindThoughtDelta,
		KindToolStarted, KindToolUpdated, KindToolFinished,
		KindPlanUpdated, KindPermission, KindNotice, KindUsage,
		KindTurnFinished, KindError:
		return true
	}
	return false
}

// ToolStatus 是工具调用的状态。
type ToolStatus string

const (
	ToolPending   ToolStatus = "pending"
	ToolRunning   ToolStatus = "running"
	ToolCompleted ToolStatus = "completed"
	ToolFailed    ToolStatus = "failed"
	ToolCancelled ToolStatus = "cancelled"
)

// Terminal 报告这个状态是否已经结束。
func (s ToolStatus) Terminal() bool {
	switch s {
	case ToolCompleted, ToolFailed, ToolCancelled:
		return true
	}
	return false
}

// PermissionKind 是审批选项的语义。ID 由 Agent 提供，Kind 只用来决定 UI 图标与默认排序。
type PermissionKind string

const (
	PermissionAllowOnce   PermissionKind = "allow_once"
	PermissionAllowAlways PermissionKind = "allow_always"
	PermissionRejectOnce  PermissionKind = "reject_once"
	PermissionRejectAlwa  PermissionKind = "reject_always"
)

// StopReason 是一轮结束的原因。前五个与 ACP 的 StopReason 对齐，error 是本层扩展。
type StopReason string

const (
	StopEndTurn         StopReason = "end_turn"
	StopMaxTokens       StopReason = "max_tokens"
	StopMaxTurnRequests StopReason = "max_turn_requests"
	StopRefusal         StopReason = "refusal"
	StopCancelled       StopReason = "cancelled"
	StopError           StopReason = "error"
	// StopBudget 是进程内 loop 的扩展：轮次/工具/墙钟预算用尽。
	// ACP 没有对应值，适配器按 StopError 或 StopCancelled 处理都可以。
	StopBudget StopReason = "budget_exhausted"
)

// Event 是运行时对外的唯一事件形状。
//
// Seq 由会话分配，单调递增；前端按 Seq 去重/丢弃乱序帧。
// TurnID 把同一轮的 delta、工具、审批绑在一起，跨轮不会串。
type Event struct {
	V          string            `json:"v"`
	Seq        int64             `json:"seq"`
	SessionID  string            `json:"sessionId"`
	TurnID     string            `json:"turnId,omitempty"`
	Kind       EventKind         `json:"kind"`
	At         int64             `json:"at"`
	Text       string            `json:"text,omitempty"`
	Tool       *Tool             `json:"tool,omitempty"`
	Plan       []PlanEntry       `json:"plan,omitempty"`
	Permission *Permission       `json:"permission,omitempty"`
	Notice     *Notice           `json:"notice,omitempty"`
	Usage      *Usage            `json:"usage,omitempty"`
	Stop       *Stop             `json:"stop,omitempty"`
	Error      *Failure          `json:"error,omitempty"`
	Meta       map[string]string `json:"meta,omitempty"`
}

// Tool 描述一次工具调用。Started 时只有 ID/Name/Title/Input，
// Updated 与 Finished 用同一个 ID 回填状态和输出。
type Tool struct {
	ID         string          `json:"id"`
	Name       string          `json:"name,omitempty"`
	Title      string          `json:"title,omitempty"`
	Status     ToolStatus      `json:"status,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     string          `json:"output,omitempty"`
	ExitCode   int             `json:"exitCode,omitempty"`
	DurationMS int64           `json:"durationMs,omitempty"`
	Locations  []Location      `json:"locations,omitempty"`
}

// Location 是工具触碰的文件位置，用于在 UI 里跳转。
type Location struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

// PlanEntry 是一条计划项。Status 取值 pending / in_progress / completed / cancelled。
type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status,omitempty"`
}

// Permission 是一次审批请求。RespondPermission 时回传的是 Options 里的 ID，
// 不是 Kind：对端可能给出多个同 Kind 但语义不同的选项（例如带规则的 allow）。
type Permission struct {
	ID         string             `json:"id"`
	TurnID     string             `json:"turnId,omitempty"`
	ToolCallID string             `json:"toolCallId,omitempty"`
	Title      string             `json:"title,omitempty"`
	Detail     string             `json:"detail,omitempty"`
	Options    []PermissionOption `json:"options"`
}

// PermissionOption 是一个可选决定。
type PermissionOption struct {
	ID    string         `json:"id"`
	Label string         `json:"label"`
	Kind  PermissionKind `json:"kind"`
}

// Notice 是给用户看的一条提示，不进入对话历史。
type Notice struct {
	Severity    string `json:"severity"` // info / warning / error
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// Usage 是上下文与成本快照。
type Usage struct {
	Used         int     `json:"used"`
	Size         int     `json:"size"`
	CostAmount   float64 `json:"costAmount,omitempty"`
	CostCurrency string  `json:"costCurrency,omitempty"`
}

// Stop 是一轮的结局。FinalText 是这一轮已经流出的完整文本，方便上层直接落历史。
type Stop struct {
	Reason    StopReason `json:"reason"`
	FinalText string     `json:"finalText,omitempty"`
}

// Failure 是可展示的错误。
type Failure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

// Spec 是启动一个运行时会话所需的全部信息。
type Spec struct {
	// ID 是本地会话 ID（通常是 agent id）。事件里的 SessionID 用它，不是对端的 sessionId。
	ID        string   `json:"id"`
	AgentID   string   `json:"agentId,omitempty"`
	WorkDir   string   `json:"workDir"`
	ExtraDirs []string `json:"extraDirs,omitempty"`
	// PermissionMode 是 ask / auto / readonly。适配器不得自行放宽这个档位。
	PermissionMode string            `json:"permissionMode,omitempty"`
	Model          string            `json:"model,omitempty"`
	Env            []string          `json:"env,omitempty"`
	Meta           map[string]string `json:"meta,omitempty"`
}

// Attachment 是随 prompt 带上的本地文件。Data 为空时由适配器按 Path 读取。
type Attachment struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Path     string `json:"path,omitempty"`
	Data     []byte `json:"-"`
}

// Prompt 是一次用户输入。TurnID 为空时由会话生成。
type Prompt struct {
	TurnID      string       `json:"turnId,omitempty"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Turn 是 native 执行函数拿到的这一轮上下文。Ask 在需要审批时调用，
// 它会发 permission.request 事件并阻塞到用户选择或 ctx 取消。
type Turn struct {
	ID   string
	Text string
	Ask  func(ctx context.Context, p Permission) (optionID string, err error)
}

// Runtime 是一个可替换的执行体。Start 只负责把会话拉起来，
// 真正的输入从 Session.Prompt 进去。
type Runtime interface {
	// Name 用于诊断与设置页展示，例如 native / acp:/usr/local/bin/grok。
	Name() string
	Start(ctx context.Context, spec Spec) (Session, error)
}

// Session 是一个已经启动的运行时会话。
//
// Prompt 在「提交成功」后返回，不等这一轮结束；结束通过 turn.finished 事件通知。
// 这样 UI 在等待模型时仍然可以调用 Cancel / RespondPermission。
type Session interface {
	ID() string
	Prompt(ctx context.Context, p Prompt) error
	Cancel(turnID string) error
	RespondPermission(requestID, optionID string) error
	Events() <-chan Event
	Close() error
}

// 协议层错误。适配器必须原样返回这些哨兵值，方便上层区分「忙」和「坏了」。
var (
	ErrBusy        = errors.New("runtime: session is busy")
	ErrClosed      = errors.New("runtime: session is closed")
	ErrUnsupported = errors.New("runtime: operation not supported")
)

// Validate 校验事件是否满足协议。适配器在发出前应校验；测试用它当断言。
func (e Event) Validate() error {
	if e.V != Version {
		return fmt.Errorf("event version = %q, want %q", e.V, Version)
	}
	if e.Seq <= 0 {
		return errors.New("event seq must be positive")
	}
	if e.SessionID == "" {
		return errors.New("event sessionId is empty")
	}
	if !e.Kind.Valid() {
		return fmt.Errorf("unknown event kind %q", e.Kind)
	}
	if e.At <= 0 {
		return errors.New("event at is zero")
	}
	switch e.Kind {
	case KindTextDelta, KindThoughtDelta:
		if e.Text == "" {
			return fmt.Errorf("%s with empty text", e.Kind)
		}
	case KindToolStarted:
		if e.Tool == nil || e.Tool.ID == "" {
			return fmt.Errorf("%s needs tool.id", e.Kind)
		}
		if e.Tool.Status == "" {
			return fmt.Errorf("%s needs tool.status", e.Kind)
		}
	case KindToolUpdated, KindToolFinished:
		if e.Tool == nil || e.Tool.ID == "" {
			return fmt.Errorf("%s needs tool.id", e.Kind)
		}
	case KindPlanUpdated:
		if len(e.Plan) == 0 {
			return fmt.Errorf("%s needs at least one plan entry", e.Kind)
		}
	case KindPermission:
		if e.Permission == nil || e.Permission.ID == "" {
			return fmt.Errorf("%s needs permission.id", e.Kind)
		}
		if len(e.Permission.Options) == 0 {
			return fmt.Errorf("%s needs at least one option", e.Kind)
		}
	case KindTurnFinished:
		if e.Stop == nil || e.Stop.Reason == "" {
			return fmt.Errorf("%s needs stop.reason", e.Kind)
		}
	case KindError:
		if e.Error == nil || e.Error.Message == "" {
			return fmt.Errorf("%s needs error.message", e.Kind)
		}
	}
	return nil
}

// NormalizeKind 把对端未定义的事件类型降级成 notice，而不是丢弃。
// 这是协议兼容性的关键：对端升级后多出一种事件，我们仍然能把信息显示出来。
func NormalizeKind(kind string) (EventKind, string) {
	k := EventKind(strings.TrimSpace(kind))
	if k.Valid() {
		return k, ""
	}
	return KindNotice, fmt.Sprintf("unknown runtime event %q", kind)
}

// nowMS 统一时间源，测试可以替换。
var nowMS = func() int64 { return time.Now().UnixMilli() }
