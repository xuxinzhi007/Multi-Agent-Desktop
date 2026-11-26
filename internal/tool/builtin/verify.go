package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"

	"multi-agent-desktop/internal/proc"
	"multi-agent-desktop/internal/shellguard"
	"multi-agent-desktop/internal/tool"
	"multi-agent-desktop/internal/workdir"
)

// Verify 在声称任务完成前跑一次可复现的检查（编译 / 类型检查），
// 把「模型说改完了」变成「有退出码和日志支撑的结论」。
//
// 治理要点：
//   - Writes() = false：只读验证语义，不进审批流，零审批成本。
//     绝不能让它弹出审批卡变成第二个 bash 审批墙。
//   - 默认命令区分 Go 与 TS：Go 侧 `go build ./...`；TS 侧读仓库根
//     config.yaml 的 verify 值。不能拿 TS 的 tsc 命令去验 Go 改动，
//     否则会误判「验过了」。
//   - 失败要回结构化失败：状态、退出码、实际命令、耗时、失败条目 / 包名、
//     前 20 行失败摘要。给模型看的 Content 是人可读摘要，结构化报告放 Meta。
type Verify struct {
	WorkDir   string
	ExtraDirs []string
}

const (
	verifyDefaultTimeoutSec = 180
	verifyMaxTimeoutSec     = 600
	verifyMaxSummaryLines   = 20
	verifyMaxOutput         = 64 << 10
	verifyRawTailBytes      = 4 << 10
)

func (*Verify) Name() string { return "verify" }

// Writes 恒为 false：验证是只读动作，不触发审批。
func (*Verify) Writes() bool { return false }

func (*Verify) Description() string {
	return "Verify the task actually works before claiming it is done. " +
		"This is the ONLY tool that proves completion: a passing verify is required before " +
		"reporting success — do NOT substitute a single bash run for it. Read-only, no approval prompt. " +
		"scope: \"go\" | \"ts\" | \"both\"; omit to infer from the working directory: " +
		"go.mod and package.json both present → both; only go.mod → go; only package.json → ts; neither → go. " +
		"Default commands: Go runs `go build ./...`; TypeScript runs the `verify:` command from config.yaml " +
		"at the repo root (fallback `npx tsc --noEmit`); both runs Go then TypeScript and merges results. " +
		"cmd: optional single build/test command overriding the default (go/npm/npx/pnpm/yarn/cargo/mvn/gradle/pytest/make prefixes only), restricted to the working directory. " +
		"timeoutSec: default 180, upper bound 600."
}

