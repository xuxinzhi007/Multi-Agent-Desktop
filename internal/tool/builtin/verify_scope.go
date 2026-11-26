package builtin

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// verify 的"相关测试"扩维。
//
// 原来的默认是 build + vet：类型对了就算过。逻辑错在测试里才看得出来，
// 而全量 `go test ./...` 在真实仓库上要几十秒到几分钟，没人愿意每次改一行都等。
//
// 折中：只看**本次改动**涉及的包。用 git 拿改动列表（工作区 + 暂存 + 未跟踪），
// 把改了 .go 的目录转成 `./internal/gateway` 这种包路径，一条 `go test` 跑完。
// 没有 git、没有改动、或目录不是仓库时静默跳过 —— 不能因为拿不到改动列表就让 verify 失败。

const (
	verifyMaxTestPkgs = 8
	// 改动文件太多时只取前若干个包：宁可漏测，也不让 verify 变成"又一个长任务"。
	verifyMaxChangedFiles = 200
)

// gitChangedFiles 返回相对 workDir 的改动文件（含未跟踪），按路径排序。
func gitChangedFiles(workDir string) []string {
	// 用 --show-prefix 而不是 --show-toplevel 拼路径：
	// macOS 上 /var 是 /private/var 的符号链接，绝对路径拼出来的结果会和 workDir
	// 对不上（Rel 得到 ../../private/...），改动列表会被全部误判为"在目录之外"。
	prefix, err := gitOut(workDir, "rev-parse", "--show-prefix")
	if err != nil {
		return nil
	}
	prefix = strings.TrimSpace(prefix)
	out, err := gitOut(workDir, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		p := strings.TrimSpace(line[3:])
		if p == "" {
			continue
		}
		// 重命名是 "old -> new"，只关心新路径。
		if i := strings.Index(p, " -> "); i >= 0 {
			p = strings.TrimSpace(p[i+4:])
		}
		p = strings.Trim(p, `"`)
		p = filepath.ToSlash(p)
		if prefix != "" {
			if !strings.HasPrefix(p, prefix) {
				continue // 改动在工作目录之外
			}
			p = strings.TrimPrefix(p, prefix)
		}
		files = append(files, p)
		if len(files) >= verifyMaxChangedFiles {
			break
		}
	}
	sort.Strings(files)
	return files
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.Output()
	return string(b), err
}

// affectedGoPackages 把改动的 .go 文件折成包路径（`./internal/agent`）。
// 同一目录只算一次；超过上限就截断，并在命令里说明"只测了前 N 个"。
func affectedGoPackages(changed []string) []string {
	seen := map[string]bool{}
	var pkgs []string
	for _, f := range changed {
		if !strings.HasSuffix(f, ".go") {
			continue
		}
		if strings.HasPrefix(f, "vendor/") || strings.Contains(f, "/vendor/") {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(f))
		if seen[dir] {
			continue
		}
		seen[dir] = true
		if dir == "." {
			pkgs = append(pkgs, "./")
		} else {
			pkgs = append(pkgs, "./"+dir)
		}
		if len(pkgs) >= verifyMaxTestPkgs {
			break
		}
	}
	return pkgs
}

// goTestStep 在有 Go 改动时给出一条 `go test <改动包>`。
func goTestStep(workDir string) (verifyStep, bool) {
	pkgs := affectedGoPackages(gitChangedFiles(workDir))
	if len(pkgs) == 0 {
		return verifyStep{}, false
	}
	args := append([]string{"go", "test"}, pkgs...)
	return verifyStep{
		Kind:    "go",
		Command: "go test " + strings.Join(pkgs, " "),
		Direct:  args,
	}, true
}

// tsLintStep 只在仓库里真的配了 lint 脚本时才跑。
// 没配就别假装验过 —— 跑一条不存在的脚本只会给模型一条误导性的红。
func tsLintStep(workDir string) (verifyStep, bool) {
	data, err := os.ReadFile(filepath.Join(workDir, "package.json"))
	if err != nil {
		return verifyStep{}, false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return verifyStep{}, false
	}
	if strings.TrimSpace(pkg.Scripts["lint"]) == "" {
		return verifyStep{}, false
	}
	return verifyStep{Kind: "ts", Command: "npm run lint", Direct: []string{"npm", "run", "lint"}}, true
}

// changedTestFiles 是本次改动里的测试文件。
//
// 用途不是"禁止改测试"，而是让"测试从红变绿"这件事**可见**：
// 模型放宽断言后 verify 会通过，人必须能看到是哪个测试文件被动了。
func changedTestFiles(changed []string) []string {
	var tests []string
	for _, f := range changed {
		base := filepath.Base(f)
		if strings.HasSuffix(base, "_test.go") ||
			strings.HasSuffix(base, ".test.ts") || strings.HasSuffix(base, ".test.tsx") ||
			strings.HasSuffix(base, ".spec.ts") || strings.HasSuffix(base, ".spec.tsx") {
			tests = append(tests, f)
		}
	}
	return tests
}
