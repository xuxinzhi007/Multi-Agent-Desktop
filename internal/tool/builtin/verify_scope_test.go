package builtin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 相关测试选择：同一个包里的多个文件只算一次，非 Go 文件忽略。
func TestAffectedGoPackagesDedupesAndFilters(t *testing.T) {
	pkgs := affectedGoPackages([]string{
		"internal/gateway/a.go",
		"internal/gateway/b_test.go",
		"src/lib/x.ts",
		"vendor/github.com/x/y.go",
		"cmd/main.go",
	})
	want := []string{"./internal/gateway", "./cmd"}
	if len(pkgs) != len(want) {
		t.Fatalf("pkgs = %v, want %v", pkgs, want)
	}
	for i := range want {
		if pkgs[i] != want[i] {
			t.Fatalf("pkgs = %v, want %v", pkgs, want)
		}
	}
}

func TestAffectedGoPackagesCapsAtLimit(t *testing.T) {
	var changed []string
	for i := 0; i < 20; i++ {
		changed = append(changed, filepath.ToSlash(filepath.Join("pkg", string(rune('a'+i)), "x.go")))
	}
	if got := affectedGoPackages(changed); len(got) != verifyMaxTestPkgs {
		t.Fatalf("应截断到 %d 个包，got %d", verifyMaxTestPkgs, len(got))
	}
}

func TestChangedTestFiles(t *testing.T) {
	got := changedTestFiles([]string{
		"internal/gateway/broker_test.go",
		"src/lib/useBackend.test.ts",
		"e2e/remote.spec.tsx",
		"internal/gateway/broker.go",
	})
	if len(got) != 3 {
		t.Fatalf("测试文件识别错：%v", got)
	}
}

// lint 只在仓库真配了脚本时才跑：配了就跑，没配就跳过。
func TestTSLintStepOnlyWhenScriptExists(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"dev":"vite"}}`), 0o644)
	if _, ok := tsLintStep(dir); ok {
		t.Fatal("没有 lint 脚本时不应生成步骤")
	}
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"lint":"eslint ."}}`), 0o644)
	step, ok := tsLintStep(dir)
	if !ok || step.Command != "npm run lint" {
		t.Fatalf("step = %+v ok=%v", step, ok)
	}
}

// 在真仓库里改一个 .go 文件后，应生成只测该包的命令。
func TestGoTestStepFromGitStatus(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "t@example.com")
	runGit(t, dir, "config", "user.name", "t")
	os.MkdirAll(filepath.Join(dir, "internal", "gateway"), 0o755)
	os.WriteFile(filepath.Join(dir, "internal", "gateway", "a.go"), []byte("package gateway\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.21\n"), 0o644)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-qm", "init")

	// 未提交改动
	os.WriteFile(filepath.Join(dir, "internal", "gateway", "a.go"), []byte("package gateway\n\nvar X = 1\n"), 0o644)

	step, ok := goTestStep(dir)
	if !ok {
		t.Fatal("有 Go 改动时应生成 test 步骤")
	}
	if !strings.Contains(step.Command, "./internal/gateway") {
		t.Fatalf("command = %q", step.Command)
	}
	if len(step.Direct) < 3 || step.Direct[1] != "test" {
		t.Fatalf("direct = %v", step.Direct)
	}
	// 没有 git 仓库时必须静默跳过，不能让 verify 失败。
	if _, ok := goTestStep(t.TempDir()); ok {
		t.Fatal("非 git 目录不应生成 test 步骤")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