func (*Verify) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"scope": {"type": "string", "enum": ["go", "ts", "both"], "description": "Which check to run. Omit to infer from go.mod / package.json."},
			"cmd": {"type": "string", "description": "Optional single command overriding the default. Runs inside the working directory only."},
			"timeoutSec": {"type": "integer", "minimum": 1, "maximum": 600, "default": 180}
		},
		"additionalProperties": false
	}`)
}

// Timeout 让 Registry 的硬上限覆盖到 verify 的最长允许时间（600s），
// 否则 Registry 默认 120s 会把慢一点的编译误判成超时。
func (*Verify) Timeout() time.Duration { return verifyMaxTimeoutSec * time.Second }

type verifyInput struct {
	Scope      string `json:"scope,omitempty"`
	Cmd        string `json:"cmd,omitempty"`
	TimeoutSec int    `json:"timeoutSec,omitempty"`
}

// verifyStep 是一条待执行的检查。Direct 非空时直 exec（不经 shell）；
// 为空时 Command 是一段交给解释器的脚本。
type verifyStep struct {
	Kind    string
	Command string
	Direct  []string
}

// verifyCommand 是单条命令的结构化结局。
type verifyCommand struct {
	Kind       string   `json:"kind"`
	Command    string   `json:"command"`
	Status     string   `json:"status"` // ok / fail / timeout
	ExitCode   int      `json:"exitCode"`
	DurationMS int      `json:"durationMs"`
	ErrorCount int      `json:"errorCount,omitempty"`
	FailedPkgs []string `json:"failedPackages,omitempty"`
	ErrorLines []string `json:"errorLines,omitempty"`
	// RawTail 是失败/超时命令原始输出的尾部，解析漏掉的上下文留给模型看。
	RawTail string `json:"rawTail,omitempty"`
}

// verifyReport 是整次 verify 的结构化结果。
type verifyReport struct {
	Scope      string          `json:"scope"`
	Status     string          `json:"status"` // ok / fail / timeout
	ExitCode   int             `json:"exitCode"`
	DurationMS int             `json:"durationMs"`
	Commands   []verifyCommand `json:"commands"`
}

// plan 解析入参，定出 scope 与要跑的步骤。默认命令不是硬编码一条：
// Go / TS 各自有默认命令，both 依序两条。
func (v *Verify) plan(in verifyInput) (scope string, steps []verifyStep, override string, err error) {
	scope = strings.TrimSpace(in.Scope)
	if scope == "" {
		scope = inferScope(v.WorkDir)
	}
	switch scope {
	case "go", "ts", "both":
	default:
		return "", nil, "", fmt.Errorf("scope must be go, ts or both, got %q", in.Scope)
	}

	override = strings.TrimSpace(in.Cmd)
	if override != "" {
		// 覆盖模式：只跑这一条命令。kind 取 scope 的首个语言用于标注。
		kind := "go"
		if scope == "ts" {
			kind = "ts"
		}
		return scope, []verifyStep{{Kind: kind, Command: override}}, override, nil
	}

	switch scope {
	case "go":
		steps = append(steps, goVerifySteps()...)
	case "ts":
		steps = append(steps, verifyStep{Kind: "ts", Command: tsVerifyCommand(v.WorkDir)})
	case "both":
		steps = append(steps, goVerifySteps()...)
		steps = append(steps, verifyStep{Kind: "ts", Command: tsVerifyCommand(v.WorkDir)})
	}
	return scope, steps, "", nil
}

// goVerifySteps 是 Go 侧的有序默认检查：先 build 再 vet。
// 两条都走 exec 直调，不经 shell；任一条失败整体判 fail，
// vet 输出复用同一套 Go 失败摘要解析。默认不跑全量 go test（太慢）。
func goVerifySteps() []verifyStep {
	return []verifyStep{
		{Kind: "go", Command: "go build ./...", Direct: []string{"go", "build", "./..."}},
		{Kind: "go", Command: "go vet ./...", Direct: []string{"go", "vet", "./..."}},
	}
}

// inferScope 按目录里的标记文件确定性推断语言范围。
func inferScope(workDir string) string {
	hasGo := fileExists(filepath.Join(workDir, "go.mod"))
	hasTS := fileExists(filepath.Join(workDir, "package.json"))
	switch {
	case hasGo && hasTS:
		return "both"
	case hasGo:
		return "go"
	case hasTS:
		return "ts"
	default:
		// 两种标记都没有：保守回落 Go，避免静默跳过编译检查。
		return "go"
	}
}

// tsVerifyCommand 读仓库根 config.yaml 的 verify: 值。
// 文件缺失、键缺失或为空时回落 npx tsc --noEmit。
// 注意：这个值只服务 TS 侧，绝不拿去验 Go 改动。
func tsVerifyCommand(workDir string) string {
	data, err := os.ReadFile(filepath.Join(workDir, "config.yaml"))
	if err != nil {
		return "npx tsc --noEmit"
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "verify") {
			continue
		}
		idx := strings.Index(trimmed, ":")
		if idx < 0 {
			continue
		}
		val := strings.TrimSpace(trimmed[idx+1:])
		val = strings.Trim(val, `"'`)
		if val != "" {
			return val
		}
	}
	return "npx tsc --noEmit"
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// verifyCmdDeniedReason 是 override 命令不在白名单时给模型的结构化说明。
// 引导它改走 bash：bash 是有审批的写/执行通道，verify 不背这个口子。
const verifyCmdDeniedReason = "verify 只接受构建/测试类命令（go build/test/vet/list/mod；" +
	"npm/pnpm/yarn test/lint/build/run build；npx tsc/eslint；cargo build/test/check/clippy；" +
	"mvn/gradle test/build/compile；python -m pytest；make）。其他命令请用 bash（会走审批）。"

