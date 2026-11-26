// Package approval 把写操作的审批从「同步 if」变成一条可等待的队列。
//
// 循环在 worker goroutine 里跑，审批必须能跨 goroutine：worker 挂起等，
// 用户在界面上点允许或拒绝，再把结果送回。等待期间按 ctx 取消，
// 所以「停止」不会留下一个永远挂着的审批。
package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"multi-agent-desktop/internal/shellguard"
	"multi-agent-desktop/internal/tool"
	"multi-agent-desktop/internal/workdir"
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
	// Root 是这次调用命中的工作区根（绝对路径）；拿不到根时为空。
	// 「以后都允许」按 工具 + 这个根 记忆，落盘时带回 Manager。
	Root string `json:"root,omitempty"`
	// Sig 是 bash 的命令族签名（如 "go test"）；文件工具为空。
	// 签名解析不出时为空，这条调用不入记忆。
	Sig string `json:"sig,omitempty"`
}

// Mode 是这个会话的权限模式。
//
// 三档语义不重叠，各自对应真实场景：
//   - ask：谨慎模式，每次写操作都问；
//   - auto：默认档，语义对标 Claude Code 的 acceptEdits——只自动放行
//     工作区根内的文件编辑类工具（write_file / edit_file / apply_patch）；
//     bash 与其他写操作不享受自动放行，仍走审批队列；
//   - readonly：只读，写操作直接拒，不问也不等人
//
// 注意：auto 不等于「完整权限」。工作目录是工具层的硬边界（文件工具不出目录，
// bash 的 cwd 就是它），审批模式不应该能把这条边界放开。
// 无论哪一档，shellguard 对危险命令的拦截都照旧生效（在工具层 Preview /
// Execute 里各查一次，审批中心不碰、也不削弱它）。
type Mode string

const (
	ModeAsk      Mode = "ask"
	ModeAuto     Mode = "auto"
	ModeReadOnly Mode = "readonly"
)

// ParseMode 把界面传来的字符串收成合法模式。
// 空值与不认识的值回落 auto（新会话 / 未配置会话的默认体验）；
// 已经显式存了 ask / readonly 的会话不会被这里改写——SetMode 落盘的原值
// 仍是合法档，只有缺配置或脏数据才会落到默认档。
func ParseMode(s string) Mode {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeAsk:
		return ModeAsk
	case ModeReadOnly:
		return ModeReadOnly
	default:
		return ModeAuto
	}
}

// defaultTimeout 是没人处理审批时的兜底上限。
// 以前固定 5 分钟，比 Supervisor 一轮 10 分钟还短，长任务会在最后一步被
// 误判成“等待审批超时”。这里放宽到 30 分钟；真正的取消仍由 ctx 负责，
// 超时依旧按拒绝处理，绝不自动放行。
const defaultTimeout = 30 * time.Minute

// Center 管理所有等待中的审批。
type Center struct {
	mu      sync.Mutex
	emit    func(event string, data any)
	pending map[string]*waiter
	order   []string
	// allowed 记「本会话对这个工具 + 这个根目录以后都放行」。
	// 内层键见 memKey：新语义是「工具 + 命中的工作区根」，不再是裸工具名。
	allowed map[string]map[string]bool
	// modes 是每个会话的权限模式；未配置的会话按 ParseMode 回落 auto。
	modes map[string]Mode
	// roots 是每个会话能碰的目录集合（主目录 + 附加目录，绝对路径）。
	// 由 Manager 在选目录 / 恢复存档时同步过来；auto 档靠它判断文件是否在工作区内。
	roots   map[string]workdir.Roots
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
		modes:   map[string]Mode{},
		roots:   map[string]workdir.Roots{},
		timeout: defaultTimeout,
	}
}

// keySep 把「工具名」和「根目录标识」拼成一个内层记忆键。用单元分隔符，
// 不会出现在工具名或普通路径里，避免两段互相串味。
const keySep = "\x1f"

// toolLevelRoot 是拿不到根目录时的退化记忆标识：表示这条记忆只记到工具级，
// 不绑任何根。退化原因见 rootKeyFor 的注释。
const toolLevelRoot = "*"

// memKey 拼内层记忆键。rootKey 为空时退化为工具级记忆；sig 非空时（bash）
// 再追加一段命令族签名，把记忆收敛到「根 + 命令族」而不是整根放行。
func memKey(toolName, rootKey, sig string) string {
	if rootKey == "" {
		rootKey = toolLevelRoot
	}
	parts := []string{toolName, rootKey}
	if sig != "" {
		parts = append(parts, sig)
	}
	return strings.Join(parts, keySep)
}

