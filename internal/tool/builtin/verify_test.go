package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"multi-agent-desktop/internal/tool"
)

func newVerifyRegistry(t *testing.T, dir string) *tool.Registry {
	t.Helper()
	r := tool.NewRegistry()
	RegisterRoots(r, dir, nil)
	return r
}

// repoRoot 从测试目录（internal/tool/builtin）回到仓库根，供需要真实模块的用例使用。
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

// (c) 默认 scope 路径不带 cmd：即使审批人全部拒绝，verify 也不进审批墙。
func TestVerifySkipsApproval(t *testing.T) {
	r := newVerifyRegistry(t, tmpDir(t))
	r.Approver = denyApprover{}

	res := r.Invoke(context.Background(), tool.Call{
		ID: "c1", Name: "verify", Input: json.RawMessage(`{}`),
	})
	if res.Status == tool.StatusDenied {
		t.Fatalf("verify default path must not require approval, got denied: %s", res.Content)
	}
}

// 通过：白名单内的 go 命令真实跑成功，状态 ok、退出码 0。
func TestVerifySuccess(t *testing.T) {
	v := &Verify{WorkDir: repoRoot(t)}

	in, _ := json.Marshal(verifyInput{Cmd: "go list ./internal/tool/..."})
	res := v.Execute(context.Background(), in)
	if res.Status != tool.StatusOK {
		t.Fatalf("status = %q, want ok: %s", res.Status, res.Content)
	}
	if res.Meta.ExitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", res.Meta.ExitCode)
	}
	if !strings.Contains(res.Content, "exit 0") {
		t.Fatalf("content = %q", res.Content)
	}

	rep := decodeVerifyReport(t, res)
	if rep.Status != "ok" || len(rep.Commands) != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Commands[0].Status != "ok" {
		t.Fatalf("command = %+v", rep.Commands[0])
	}
}

// (b) go test 在白名单内，放行并真实执行成功。
// 用临时通过模块，避免内层 go test 递归跑回本包测试。
func TestVerifyAllowsGoTest(t *testing.T) {
	dir := tempPassModule(t)
	v := &Verify{WorkDir: dir}

	in, _ := json.Marshal(verifyInput{Cmd: "go test ./..."})
	res := v.Execute(context.Background(), in)
	if res.Status != tool.StatusOK {
		t.Fatalf("status = %q, want ok: %s", res.Status, res.Content)
	}
	if res.Meta.ExitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", res.Meta.ExitCode)
	}
}

// 失败：非零退出要解析出失败摘要，状态是 error 而不是 ok。
func TestVerifyFailureParsesSummary(t *testing.T) {
	// 单元层：合成 Go / TS 输出，验证失败包名与错误条目解析。
	goPkgs, goLines, _ := parseFailure("go", "# m/internal/foo\n./main.go:3:5: error: undeclared: x\nFAIL\tm/internal/foo\n")
	if len(goPkgs) == 0 || !strings.Contains(strings.Join(goLines, "\n"), "undeclared") {
		t.Fatalf("go parse = pkgs=%v lines=%v", goPkgs, goLines)
	}
	_, tsLines, tsCount := parseFailure("ts", "src/a.ts(1,1): error TS2304: Cannot find name 'x'.\nsrc/b.ts(2,2): error TS2305: ...\n")
	if tsCount != 2 || len(tsLines) != 2 {
		t.Fatalf("ts parse = count=%d lines=%v", tsCount, tsLines)
	}

	// 执行层：空模块目录里 go test 必然失败，回结构化失败而不是 panic。
	dir := tmpDir(t)
	v := &Verify{WorkDir: dir}
	in, _ := json.Marshal(verifyInput{Cmd: "go test ./..."})
	res := v.Execute(context.Background(), in)
	if res.Status != tool.StatusError {
		t.Fatalf("status = %q, want error: %s", res.Status, res.Content)
	}
	if res.Meta.ExitCode != 1 {
		t.Fatalf("exitCode = %d, want 1", res.Meta.ExitCode)
	}
	rep := decodeVerifyReport(t, res)
	if rep.Status != "fail" {
		t.Fatalf("report status = %q", rep.Status)
	}
	if len(rep.Commands[0].ErrorLines) == 0 && rep.Commands[0].RawTail == "" {
		t.Fatalf("expected failure output, report = %+v", rep.Commands[0])
	}
}

