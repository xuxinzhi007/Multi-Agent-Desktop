package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"multi-agent-desktop/internal/diff"
	"multi-agent-desktop/internal/proc"
	"multi-agent-desktop/internal/shellguard"
	"multi-agent-desktop/internal/tool"
	"multi-agent-desktop/internal/workdir"
)

// Bash 在指定工作目录执行 shell 命令。
//
// 治理要点（每一条都对应一种真实事故）：
//   - 超时后杀整个进程组，否则孙进程会继续持有 stdout，"停止"形同虚设
//   - 输出上限，否则一条 find / 就能撑爆上下文
//   - 非零退出码不是错误，是结果；模型需要看到 stderr 才能自我修正
type Bash struct {
	// WorkDir 是命令的默认工作目录。
	WorkDir string
	// Timeout 是单条命令的硬上限。
	Timeout time.Duration
	// MaxOutput 限制 stdout+stderr 合计采集的字节数。
	MaxOutput int
}

func (*Bash) Name() string { return "bash" }

func (*Bash) Description() string {
	shell := "a POSIX shell (zsh -dfc, or sh -c)"
	if runtime.GOOS == "windows" {
		shell = "PowerShell (-NoProfile -NonInteractive). Do not use bash syntax such as && or heredocs unless you know they work in PowerShell"
	}
	return "Run " + shell + ". Returns stdout and stderr. " +
		"Prefer read_file/write_file/edit_file for file operations — " +
		"they are validated and reversible."
}

func (*Bash) Writes() bool { return true }

func (*Bash) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {"type": "string"},
			"description": {"type": "string"}
		},
		"required": ["command"],
		"additionalProperties": false
	}`)
}

// Preview 给审批卡看：要跑什么命令、在哪个目录。
// 明显毁盘的写法在这里就拦掉，不浪费用户一次点击。
func (b *Bash) Preview(ctx context.Context, raw json.RawMessage) (tool.Preview, error) {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Preview{}, fmt.Errorf("cannot parse arguments: %v", err)
	}
	cmd := strings.TrimSpace(in.Command)
	if cmd == "" {
		return tool.Preview{}, fmt.Errorf("command must not be empty")
	}
	if blocked, reason := shellguard.Check(cmd); blocked {
		return tool.Preview{Blocked: true, Reason: reason}, nil
	}
	dir := b.WorkDir
	if dir == "" {
		dir = "(未设置)"
	}
	return tool.Preview{
		Title:  "运行命令",
		Detail: "目录：" + dir + "\n" + cmd,
	}, nil
}

func (b *Bash) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		Command     string `json:"command"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Fail("cannot parse arguments: %v", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return tool.Fail("command must not be empty")
	}
	if blocked, reason := shellguard.Check(in.Command); blocked {
		return tool.Result{Status: tool.StatusDenied, Content: "operation blocked by policy: " + reason}
	}
	if workdir.TooBroad(b.WorkDir) {
		return tool.Fail("%s", workdir.RejectReason)
	}

	timeout := b.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := proc.CommandContext(execCtx, in.Command)
	if b.WorkDir != "" {
		cmd.Dir = b.WorkDir
	}
	// 关键：独立进程组 + 取消时杀整组。
	// shell 派生的子进程不会随父进程退出而退出，
	// 不杀组的话取消 ctx 之后管道仍然打开，上层就永远等不到关闭。
	proc.Setup(cmd)

	var buf bytes.Buffer
	limit := b.MaxOutput
	if limit <= 0 {
		limit = 64 << 10
	}
	// 用 limitWriter 而不是 bytes.Buffer 直接接管道：
	// 超大输出（比如 cat 一个二进制文件）会把内存吃掉
	lw := &limitWriter{w: &buf, n: limit}
	cmd.Stdout = lw
	cmd.Stderr = lw

	start := time.Now()
	runErr := cmd.Run()
	dur := int(time.Since(start).Milliseconds())

	out := buf.String()
	code := 0
	if runErr != nil {
		// 先看 ctx：超时被杀时 runErr 是 "signal: killed"，
		// 它同样满足 *exec.ExitError，所以顺序不能反 ——
		// 反了就会把「超时」误报成「退出码 -1」，模型会以为是命令写错了
		// 而不是执行超时，于是重试同一个命令。
		if execCtx.Err() == context.DeadlineExceeded || execCtx.Err() == context.Canceled {
			return tool.Result{
				Status:  tool.StatusTimeout,
				Content: fmt.Sprintf("超时\n%s", out),
				Meta:    tool.Meta{DurationMS: dur},
			}
		}
		if ee, ok := runErr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			return tool.Fail("cannot start command: %v", runErr)
		}
	}

	if out == "" {
		out = "(no output)"
	}
	if code != 0 {
		// 非零退出不是「工具坏了」，是「命令失败了」。
		// 明确告诉模型退出码，它才知道该改参数还是该换命令。
		return tool.Result{
			Status:  tool.StatusOK,
			Content: fmt.Sprintf("exit code %d\n%s", code, out),
			Meta:    tool.Meta{DurationMS: dur, ExitCode: code},
		}
	}
	return tool.Result{
		Status:  tool.StatusOK,
		Content: out,
		Meta:    tool.Meta{DurationMS: dur},
	}
}