// SetRoots 同步一个会话的工作区根目录集合。main 为主工作目录，extra 为附加目录。
// main 为空时清掉这个会话的根（此后 auto 档无法判定目标是否在工作区内，
// 保守回落到询问）。目录非法时保留旧值不动。
func (c *Center) SetRoots(agentID, main string, extra []string) {
	if c == nil || agentID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(main) == "" {
		delete(c.roots, agentID)
		return
	}
	roots, err := workdir.NewRoots(main, extra...)
	if err != nil {
		return
	}
	c.roots[agentID] = roots
}

// rootsOf 读一个会话的根目录集合，返回副本。
func (c *Center) rootsOf(agentID string) workdir.Roots {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append(workdir.Roots(nil), c.roots[agentID]...)
	return out
}

// SetMode 改一个会话的权限模式。
func (c *Center) SetMode(agentID string, m Mode) {
	if c == nil || agentID == "" {
		return
	}
	c.mu.Lock()
	c.modes[agentID] = ParseMode(string(m))
	c.mu.Unlock()
}

// ModeOf 读一个会话当前的权限模式。未配置 / 脏值回落默认档 auto。
func (c *Center) ModeOf(agentID string) Mode {
	if c == nil {
		return ModeAuto
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return ParseMode(string(c.modes[agentID]))
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
	// 权限模式先于逐次审批。readonly 直接拒，不要挂一张没人能批的卡；
	// auto 只放工作区内的文件编辑，放不出去的继续走下面的审批队列。
	switch c.ModeOf(agentID) {
	case ModeReadOnly:
		return false, "这个会话是只读模式，不执行写操作。要改东西先把权限模式改成谨慎模式或自动。"
	case ModeAuto:
		if c.autoAllow(agentID, call) {
			return true, ""
		}
	}
	// shellguard 前置且优先：哪怕这条命令以前勾过「以后都允许」，高危命令
	// 照样拒。判定顺序固定为 shellguard → 记忆 → 询问，任何一步拒绝即终止。
	if call.Name == "bash" {
		if blocked, reason := shellguard.Check(callCommand(call)); blocked {
			return false, "operation blocked by policy: " + reason
		}
	}
	rootKey := c.rootKeyFor(agentID, call.Name, call)
	// bash 再收敛一层命令族签名；解析不出时 sig 为空，不入记忆、仍走询问。
	sig := ""
	if call.Name == "bash" {
		sig, _ = bashSignature(callCommand(call))
	}
	if c.remembered(agentID, call.Name, rootKey, sig) {
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
		Root:      rootKey,
		Sig:       sig,
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

	// 等待上限跟会话生命周期对齐：ctx 带 deadline（长会话/看门狗）时以
	// 剩余时间为准，没有 deadline 才用默认上限。注意这只是等待时长的边界，
	// 不会把没人批的写操作放过去——任何一路超时/取消都是拒绝。
	wait := c.timeout
	if dl, ok := ctx.Deadline(); ok {
		wait = time.Until(dl)
	}
	if wait <= 0 {
		wait = time.Millisecond
	}

	select {
	case d := <-w.ch:
		if d.remember && d.allow {
			// 签名解析不出的 bash 不入记忆（sig 为空）；文件工具没有签名段。
			if call.Name != "bash" || sig != "" {
				c.remember(agentID, call.Name, rootKey, sig)
			}
		}
		return d.allow, d.reason
	case <-ctx.Done():
		return false, "这一轮已停止，审批取消"
	case <-time.After(wait):
		return false, "等待审批超时"
	}
}

// fileWriteTools 是 auto 档可以自动放行的「工作区内文件编辑类」工具白名单。
// bash 不在里面：命令执行仍按规则走审批，危险命令由 shellguard 在工具层拦截。
var fileWriteTools = map[string]bool{
	"write_file":  true,
	"edit_file":   true,
	"apply_patch": true,
}

// autoAllow 判定 auto 档能不能直接放行这一次调用。
//
// 放行条件必须同时成立：
//  1. 工具在文件编辑白名单里（bash、外部 ACP 请求、其他内部写工具都不在）；
//  2. 会话登记了工作区根，且调用涉及的每个路径都落在根内。
//
// 任何一条不成立都返回 false，落回审批队列——auto 不是 bypassPermissions。
// 拿不到根目录时无法证明目标在工作区内，保守询问，不放行。
func (c *Center) autoAllow(agentID string, call tool.Call) bool {
	if call.Name == "bash" || !fileWriteTools[call.Name] {
		return false
	}
	roots := c.rootsOf(agentID)
	if len(roots) == 0 {
		return false
	}
	paths := callPaths(call)
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		abs, err := roots.Resolve(p)
		if err != nil || !roots.Contains(abs) {
			return false
		}
	}
	return true
}

// rootKeyFor 算出这次调用要记到哪个根目录上。
//
// 命中具体根时返回该根的绝对路径；拿不到根（会话没登记目录、路径解析失败、
// 工具不在记忆范围内）时返回空串，记忆退化为工具级——不绑根，只记这个工具。
// 退化是不得已：没有根上下文就无法把记忆限定到某个目录，宁可记宽一点，
// 也不在审批时凭空造一个根。
//
// bash 的 cwd 就是主根（附加目录只给文件工具读写），所以 bash 的记忆绑主根：
// 同主根记住后不再问，换主根仍然要问。
func (c *Center) rootKeyFor(agentID, toolName string, call tool.Call) string {
	roots := c.rootsOf(agentID)
	if len(roots) == 0 {
		return ""
	}
	if toolName == "bash" {
		return roots.Main()
	}
	if !fileWriteTools[toolName] {
		return ""
	}
	for _, p := range callPaths(call) {
		abs, err := roots.Resolve(p)
		if err != nil {
			continue
		}
		if root := containingRoot(roots, abs); root != "" {
			return root
		}
	}
	return ""
}

// containingRoot 返回包含 abs 的最具体（最长）根；都不包含时返回空串。
func containingRoot(roots workdir.Roots, abs string) string {
	best := ""
	for _, root := range roots {
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		if rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)) {
			if len(root) > len(best) {
				best = root
			}
		}
	}
	return best
}