// 超时：短 timeout 加长命令必须回 timeout 状态，不能混进普通失败。
// 白名单在 Execute 层；这里直接跑 runner，验证超时判定路径。
func TestVerifyTimeout(t *testing.T) {
	dir := tmpDir(t)
	v := &Verify{WorkDir: dir}

	var script string
	if runtime.GOOS == "windows" {
		// timeout 在带重定向的自动化终端里会直接报 Input redirection is not supported；
		// ping 的每次回包间隔提供可取消的真实等待，适合验证 runStep 的超时分支。
		script = "ping 127.0.0.1 -n 4 >nul"
	} else {
		script = "sleep 3"
	}
	step := verifyStep{Kind: "ts", Command: script}
	vc := v.runStep(context.Background(), step, time.Second)

	if vc.Status != "timeout" {
		t.Fatalf("status = %q, want timeout: %+v", vc.Status, vc)
	}
}

// 超长输出由 Registry 统一截断：verify 自己不另搞一套截断口径，
// 行为与其它工具一致（StatusTruncated + Meta.Truncated）。
func TestVerifyOutputTruncatedByRegistry(t *testing.T) {
	dir := tempFailModule(t)
	r := tool.NewRegistry()
	r.MaxResultBytes = 512
	RegisterRoots(r, dir, nil)

	in, _ := json.Marshal(verifyInput{Cmd: "go test ./..."})
	res := r.Invoke(context.Background(), tool.Call{
		ID: "c1", Name: "verify", Input: in,
	})

	if res.Status != tool.StatusTruncated {
		t.Fatalf("status = %q, want truncated: %s", res.Status, res.Content)
	}
	if !res.Meta.Truncated || res.Meta.BytesRaw <= 512 {
		t.Fatalf("meta = %+v", res.Meta)
	}
	if res.Meta.BytesSent >= res.Meta.BytesRaw {
		t.Fatalf("sent %d should be < raw %d", res.Meta.BytesSent, res.Meta.BytesRaw)
	}
}

// (a) 非构建/测试类命令被白名单拒绝，且根本不执行。
func TestVerifyRejectsUnwhitelistedCommand(t *testing.T) {
	dir := tmpDir(t)
	marker := filepath.Join(dir, "marker-dir")
	if err := os.MkdirAll(marker, 0o755); err != nil {
		t.Fatal(err)
	}
	v := &Verify{WorkDir: dir}

	in, _ := json.Marshal(verifyInput{Cmd: "rm -rf " + marker})
	res := v.Execute(context.Background(), in)

	if res.Status != tool.StatusError || !res.Meta.BadArgs {
		t.Fatalf("status = %q meta = %+v, want bad-args failure", res.Status, res.Meta)
	}
	if !strings.Contains(res.Content, "bash") {
		t.Fatalf("reject reason should point to bash: %s", res.Content)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("rejected command must not have executed; marker dir is gone")
	}
}

// scope 推断：按 go.mod / package.json 确定性落档。
func TestVerifyInferScope(t *testing.T) {
	cases := []struct {
		name    string
		goMod   bool
		pkgJSON bool
		want    string
	}{
		{"neither falls back to go", false, false, "go"},
		{"go only", true, false, "go"},
		{"ts only", false, true, "ts"},
		{"both", true, true, "both"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := tmpDir(t)
			if tc.goMod {
				os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n"), 0o644)
			}
			if tc.pkgJSON {
				os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}\n"), 0o644)
			}
			if got := inferScope(dir); got != tc.want {
				t.Fatalf("inferScope = %q, want %q", got, tc.want)
			}
		})
	}
}