// limitWriter 只接受前 n 个字节，其余丢弃并标记。
type limitWriter struct {
	w       *bytes.Buffer
	n       int
	dropped int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	remain := l.n - l.w.Len()
	if remain <= 0 {
		l.dropped += len(p)
		return len(p), nil
	}
	if len(p) > remain {
		l.w.Write(p[:remain])
		l.dropped += len(p) - remain
		return len(p), nil
	}
	return l.w.Write(p)
}

// safePath 把模型给的路径解析成绝对路径，并限制在工作目录内。
//
// 不做这个检查的话，模型可以 read_file("/etc/passwd")。
// 它不是恶意，只是不知道边界在哪 —— 所以边界必须由代码强制。
//
// 多根走 rootsPath；这里保留单根形式，调用方没给附加目录时行为不变。
func safePath(workDir, p string) (string, error) {
	return rootsPath(workDir, nil, p)
}

// rootsPath 在主目录 + 附加目录这组边界内解析路径。
func rootsPath(workDir string, extra []string, p string) (string, error) {
	if workDir == "" {
		if strings.TrimSpace(p) == "" {
			return "", fmt.Errorf("path must not be empty")
		}
		return filepath.Clean(p), nil
	}
	roots, err := workdir.NewRoots(workDir, extra...)
	if err != nil {
		return "", err
	}
	return roots.Resolve(p)
}

// ReadFile 读取文本文件，带行号。
//
// 行号是刻意加的：模型拿到行号才能在 edit_file 里精确定位。
// 不带行号的 read_file 会让后续编辑变成"猜位置"。
type ReadFile struct {
	WorkDir string
	// ExtraDirs 是附加目录：只给文件读写，不改 shell 的 cwd。
	ExtraDirs []string
}

func (*ReadFile) Name() string        { return "read_file" }
func (*ReadFile) Writes() bool        { return false }
func (*ReadFile) Description() string { return "Read a text file. Returns content with line numbers." }

func (*ReadFile) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"offset": {"type": "integer", "minimum": 0},
			"limit": {"type": "integer", "minimum": 1}
		},
		"required": ["path"],
		"additionalProperties": false
	}`)
}

func (r *ReadFile) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Fail("cannot parse arguments: %v", err)
	}
	abs, err := rootsPath(r.WorkDir, r.ExtraDirs, in.Path)
	if err != nil {
		return tool.Fail("%v", err)
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return tool.Fail("file not found: %s", in.Path)
		}
		return tool.Fail("cannot read %s: %v", in.Path, err)
	}
	if bytes.IndexByte(data[:min(len(data), 512)], 0) >= 0 {
		return tool.Fail("%s looks like a binary file; use bash with an appropriate tool", in.Path)
	}

	lines := strings.Split(string(data), "\n")
	// 末尾换行会切出一个空元素，去掉
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	start := in.Offset
	if start > len(lines) {
		start = len(lines)
	}
	end := len(lines)
	if in.Limit > 0 && start+in.Limit < end {
		end = start + in.Limit
	}

	var sb strings.Builder
	for i := start; i < end; i++ {
		fmt.Fprintf(&sb, "%6d\t%s\n", i+1, lines[i])
	}
	if start >= end {
		sb.WriteString("(empty range)\n")
	}
	if end < len(lines) {
		fmt.Fprintf(&sb, "... (%d more lines)\n", len(lines)-end)
	}
	return tool.OK(sb.String())
}

// WriteFile 写入整个文件。
//
// 明确的写操作，所以 Writes() = true，执行前需要审批。
// 不做"自动创建目录"以外的智能推断 —— 模型应该自己说清楚要写什么。
type WriteFile struct {
	WorkDir   string
	ExtraDirs []string
}

func (*WriteFile) Name() string { return "write_file" }
func (*WriteFile) Writes() bool { return true }
func (*WriteFile) Description() string {
	return "Write content to a file, replacing it entirely. Creates parent directories."
}

func (*WriteFile) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"content": {"type": "string"}
		},
		"required": ["path", "content"],
		"additionalProperties": false
	}`)
}

// Preview 先算出「会改成什么样」，审批卡和检查器都靠它。
func (w *WriteFile) Preview(ctx context.Context, raw json.RawMessage) (tool.Preview, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Preview{}, fmt.Errorf("cannot parse arguments: %v", err)
	}
	abs, err := rootsPath(w.WorkDir, w.ExtraDirs, in.Path)
	if err != nil {
		return tool.Preview{}, err
	}
	before, _ := os.ReadFile(abs)
	detail := fmt.Sprintf("覆盖 %s：%d 字节 → %d 字节", in.Path, len(before), len(in.Content))
	if _, err := os.Stat(abs); os.IsNotExist(err) {
		detail = fmt.Sprintf("新建 %s：%d 字节", in.Path, len(in.Content))
	}
	return tool.Preview{
		Title:  "写入文件",
		Detail: detail,
		Diff:   diff.Unified(displayPath(in.Path), string(before), in.Content, 3),
	}, nil
}