// callCommand 抽出 bash 调用的 command 参数。解析失败返回空串——
// 空串过不了 shellguard（它对空串放行），后续仍有审批队列兜底，不会因此误放。
func callCommand(call tool.Call) string {
	if len(call.Input) == 0 {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(call.Input, &in); err != nil {
		return ""
	}
	return in.Command
}

// callPaths 从调用入参里抽出文件路径。只认文件编辑工具用的两种形状：
// 单文件 {"path": ...}，补丁批量 {"patches": [{"path": ...}, ...]}。
// 解析失败或没有路径时返回空切片，调用方按保守处理。
func callPaths(call tool.Call) []string {
	if len(call.Input) == 0 {
		return nil
	}
	var in struct {
		Path    string `json:"path"`
		Patches []struct {
			Path string `json:"path"`
		} `json:"patches"`
	}
	if err := json.Unmarshal(call.Input, &in); err != nil {
		return nil
	}
	var out []string
	if strings.TrimSpace(in.Path) != "" {
		out = append(out, in.Path)
	}
	for _, p := range in.Patches {
		if strings.TrimSpace(p.Path) != "" {
			out = append(out, p.Path)
		}
	}
	return out
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

// remembered 查「这个工具 + 这个根 (+ 命令族签名)」是否已经记过放行。
//
// 兼容说明：旧版存档的内层键是裸工具名（没有根标识）。新查找按 memKey 拼键，
// 裸工具名永远不会命中新键——也就是说旧记录不会造成跨根误放行。恢复存档时
// Manager 会把裸工具名的旧条目丢弃并记日志，不在这里静默当成工具级通配。
func (c *Center) remembered(agentID, toolName, rootKey, sig string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.allowed[agentID] != nil && c.allowed[agentID][memKey(toolName, rootKey, sig)]
}

// AllowTool 记下「本会话对这个工具 + 这个根 (+ 签名) 以后都放行」。
// 恢复落盘的放行清单时调它。rootKey 为空表示退化到工具级记忆（见 rootKeyFor）。
func (c *Center) AllowTool(agentID, toolName, rootKey, sig string) {
	if c == nil || agentID == "" || toolName == "" {
		return
	}
	c.remember(agentID, toolName, rootKey, sig)
}

func (c *Center) remember(agentID, toolName, rootKey, sig string) {
	c.mu.Lock()
	if c.allowed[agentID] == nil {
		c.allowed[agentID] = map[string]bool{}
	}
	c.allowed[agentID][memKey(toolName, rootKey, sig)] = true
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