// 默认命令选择：Go 是 build+vet 两条有序直调；TS 读 config.yaml；both 依序三者。
func TestVerifyDefaultCommands(t *testing.T) {
	t.Run("go default builds then vets", func(t *testing.T) {
		dir := tmpDir(t)
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n"), 0o644)
		v := &Verify{WorkDir: dir}

		scope, steps, _, err := v.plan(verifyInput{Scope: "go"})
		if err != nil {
			t.Fatal(err)
		}
		if scope != "go" || len(steps) != 2 {
			t.Fatalf("scope=%s steps=%+v", scope, steps)
		}
		if steps[0].Command != "go build ./..." || steps[1].Command != "go vet ./..." {
			t.Fatalf("go default order = %q, %q", steps[0].Command, steps[1].Command)
		}
		for _, s := range steps {
			if len(s.Direct) == 0 {
				t.Fatal("go default steps should run direct without shell")
			}
		}
	})

	t.Run("ts default reads config.yaml", func(t *testing.T) {
		dir := tmpDir(t)
		os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("verify: \"npx tsc --noEmit\"\n"), 0o644)
		v := &Verify{WorkDir: dir}

		_, steps, _, err := v.plan(verifyInput{Scope: "ts"})
		if err != nil {
			t.Fatal(err)
		}
		if steps[0].Command != "npx tsc --noEmit" {
			t.Fatalf("ts default command = %q", steps[0].Command)
		}
	})

	t.Run("ts fallback when config missing", func(t *testing.T) {
		dir := tmpDir(t)
		if got := tsVerifyCommand(dir); got != "npx tsc --noEmit" {
			t.Fatalf("fallback = %q", got)
		}
	})

	t.Run("both runs build vet then ts", func(t *testing.T) {
		dir := tmpDir(t)
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("verify: \"npx tsc --noEmit\"\n"), 0o644)
		v := &Verify{WorkDir: dir}

		_, steps, _, err := v.plan(verifyInput{Scope: "both"})
		if err != nil {
			t.Fatal(err)
		}
		if len(steps) != 3 {
			t.Fatalf("steps = %+v", steps)
		}
		if steps[0].Kind != "go" || steps[1].Kind != "go" || steps[2].Kind != "ts" {
			t.Fatalf("steps = %+v", steps)
		}
	})
}

// 覆盖命令拒绝换行（单条命令边界）。
func TestVerifyRejectsMultilineOverride(t *testing.T) {
	dir := tmpDir(t)
	v := &Verify{WorkDir: dir}
	in, _ := json.Marshal(verifyInput{Cmd: "go build ./...\nrm -rf x"})
	res := v.Execute(context.Background(), in)
	if !res.Meta.BadArgs {
		t.Fatalf("status = %q meta = %+v", res.Status, res.Meta)
	}
}

// 工具对外声明的超时上限必须是 600s，Registry 不会用默认 120s 误杀编译。
func TestVerifyTimeoutCeiling(t *testing.T) {
	v := &Verify{WorkDir: tmpDir(t)}
	if got := v.Timeout(); got != verifyMaxTimeoutSec*time.Second {
		t.Fatalf("timeout = %v, want %v", got, verifyMaxTimeoutSec*time.Second)
	}
}

// tempFailModule 造一个必然失败、且输出超长的最小 Go 模块，供截断用例使用。
func tempFailModule(t *testing.T) string {
	t.Helper()
	dir := tmpDir(t)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.21\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644)
	testSrc := "package main\n\nimport (\n\t\"strings\"\n\t\"testing\"\n)\n\nfunc TestHuge(t *testing.T) {\n\tt.Fatal(strings.Repeat(\"x\", 4000))\n}\n"
	os.WriteFile(filepath.Join(dir, "main_test.go"), []byte(testSrc), 0o644)
	return dir
}

// tempPassModule 造一个测试全绿的最小 Go 模块，供 go test 放行用例使用。
func tempPassModule(t *testing.T) string {
	t.Helper()
	dir := tmpDir(t)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.21\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644)
	testSrc := "package main\n\nimport \"testing\"\n\nfunc TestOk(t *testing.T) {}\n"
	os.WriteFile(filepath.Join(dir, "main_test.go"), []byte(testSrc), 0o644)
	return dir
}

func decodeVerifyReport(t *testing.T, res tool.Result) verifyReport {
	t.Helper()
	var rep verifyReport
	if err := json.Unmarshal([]byte(res.Meta.Diff), &rep); err != nil {
		t.Fatalf("structured report in Meta invalid: %v; meta.Diff=%q", err, res.Meta.Diff)
	}
	return rep
}
