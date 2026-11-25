// Package gateway 是 M3 网关最小闭环的渠道层：入站路由、会话绑定、幂等去重、
// 即时回执、出站回复，以及只读状态 / 审批查询 / SSE 事件。
//
// 本包只通过端口接口向下依赖，不直接 import internal/agent、internal/runtime
// 或桌面 Manager；具体装配（飞书长连接、Manager 接线、审批中心接线）由主进程完成。
package gateway

import (
	"context"
	"errors"
)

// 渠道标识。新渠道在适配器层补充，端口层只认字符串。
const (
	ChannelFeishu   = "feishu"
	ChannelTelegram = "telegram"
	ChannelWeb      = "web"
)

// 绑定策略：mentionOnly 表示群里只有 @ 机器人才回应；allowAll 表示全量回应。
const (
	PolicyMentionOnly = "mentionOnly"
	PolicyAllowAll    = "allowAll"
)

// 出站消息类型。
const (
	OutboundAck    = "ack"    // 即时回执：收到，排队中
	OutboundReply  = "reply"  // 最终回复
	OutboundNotice = "notice" // 状态通知（错误、已停止等）
)

// 端口层向外暴露的错误。Handler 按错误映射 HTTP 状态码，不直接暴露内部细节。
var (
	// ErrNotBound 表示这个渠道会话还没有绑定本地 Agent。调用方不得伪造会话。
	ErrNotBound = errors.New("渠道会话未绑定 Agent")
	// ErrNotWired 表示对应端口还没接进真实内核（测试或主进程装配前）。
	ErrNotWired = errors.New("端口尚未接线")
	// ErrDuplicate 表示同一外部事件 ID 已经处理过。
	ErrDuplicate = errors.New("重复事件")
	// ErrChannelNotOwned 表示这条事件/回复来自网关未接管的渠道（默认只接管 web）。
	ErrChannelNotOwned = errors.New("网关未接管该渠道")
)

// 入站处理结果状态。常量集中在端口层，HTTP 层与测试都按它映射。
const (
	// ResultUnsupported 表示渠道不在接管范围内：不去重、不绑定、不回执、不提交。
	ResultUnsupported = "unsupported"
)

// InboundEvent 是渠道消息的抽象。各渠道适配器把原始帧归一化成这个结构，
// 不可信输入只做归一化，不在这里把消息当系统指令拼接。
type InboundEvent struct {
	// EventID 是渠道侧的外部消息 ID，作为幂等去重键。必填。
	EventID string `json:"eventId"`
	// Channel 是渠道标识（feishu / telegram / web）。
	Channel string `json:"channel"`
	// ExternalChatID 是渠道会话 ID（群聊 chat_id 或私聊 id）。
	ExternalChatID string `json:"externalChatId"`
	// SenderID / SenderName 是发送人身份，审批与白名单校验用。
	SenderID   string `json:"senderId,omitempty"`
	SenderName string `json:"senderName,omitempty"`
	// Text 是归一化后的纯文本。
	Text string `json:"text"`
	// Mentioned 表示这条消息是否 @ 了机器人。mentionOnly 策略下据此判断是否回应。
	Mentioned bool `json:"mentioned,omitempty"`
	// Kind 是消息类型：message / system。默认 message。
	Kind string `json:"kind,omitempty"`
}

// OutboundMessage 是网关发回渠道的消息。
type OutboundMessage struct {
	Channel        string `json:"channel"`
	ExternalChatID string `json:"externalChatId"`
	// Kind 为 ack / reply / notice。
	Kind string `json:"kind"`
	Text string `json:"text"`
	// InReplyToEvent 是对应的入站事件 ID，便于渠道侧做引用回复。
	InReplyToEvent string `json:"inReplyToEvent,omitempty"`
}

// Sender 把出站消息写回渠道。实现方负责渠道 SDK 的发送细节。
type Sender interface {
	Send(ctx context.Context, msg OutboundMessage) error
}

// SessionStatus 是会话的只读快照，供状态 / 会话列表接口使用。
type SessionStatus struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"` // idle / queued / running / blocked / done / error
	QueueLen  int    `json:"queueLen"`
	LastError string `json:"lastError,omitempty"`
}

// SessionPort 是网关对本地会话内核的最小依赖面，对应 agent.Manager 的
// Prompt / Stop / List / Status。不暴露会话历史与内部运行细节。
type SessionPort interface {
	// Prompt 把一条用户输入交给会话排队执行。立即返回，执行结果走事件流。
	Prompt(ctx context.Context, agentID, text string) error
	// Stop 取消会话当前执行并清空队列。返回是否命中会话。
	Stop(agentID string) bool
	// Status 读单个会话快照；不存在时第二返回值为 false。
	Status(agentID string) (SessionStatus, bool)
	// List 返回所有会话快照。
	List() []SessionStatus
}

// ApprovalView 是一张待审批卡片的只读视图，形状对齐 approval.View。
type ApprovalView struct {
	ID        string `json:"id"`
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	Tool      string `json:"tool"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Diff      string `json:"diff,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

// ApprovalPort 是网关对审批中心的最小依赖面：查待办、转发决定。
type ApprovalPort interface {
	// Pending 返回还没决定的审批，顺序为到达先后。
	Pending() []ApprovalView
	// Decide 转发一次审批决定。remember 表示本会话以后不再问这个工具。
	Decide(id string, allow, remember bool, reason string) error
}

// Binding 是外部渠道会话与本地 Agent 的绑定关系。落盘到网关数据目录。
type Binding struct {
	Channel        string `json:"channel"`
	ExternalChatID string `json:"externalChatId"`
	AgentID        string `json:"agentId"`
	// Policy 为 mentionOnly / allowAll。空按 allowAll 处理。
	Policy    string `json:"policy,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

// Binder 维护 chat ↔ agent 绑定。实现必须并发安全，并按 §4 单写者原则原子落盘。
type Binder interface {
	// Bind 写入或覆盖一条绑定。
	Bind(b Binding) error
	// Resolve 按渠道会话查绑定；未绑定返回 false。
	Resolve(channel, externalChatID string) (Binding, bool)
	// List 返回全部绑定。
	List() []Binding
	// Unbind 解除绑定；不存在时返回 nil（幂等）。
	Unbind(channel, externalChatID string) error
}