// verifyCommandAllowed 用前缀白名单判断 override 是否属于构建/测试类命令。
// 返回空串表示放行；否则返回拒绝原因。
func verifyCommandAllowed(workDir, cmd string) (string, bool) {
	argv, ok := splitArgv(cmd)
	if !ok || len(argv) == 0 {
		return "cmd must be a single parseable command", false
	}
	bin := strings.TrimSuffix(strings.ToLower(argv[0]), ".exe")
	switch bin {
	case "go":
		if len(argv) < 2 || !stringIn(argv[1], "build", "test", "vet", "list", "mod") {
			return verifyCmdDeniedReason, false
		}
	case "npm", "pnpm", "yarn":
		if !pkgScriptAllowed(argv) {
			return verifyCmdDeniedReason, false
		}
	case "npx":
		if len(argv) < 2 || !stringIn(argv[1], "tsc", "eslint", "tslint", "biome") {
			return verifyCmdDeniedReason, false
		}
	case "cargo":
		if len(argv) < 2 || !stringIn(argv[1], "build", "test", "check", "clippy") {
			return verifyCmdDeniedReason, false
		}
	case "mvn":
		if len(argv) < 2 || !stringIn(argv[1], "test", "build", "compile") {
			return verifyCmdDeniedReason, false
		}
	case "gradle":
		if len(argv) < 2 || !stringIn(argv[1], "test", "build", "compile") {
			return verifyCmdDeniedReason, false
		}
	case "python", "python3", "py":
		if len(argv) < 3 || argv[1] != "-m" || argv[2] != "pytest" {
			return verifyCmdDeniedReason, false
		}
	case "make":
		if _, err := os.Stat(filepath.Join(workDir, "Makefile")); err != nil {
			return "make 仅在工作目录存在 Makefile 时允许", false
		}
	default:
		return verifyCmdDeniedReason, false
	}
	return "", true
}

// pkgScriptAllowed 放行包管理器的 test/lint/build，以及 `run test/lint/build`。
func pkgScriptAllowed(argv []string) bool {
	if len(argv) < 2 {
		return false
	}
	if stringIn(argv[1], "test", "lint", "build") {
		return true
	}
	return argv[1] == "run" && len(argv) >= 3 && stringIn(argv[2], "test", "lint", "build")
}

func stringIn(v string, all ...string) bool {
	for _, a := range all {
		if v == a {
			return true
		}
	}
	return false
}

// splitArgv 做引号感知的简单分词，只用于白名单前缀判断，不交给 shell 重组。
func splitArgv(s string) ([]string, bool) {
	var out []string
	var cur strings.Builder
	var single, double bool
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '\'' && !double:
			single = !single
		case r == '"' && !single:
			double = !double
		case unicode.IsSpace(r) && !single && !double:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	if single || double {
		return nil, false
	}
	return out, true
}

var (
	goFailPkgRe = regexp.MustCompile(`^(?:#|FAIL)\s+(\S+)`)
	tsErrorRe   = regexp.MustCompile(`error TS\d+`)
)

