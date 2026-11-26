package approval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"multi-agent-desktop/internal/tool"
)

func call(name string) tool.Call {
	return tool.Call{
		ID:    "c1",
		Name:  name,
		Input: []byte(`{"path":"a.txt"}`),
		Preview: tool.Preview{
			Title:  "写入文件",
			Detail: "覆盖 a.txt",
			Diff:   "+hello",
		},
	}
}

// fileCall 造一条带具体路径的文件编辑调用。
func fileCall(name, path string) tool.Call {
	return tool.Call{
		ID:    "c1",
		Name:  name,
		Input: []byte(`{"path":"` + path + `"}`),
		Preview: tool.Preview{
			Title:  "写入文件",
			Detail: path,
		},
	}
}

// bashCall 造一条 bash 调用。
func bashCall(command string) tool.Call {
	return tool.Call{
		ID:      "c1",
		Name:    "bash",
		Input:   []byte(`{"command":"` + command + `"}`),
		Preview: tool.Preview{Title: "运行命令", Detail: command},
	}
}

// patchCall 造一条 apply_patch 批量补丁调用，每条目带一个路径。
func patchCall(paths ...string) tool.Call {
	var b strings.Builder
	b.WriteString(`{"patches":[`)
	for i, p := range paths {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"path":%q,"op":"add"}`, p)
	}
	b.WriteString(`]}`)
	return tool.Call{
		ID:      "c1",
		Name:    "apply_patch",
		Input:   []byte(b.String()),
		Preview: tool.Preview{Title: "应用补丁", Detail: strings.Join(paths, ", ")},
	}
}

type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) emit(name string, _ any) {
	r.mu.Lock()
	r.events = append(r.events, name)
	r.mu.Unlock()
}

func (r *recorder) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e == name {
			n++
		}
	}
	return n
}

func waitPending(t *testing.T, c *Center) View {
	t.Helper()
	for i := 0; i < 100; i++ {
		if list := c.Pending(); len(list) == 1 {
			return list[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("approval never entered the queue")
	return View{}
}

func TestApproveBlocksUntilDecide(t *testing.T) {
	rec := &recorder{}
	c := New(rec.emit)
	// 这条用例验证「询问队列」本身，显式钉在谨慎模式，不依赖默认档。
	c.SetMode("a1", ModeAsk)

	done := make(chan bool, 1)
	go func() {
		ok, _ := c.Approve(context.Background(), "a1", "前端", call("write_file"))
		done <- ok
	}()

	pending := waitPending(t, c)
	if pending.Diff != "+hello" || pending.AgentName != "前端" {
		t.Fatalf("card lost preview: %+v", pending)
	}
	for i := 0; i < 20 && rec.count("tool.approval") == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if rec.count("tool.approval") != 1 {
		t.Fatalf("expected one approval event, got %v", rec.events)
	}

	if err := c.Decide(pending.ID, true, false, ""); err != nil {
		t.Fatalf("decide: %v", err)
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("approved call should be allowed")
		}
	case <-time.After(time.Second):
		t.Fatal("approve did not unblock")
	}
	for i := 0; i < 20 && rec.count("tool.approval.done") == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if rec.count("tool.approval.done") != 1 {
		t.Fatalf("expected done event, got %v", rec.events)
	}
}

func TestRememberSkipsNextPrompt(t *testing.T) {
	c := New(nil)
	// 显式谨慎模式：本用例验证「记住后下次不再问」，与默认档无关。
	c.SetMode("a1", ModeAsk)
	c.SetMode("a2", ModeAsk)
	done := make(chan bool, 1)
	go func() {
		ok, _ := c.Approve(context.Background(), "a1", "前端", call("write_file"))
		done <- ok
	}()
	pending := waitPending(t, c)
	if err := c.Decide(pending.ID, true, true, ""); err != nil {
		t.Fatal(err)
	}
	if ok := <-done; !ok {
		t.Fatal("first call should pass")
	}

	// 第二次不再入队，直接放行。
	if ok, _ := c.Approve(context.Background(), "a1", "前端", call("write_file")); !ok {
		t.Fatal("remembered tool should pass without asking")
	}
	if got := len(c.Pending()); got != 0 {
		t.Fatalf("remembered call should not enqueue, pending=%d", got)
	}

	// 另一个会话不受影响。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = c.Approve(ctx, "a2", "后端", call("write_file"))
	}()
	waitPending(t, c)
}

func TestDenyUnblocks(t *testing.T) {
	c := New(nil)
	c.SetMode("a1", ModeAsk)
	done := make(chan bool, 1)
	go func() {
		ok, _ := c.Approve(context.Background(), "a1", "前端", call("bash"))
		done <- ok
	}()
	pending := waitPending(t, c)
	if err := c.Decide(pending.ID, false, false, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-done:
		if ok {
			t.Fatal("deny should not allow")
		}
	case <-time.After(time.Second):
		t.Fatal("deny did not unblock")
	}
	if got := len(c.Pending()); got != 0 {
		t.Fatalf("denied card should be gone, pending=%d", got)
	}
}

func TestCancelledContextDenies(t *testing.T) {
	c := New(nil)
	c.SetMode("a1", ModeAsk)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ok, reason := c.Approve(ctx, "a1", "前端", call("bash")); ok || reason == "" {
		t.Fatalf("cancelled ctx should deny: ok=%v reason=%q", ok, reason)
	}
	if got := len(c.Pending()); got != 0 {
		t.Fatalf("cancelled call should not stay pending, got %d", got)
	}
}

func TestDefaultTimeoutOutlivesSupervisorTurn(t *testing.T) {
	// Supervisor 一轮按 10 分钟算，默认审批等待必须比它长，否则长任务
	// 最后一步的审批会被固定 5 分钟误杀。
	c := New(func(string, any) {})
	if c.timeout <= 10*time.Minute {
		t.Fatalf("default approval timeout = %s, want > 10m", c.timeout)
	}
}

func TestTimeoutDenies(t *testing.T) {
	c := New(nil)
	c.SetMode("a1", ModeAsk)
	c.SetTimeout(60 * time.Millisecond)
	start := time.Now()
	if ok, reason := c.Approve(context.Background(), "a1", "前端", call("bash")); ok {
		t.Fatal("timeout should deny")
	} else if reason == "" {
		t.Fatal("timeout needs a reason")
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatal("returned before timeout")
	}
}

// auto 档（acceptEdits 语义）：工作区内的文件编辑直接放行、不挂卡；
// bash 不享受自动放行，仍然进审批队列。
func TestModeAutoAllowsInRootFileEditButNotBash(t *testing.T) {
	dir := t.TempDir()
	rec := &recorder{}
	c := New(rec.emit)
	c.SetMode("a1", ModeAuto)
	c.SetRoots("a1", dir, nil)

	// 工作区内的 write_file 直接放行。
	ok, why := c.Approve(context.Background(), "a1", "A", fileCall("write_file", "a.txt"))
	if !ok || why != "" {
		t.Fatalf("auto 下工作区内写文件应当直接放行：%v %q", ok, why)
	}
	if rec.count("tool.approval") != 0 {
		t.Fatalf("工作区内文件编辑不该发审批卡：%v", rec.events)
	}

	// bash 在 auto 下仍然要审批：挂卡等人，而不是被 bypass。
	done := make(chan bool, 1)
	go func() {
		allow, _ := c.Approve(context.Background(), "a1", "A", tool.Call{Name: "bash", Input: []byte(`{"command":"ls"}`)})
		done <- allow
	}()
	pending := waitPending(t, c)
	if pending.Tool != "bash" {
		t.Fatalf("auto 下 bash 应当进审批队列，工具=%q", pending.Tool)
	}
	_ = c.Decide(pending.ID, false, false, "")
	<-done
}

func TestModeReadOnlyRejectsImmediately(t *testing.T) {
	c := New(nil)
	c.SetMode("a1", ModeReadOnly)
	done := make(chan struct{})
	var ok bool
	var why string
	go func() {
		ok, why = c.Approve(context.Background(), "a1", "A", tool.Call{Name: "write_file"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("只读模式应当立刻拒绝，而不是挂着等人")
	}
	if ok || !strings.Contains(why, "只读") {
		t.Fatalf("ok=%v why=%q", ok, why)
	}
	if len(c.Pending()) != 0 {
		t.Fatal("只读模式不该留下待批卡")
	}
}

// 会话带 deadline 时，审批等待跟会话生命周期走：短 deadline 到点就拒绝，
// 不能再被固定的 5 分钟上限拖住，也不能自动放行。
func TestApprovalFollowsContextDeadline(t *testing.T) {
	c := New(nil)
	c.SetMode("a1", ModeAsk)
	c.SetTimeout(time.Hour) // 默认上限拉长，确保触发的是 deadline 而不是默认超时
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	start := time.Now()
	ok, reason := c.Approve(ctx, "a1", "前端", call("bash"))
	if ok {
		t.Fatal("deadline 到期应当拒绝")
	}
	if reason == "" {
		t.Fatal("拒绝要带原因")
	}
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Fatalf("返回得太早：%v", elapsed)
	}
	if got := len(c.Pending()); got != 0 {
		t.Fatalf("deadline 结束后不该残留待批卡，pending=%d", got)
	}
}

// 长会话的 deadline 比默认等待上限长时，审批不该被固定上限误杀：
// 在会话还活着的时候批，应当放行。
func TestApprovalOutlivesDefaultTimeoutForLongSession(t *testing.T) {
	c := New(nil)
	c.SetMode("a1", ModeAsk)
	// 模拟被压短的默认上限；会话 deadline 更长，等待以 deadline 为准。
	c.SetTimeout(150 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	var ok bool
	go func() {
		ok, _ = c.Approve(ctx, "a1", "前端", call("bash"))
		close(done)
	}()

	pending := waitPending(t, c)
	// 超过被压短的默认上限之后再批，证明等待没有被默认上限掐断。
	time.Sleep(300 * time.Millisecond)
	if err := c.Decide(pending.ID, true, false, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("审批没有被决定唤醒")
	}
	if !ok {
		t.Fatal("长会话里及时批准应当放行")
	}
}

// 用户中途停止：ctx 取消要拒绝，不能放行，也不能留待批卡。
func TestApprovalRejectedWhenUserStops(t *testing.T) {
	c := New(nil)
	c.SetMode("a1", ModeAsk)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var ok bool
	var reason string
	go func() {
		ok, reason = c.Approve(ctx, "a1", "前端", call("bash"))
		close(done)
	}()
	waitPending(t, c)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("取消后没有唤醒")
	}
	if ok || reason == "" {
		t.Fatalf("停止应当拒绝：ok=%v reason=%q", ok, reason)
	}
	if got := len(c.Pending()); got != 0 {
		t.Fatalf("停止后不该残留待批卡，pending=%d", got)
	}
}

// 未配置 / 脏值回落默认档 auto；已经显式存了 ask / readonly 的会话保持原值。
func TestUnknownModeFallsBackToAuto(t *testing.T) {
	c := New(nil)
	if got := c.ModeOf("unconfigured"); got != ModeAuto {
		t.Fatalf("未配置的会话必须回落默认 auto，实际 %q", got)
	}
	c.SetMode("a1", Mode("sudo-everything"))
	if got := c.ModeOf("a1"); got != ModeAuto {
		t.Fatalf("不认识的模式必须回落默认 auto，实际 %q", got)
	}
	c.SetMode("a2", ModeAsk)
	if got := c.ModeOf("a2"); got != ModeAsk {
		t.Fatalf("显式存的 ask 必须保持 ask，实际 %q", got)
	}
	c.SetMode("a3", ModeReadOnly)
	if got := c.ModeOf("a3"); got != ModeReadOnly {
		t.Fatalf("显式存的 readonly 必须保持 readonly，实际 %q", got)
	}
}

// approveRemember 入队一张卡，按「允许并记住」决定，返回这张卡（带根标识）。
func approveRemember(t *testing.T, c *Center, agentID string, call tool.Call) View {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		ok, _ := c.Approve(context.Background(), agentID, "前端", call)
		done <- ok
	}()
	pending := waitPending(t, c)
	if err := c.Decide(pending.ID, true, true, ""); err != nil {
		t.Fatal(err)
	}
	if !<-done {
		t.Fatal("批准后应当放行")
	}
	return pending
}

// ① 默认（未配置）会话 + 工作区内路径：edit/write 直接放行，不弹卡。
func TestDefaultAutoAllowsInRootWrite(t *testing.T) {
	dir := t.TempDir()
	rec := &recorder{}
	c := New(rec.emit)
	c.SetRoots("a1", dir, nil) // 不 SetMode：验证未配置会话回落 auto

	for _, name := range []string{"write_file", "edit_file"} {
		ok, why := c.Approve(context.Background(), "a1", "A", fileCall(name, "a.txt"))
		if !ok || why != "" {
			t.Fatalf("默认 auto 下工作区内 %s 应当直接放行：%v %q", name, ok, why)
		}
	}
	if rec.count("tool.approval") != 0 {
		t.Fatalf("工作区内文件编辑不该弹审批卡：%v", rec.events)
	}
}

// ② auto 下越出工作区的文件写仍要审批；拿不到根时也保守询问。
func TestAutoOutsideRootStillAsks(t *testing.T) {
	dir := t.TempDir()
	c := New(nil)
	c.SetMode("a1", ModeAuto)
	c.SetRoots("a1", dir, nil)

	outside := filepath.Join(os.TempDir(), "approval-escape.txt")
	done := make(chan bool, 1)
	go func() {
		ok, _ := c.Approve(context.Background(), "a1", "A", fileCall("write_file", outside))
		done <- ok
	}()
	pending := waitPending(t, c)
	_ = c.Decide(pending.ID, false, false, "")
	if ok := <-done; ok {
		t.Fatal("工作区外的写操作在 auto 下仍应被拦下询问")
	}

	// 没登记根的会话：无法判定是否在工作区内，退化到询问而不是放行。
	c2 := New(nil)
	c2.SetMode("a2", ModeAuto)
	go func() {
		_, _ = c2.Approve(context.Background(), "a2", "A", fileCall("write_file", "a.txt"))
	}()
	waitPending(t, c2)
}

// ③ 显式 ask 的会话行为不变：即便路径在工作区内，也要逐次问。
func TestAskSessionStillPromptsInRoot(t *testing.T) {
	dir := t.TempDir()
	c := New(nil)
	c.SetMode("a1", ModeAsk)
	c.SetRoots("a1", dir, nil)
	go func() {
		_, _ = c.Approve(context.Background(), "a1", "A", fileCall("write_file", "a.txt"))
	}()
	waitPending(t, c)
}

// ④ 记忆按 工具 + 根：同根同工具第二次不再问；异根同工具仍会问。
func TestMemoryScopedByRoot(t *testing.T) {
	r1 := t.TempDir()
	r2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(r2, "b.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New(nil)
	c.SetMode("a1", ModeAsk)
	c.SetRoots("a1", r1, []string{r2})

	first := approveRemember(t, c, "a1", fileCall("write_file", "a.txt"))
	if first.Root != r1 {
		t.Fatalf("记住的根应当是主根 %q，实际 %q", r1, first.Root)
	}

	// 同根同工具：第二次不再入队。
	if ok, _ := c.Approve(context.Background(), "a1", "A", fileCall("write_file", "a.txt")); !ok {
		t.Fatal("同根同工具记住后应当直接放行")
	}
	if got := len(c.Pending()); got != 0 {
		t.Fatalf("同根记忆不该再留卡，pending=%d", got)
	}

	// 异根同工具：仍要问。
	go func() {
		_, _ = c.Approve(context.Background(), "a1", "A", fileCall("write_file", filepath.Join(r2, "b.txt")))
	}()
	waitPending(t, c)
}

// ⑤ 旧存档只有裸工具名的记录：不能造成跨根误放行。新查找按 工具+根 拼键，
// 裸名永不命中；恢复时由 Manager 丢弃并记启动日志。
func TestLegacyPlainEntryDoesNotCrossRoot(t *testing.T) {
	r1 := t.TempDir()
	r2 := t.TempDir()
	c := New(nil)
	c.SetMode("a1", ModeAsk)
	c.SetRoots("a1", r1, []string{r2})
	// 模拟旧版存档直接种进内存的裸工具名记录。
	c.mu.Lock()
	c.allowed["a1"] = map[string]bool{"write_file": true}
	c.mu.Unlock()

	// 连 r1 内都不该被这条旧记录放行，更别说 r2。
	go func() {
		_, _ = c.Approve(context.Background(), "a1", "A", fileCall("write_file", "a.txt"))
	}()
	waitPending(t, c)
}

// apply_patch 批量条目只要有一条在根外，整批仍要审批。
func TestAutoPatchRequiresAllEntriesInRoot(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(os.TempDir(), "patch-escape.txt")
	c := New(nil)
	c.SetMode("a1", ModeAuto)
	c.SetRoots("a1", dir, nil)

	go func() {
		_, _ = c.Approve(context.Background(), "a1", "A", patchCall("ok.txt", outside))
	}()
	waitPending(t, c)
}

// 勾过「以后都允许 bash」后，同主根、同命令族签名的第二条 bash 不再弹卡。
func TestMemoryBashSameRootSkips(t *testing.T) {
	dir := t.TempDir()
	c := New(nil)
	c.SetMode("a1", ModeAsk)
	c.SetRoots("a1", dir, nil)

	first := approveRemember(t, c, "a1", bashCall("ls"))
	if first.Root != dir {
		t.Fatalf("bash 记忆应当绑主根 %q，实际 %q", dir, first.Root)
	}
	if first.Sig != "ls" {
		t.Fatalf("单段工具签名应当是 %q，实际 %q", "ls", first.Sig)
	}

	// 同签名（ls 带不同参数）命中记忆。
	if ok, _ := c.Approve(context.Background(), "a1", "A", bashCall("ls -la")); !ok {
		t.Fatal("同主根同签名记住的 bash 应当直接放行")
	}
	if got := len(c.Pending()); got != 0 {
		t.Fatalf("同根记忆不该再留卡，pending=%d", got)
	}
}

// (b) 同一条记忆换到另一个主根：bash 仍然要问。
func TestMemoryBashDifferentRootAsks(t *testing.T) {
	r1 := t.TempDir()
	r2 := t.TempDir()
	c := New(nil)
	c.SetMode("a1", ModeAsk)
	c.SetRoots("a1", r1, nil)
	approveRemember(t, c, "a1", bashCall("ls"))

	// 会话换到另一个项目目录（主根变了），旧记忆不能跨根套用。
	c.SetRoots("a1", r2, nil)
	go func() {
		_, _ = c.Approve(context.Background(), "a1", "A", bashCall("ls"))
	}()
	waitPending(t, c)
}

// (c) 即使 bash 已被记住放行，shellguard 判 Blocked 的高危命令照旧拒绝，
// 记忆不能越过安全层。判定顺序：shellguard → 记忆 → 询问。
func TestMemoryBashBlockedStillDenied(t *testing.T) {
	dir := t.TempDir()
	c := New(nil)
	c.SetMode("a1", ModeAsk)
	c.SetRoots("a1", dir, nil)
	approveRemember(t, c, "a1", bashCall("ls"))

	ok, reason := c.Approve(context.Background(), "a1", "A", bashCall("rm -rf /"))
	if ok {
		t.Fatal("高危命令即使记过允许也必须拒绝")
	}
	if reason == "" {
		t.Fatal("拒绝要带策略原因")
	}
	if got := len(c.Pending()); got != 0 {
		t.Fatalf("被拦的命令不该留待批卡，pending=%d", got)
	}
}

// (d) 默认 auto、没有记忆时，首条 bash 仍要问。
func TestAutoBashFirstAsksWithoutMemory(t *testing.T) {
	dir := t.TempDir()
	c := New(nil)
	c.SetMode("a1", ModeAuto)
	c.SetRoots("a1", dir, nil)

	go func() {
		_, _ = c.Approve(context.Background(), "a1", "A", bashCall("ls"))
	}()
	waitPending(t, c)
}

// (a) 记住 `go test`：同根 `go test ./internal/tool/` 不再问；`go vet ./...` 仍问。
func TestMemoryBashSignatureScopesByFamily(t *testing.T) {
	dir := t.TempDir()
	c := New(nil)
	c.SetMode("a1", ModeAsk)
	c.SetRoots("a1", dir, nil)

	first := approveRemember(t, c, "a1", bashCall("go test ./..."))
	if first.Sig != "go test" {
		t.Fatalf("多段工具签名应当是 %q，实际 %q", "go test", first.Sig)
	}

	// 同子命令、不同参数：命中记忆。
	if ok, _ := c.Approve(context.Background(), "a1", "A", bashCall("go test ./internal/tool/")); !ok {
		t.Fatal("同根同签名 go test 应当直接放行")
	}
	if got := len(c.Pending()); got != 0 {
		t.Fatalf("同签名记忆不该再留卡，pending=%d", got)
	}

	// 不同子命令 go vet：仍要问。
	go func() {
		_, _ = c.Approve(context.Background(), "a1", "A", bashCall("go vet ./..."))
	}()
	waitPending(t, c)
}

// (b) 记住 `git status`：`git push` 不命中，仍要问。
func TestMemoryBashGitStatusDoesNotCoverPush(t *testing.T) {
	dir := t.TempDir()
	c := New(nil)
	c.SetMode("a1", ModeAsk)
	c.SetRoots("a1", dir, nil)
	approveRemember(t, c, "a1", bashCall("git status"))

	go func() {
		_, _ = c.Approve(context.Background(), "a1", "A", bashCall("git push"))
	}()
	waitPending(t, c)
}

// (c) 解析不出签名的命令（管道起头）不入记忆，勾了「以后都允许」第二次仍要问。
func TestDegenerateBashSignatureNotRemembered(t *testing.T) {
	dir := t.TempDir()
	c := New(nil)
	c.SetMode("a1", ModeAsk)
	c.SetRoots("a1", dir, nil)

	first := approveRemember(t, c, "a1", bashCall("| grep foo"))
	if first.Sig != "" {
		t.Fatalf("退化命令不该产生签名，实际 %q", first.Sig)
	}

	go func() {
		_, _ = c.Approve(context.Background(), "a1", "A", bashCall("| grep bar"))
	}()
	waitPending(t, c)
}