func (w *WriteFile) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Fail("cannot parse arguments: %v", err)
	}
	abs, err := rootsPath(w.WorkDir, w.ExtraDirs, in.Path)
	if err != nil {
		return tool.Fail("%v", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return tool.Fail("cannot create parent directory: %v", err)
	}
	before, _ := os.ReadFile(abs)
	if err := os.WriteFile(abs, []byte(in.Content), 0o644); err != nil {
		return tool.Fail("cannot write %s: %v", in.Path, err)
	}
	res := tool.OK(fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path))
	res.Meta.Path = displayPath(in.Path)
	res.Meta.Diff = diff.Unified(displayPath(in.Path), string(before), in.Content, 3)
	return res
}

// EditFile 精确替换。
//
// 要求 oldText 在文件里唯一出现 —— 不唯一就报错而不是"替换第一个"。
// 后者是最容易造成静默错误的操作：模型以为改对了，实际改了另一处。
type EditFile struct {
	WorkDir   string
	ExtraDirs []string
}

func (*EditFile) Name() string { return "edit_file" }
func (*EditFile) Writes() bool { return true }
func (*EditFile) Description() string {
	return "Replace an exact string in a file. oldText must appear exactly once."
}

func (*EditFile) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"oldText": {"type": "string"},
			"newText": {"type": "string"}
		},
		"required": ["path", "oldText", "newText"],
		"additionalProperties": false
	}`)
}

type editPlan struct {
	Path   string
	Abs    string
	Before string
	After  string
}

// plan 做所有只读校验，算出替换后的全文。
// Preview 和 Execute 共用，保证「审批时看到的」就是「真要写的」。
func (e *EditFile) plan(raw json.RawMessage) (editPlan, error) {
	var in struct {
		Path    string `json:"path"`
		OldText string `json:"oldText"`
		NewText string `json:"newText"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return editPlan{}, fmt.Errorf("cannot parse arguments: %v", err)
	}
	if in.OldText == "" {
		return editPlan{}, fmt.Errorf("oldText must not be empty")
	}
	if in.OldText == in.NewText {
		return editPlan{}, fmt.Errorf("oldText and newText are identical; nothing to do")
	}
	abs, err := rootsPath(e.WorkDir, e.ExtraDirs, in.Path)
	if err != nil {
		return editPlan{}, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return editPlan{}, fmt.Errorf("cannot read %s: %v", in.Path, err)
	}
	src := string(data)
	n := strings.Count(src, in.OldText)
	if n == 0 {
		return editPlan{}, fmt.Errorf(
			"oldText not found in %s. Read the file again and copy the exact text, "+
				"including whitespace and indentation.", in.Path)
	}
	if n > 1 {
		return editPlan{}, fmt.Errorf(
			"oldText appears %d times in %s; it must be unique. "+
				"Include more surrounding context to disambiguate.", n, in.Path)
	}
	return editPlan{
		Path:   in.Path,
		Abs:    abs,
		Before: src,
		After:  strings.Replace(src, in.OldText, in.NewText, 1),
	}, nil
}

func (e *EditFile) Preview(ctx context.Context, raw json.RawMessage) (tool.Preview, error) {
	p, err := e.plan(raw)
	if err != nil {
		return tool.Preview{}, err
	}
	return tool.Preview{
		Title:  "编辑文件",
		Detail: fmt.Sprintf("编辑 %s：%d 字节 → %d 字节", p.Path, len(p.Before), len(p.After)),
		Diff:   diff.Unified(displayPath(p.Path), p.Before, p.After, 3),
	}, nil
}

func (e *EditFile) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	p, err := e.plan(raw)
	if err != nil {
		return tool.Fail("%v", err)
	}
	if err := os.WriteFile(p.Abs, []byte(p.After), 0o644); err != nil {
		return tool.Fail("cannot write %s: %v", p.Path, err)
	}
	res := tool.OK(fmt.Sprintf("edited %s (%d bytes -> %d bytes)", p.Path, len(p.Before), len(p.After)))
	res.Meta.Path = displayPath(p.Path)
	res.Meta.Diff = diff.Unified(displayPath(p.Path), p.Before, p.After, 3)
	return res
}

// displayPath 把相对路径统一成 slash，diff 头里不出现反斜杠。
func displayPath(p string) string {
	return strings.ReplaceAll(filepath.ToSlash(p), "\\", "/")
}

// Register 把 4 个原语工具装进注册表。
func Register(r *tool.Registry, workDir string) {
	RegisterRoots(r, workDir, nil)
}

// RegisterRoots 装文件与 shell 工具。extra 是附加目录：
// 文件工具能读写它们，bash 的 cwd 仍是主目录——命令在哪跑必须是确定的。
func RegisterRoots(r *tool.Registry, workDir string, extra []string) {
	r.Register(&ReadFile{WorkDir: workDir, ExtraDirs: extra})
	r.Register(&WriteFile{WorkDir: workDir, ExtraDirs: extra})
	r.Register(&EditFile{WorkDir: workDir, ExtraDirs: extra})
	r.Register(&Bash{WorkDir: workDir})
}