func (v *Verify) Execute(ctx context.Context, raw json.RawMessage) tool.Result {
	var in verifyInput
	if err := json.Unmarshal(raw, &in); err != nil {
		res := tool.Fail("cannot parse arguments: %v", err)
		res.Meta.BadArgs = true
		return res
	}
	if workdir.TooBroad(v.WorkDir) {
		return tool.Fail("%s", workdir.RejectReason)
	}

	timeoutSec := in.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = verifyDefaultTimeoutSec
	}
	if timeoutSec > verifyMaxTimeoutSec {
		timeoutSec = verifyMaxTimeoutSec
	}

	scope, steps, override, err := v.plan(in)
	if err != nil {
		res := tool.Fail("%v", err)
		res.Meta.BadArgs = true
		return res
	}

	if override != "" {
		// 覆盖命令的安全边界：单条命令、不允许换行链式拼接、前缀白名单、
		// 最后再过一遍毁盘黑名单。verify 零审批，绝不能变成第二个任意 shell。
		if strings.ContainsAny(override, "\n\r") {
			res := tool.Fail("cmd must be a single command; newlines are not allowed")
			res.Meta.BadArgs = true
			return res
		}
		if reason, ok := verifyCommandAllowed(v.WorkDir, override); !ok {
			res := tool.Fail("%s", reason)
			res.Meta.BadArgs = true
			return res
		}
		if blocked, reason := shellguard.Check(override); blocked {
			return tool.Result{
				Status:  tool.StatusDenied,
				Content: "operation blocked by policy: " + reason,
			}
		}
	}

	timeout := time.Duration(timeoutSec) * time.Second
	rep := verifyReport{Scope: scope}
	overall := "ok"
	overallCode := 0

	for _, step := range steps {
		vc := v.runStep(ctx, step, timeout)
		rep.Commands = append(rep.Commands, vc)
		rep.DurationMS += vc.DurationMS
		switch vc.Status {
		case "timeout":
			overall = "timeout"
		case "fail":
			if overall != "timeout" {
				overall = "fail"
			}
			overallCode = vc.ExitCode
		}
	}
	rep.Status = overall
	rep.ExitCode = overallCode

	res := tool.Result{Content: renderVerifyReport(rep)}
	switch overall {
	case "ok":
		res.Status = tool.StatusOK
	case "timeout":
		res.Status = tool.StatusTimeout
	default:
		res.Status = tool.StatusError
	}
	res.Meta.ExitCode = overallCode
	res.Meta.DurationMS = rep.DurationMS
	res.Meta.Path = strings.Join(verifyCommands(rep.Commands), " | ")
	// 结构化报告放进 Meta：本工具不产生统一 diff，复用 Diff 字段承载 JSON 报告。
	if b, err := json.Marshal(rep); err == nil {
		res.Meta.Diff = string(b)
	}
	return res
}

// runStep 跑单条检查，采集输出并解析失败摘要。
func (v *Verify) runStep(ctx context.Context, step verifyStep, timeout time.Duration) verifyCommand {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	switch {
	case len(step.Direct) > 0:
		// 固定的 go build 直调，不经 shell：少一层转义，少一个注入面。
		cmd = exec.CommandContext(execCtx, step.Direct[0], step.Direct[1:]...)
	default:
		// 自由命令（config.yaml 的 verify 值 / 模型覆盖）必须经过解释器拆分：
		// Windows 用 cmd /c，其它平台用 sh -c。
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(execCtx, "cmd", "/c", step.Command)
		} else {
			cmd = exec.CommandContext(execCtx, "sh", "-c", step.Command)
		}
	}
	cmd.Dir = v.WorkDir
	proc.Setup(cmd)

	var buf bytes.Buffer
	lw := &limitWriter{w: &buf, n: verifyMaxOutput}
	cmd.Stdout = lw
	cmd.Stderr = lw

	start := time.Now()
	runErr := cmd.Run()
	dur := int(time.Since(start).Milliseconds())
	out := buf.String()

	vc := verifyCommand{Kind: step.Kind, Command: step.Command, DurationMS: dur}

	// 先判 ctx：超时被杀时 runErr 是 signal: killed，顺序反了会把超时误报成退出码。
	if execCtx.Err() == context.DeadlineExceeded || execCtx.Err() == context.Canceled {
		vc.Status = "timeout"
		vc.ErrorLines = takeLines(strings.Split(out, "\n"), verifyMaxSummaryLines)
		vc.RawTail = tailOutput(out, verifyRawTailBytes)
		return vc
	}

	code := 0
	if runErr != nil {
		ee, ok := runErr.(*exec.ExitError)
		if !ok {
			vc.Status = "fail"
			vc.ErrorLines = []string{runErr.Error()}
			vc.RawTail = tailOutput(out, verifyRawTailBytes)
			return vc
		}
		code = ee.ExitCode()
	}
	vc.ExitCode = code
	if code == 0 {
		vc.Status = "ok"
		return vc
	}

	vc.Status = "fail"
	vc.FailedPkgs, vc.ErrorLines, vc.ErrorCount = parseFailure(step.Kind, out)
	vc.RawTail = tailOutput(out, verifyRawTailBytes)
	return vc
}

// parseFailure 从编译 / 类型检查输出里挑失败包名与关键错误行。
func parseFailure(kind, out string) (pkgs []string, lines []string, errCount int) {
	pkgSeen := map[string]bool{}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch kind {
		case "go":
			if m := goFailPkgRe.FindStringSubmatch(line); m != nil && !pkgSeen[m[1]] {
				pkgSeen[m[1]] = true
				pkgs = append(pkgs, m[1])
			}
			if strings.Contains(line, "error") || strings.Contains(line, "FAIL") {
				lines = append(lines, line)
			}
		default:
			if tsErrorRe.MatchString(line) {
				errCount++
				lines = append(lines, line)
			}
		}
	}
	lines = takeLines(lines, verifyMaxSummaryLines)
	return pkgs, lines, errCount
}

// renderVerifyReport 生成给模型看的人可读摘要。
func renderVerifyReport(rep verifyReport) string {
	var b strings.Builder
	verb := map[string]string{"ok": "通过", "fail": "失败", "timeout": "超时"}[rep.Status]
	fmt.Fprintf(&b, "verify: %s（scope: %s，%d 条命令，共 %dms）", verb, rep.Scope, len(rep.Commands), rep.DurationMS)

	for _, vc := range rep.Commands {
		fmt.Fprintf(&b, "\n[%s] %s — ", vc.Kind, vc.Command)
		switch vc.Status {
		case "ok":
			b.WriteString(fmt.Sprintf("exit 0，%dms", vc.DurationMS))
		case "timeout":
			b.WriteString(fmt.Sprintf("超时（%dms）", vc.DurationMS))
		default:
			detail := fmt.Sprintf("exit %d，%dms", vc.ExitCode, vc.DurationMS)
			if vc.ErrorCount > 0 {
				detail += fmt.Sprintf("，%d 个错误", vc.ErrorCount)
			}
			if len(vc.FailedPkgs) > 0 {
				detail += "，失败包：" + strings.Join(vc.FailedPkgs, ", ")
			}
			b.WriteString(detail)
		}
		if len(vc.ErrorLines) > 0 {
			b.WriteString("\n失败摘要：")
			for _, l := range vc.ErrorLines {
				b.WriteString("\n  " + l)
			}
		}
		if vc.Status != "ok" && vc.RawTail != "" {
			b.WriteString("\n原始输出尾部：\n  " + strings.ReplaceAll(vc.RawTail, "\n", "\n  "))
		}
	}

	if rep.Status == "ok" {
		b.WriteString("\n全部检查通过，可以声称任务完成。")
	} else {
		b.WriteString("\n检查未通过：修复上面的失败后重新调用 verify，通过前不要声称完成。")
	}
	return b.String()
}

func verifyCommands(cmds []verifyCommand) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, c.Command)
	}
	return out
}

func takeLines(lines []string, n int) []string {
	if len(lines) > n {
		lines = lines[:n]
	}
	return lines
}

// tailOutput 取输出末尾 n 字节；结论通常在末尾，和 Registry 截断口径一致。
func tailOutput(out string, n int) string {
	if len(out) <= n {
		return strings.TrimSpace(out)
	}
	return strings.TrimSpace(out[len(out)-n:])
}
