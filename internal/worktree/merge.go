package worktree

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// MergeResult 描述一次"把工作树合回主干"的结局。
//
// 设计原则：宁可失败得干净，不许留下半个合并。
// 冲突时一律 merge --abort 回到合并前，把冲突文件清单交回去——
// 让人或模型去解决语义，工具不替它猜。
type MergeResult struct {
	Merged    bool     `json:"merged"`
	Repo      string   `json:"repo"`
	Branch    string   `json:"branch"`
	Into      string   `json:"into"`
	FastFwd   bool     `json:"fastForward"`
	Conflicts []string `json:"conflicts,omitempty"`
	Output    string   `json:"output,omitempty"`
}

// Merge 把 wtPath 所在工作树的分支合进主仓当前分支。
//
// 前置条件（任一不满足就明确报错，不自动 stash / 不自动 commit）：
//   - 工作树里没有未提交改动（否则合并的是半个改动）；
//   - 主仓里没有未提交改动（否则 merge 会被拒绝，报错还难懂）。
func Merge(ctx context.Context, wtPath string) (MergeResult, error) {
	repo, err := mainRepoOf(ctx, wtPath)
	if err != nil {
		return MergeResult{}, err
	}
	branch, err := git(ctx, wtPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return MergeResult{}, fmt.Errorf("读工作树分支失败：%w", err)
	}
	branch = strings.TrimSpace(branch)

	wtStatus, err := git(ctx, wtPath, "status", "--porcelain")
	if err != nil {
		return MergeResult{}, fmt.Errorf("读工作树状态失败：%w", err)
	}
	if strings.TrimSpace(wtStatus) != "" {
		return MergeResult{}, fmt.Errorf("工作树里还有未提交的改动；先 commit 再合回（工具不会替你提交）")
	}

	ref := MergeResult{Repo: repo, Branch: branch}

	into, err := git(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return MergeResult{}, fmt.Errorf("读主仓分支失败：%w", err)
	}
	ref.Into = strings.TrimSpace(into)
	if ref.Into == branch {
		return MergeResult{}, fmt.Errorf("主仓当前就在 %s 分支上，不需要合并", branch)
	}

	mainStatus, err := git(ctx, repo, "status", "--porcelain")
	if err != nil {
		return MergeResult{}, fmt.Errorf("读主仓状态失败：%w", err)
	}
	if strings.TrimSpace(mainStatus) != "" {
		return MergeResult{}, fmt.Errorf("主仓里有未提交的改动，先处理（commit 或 stash）再合回")
	}

	// 先试 ff：线性历史最容易读懂，也最不容易出错。
	if out, ffErr := git(ctx, repo, "merge", "--ff-only", branch); ffErr == nil {
		ref.Merged = true
		ref.FastFwd = true
		ref.Output = strings.TrimSpace(out)
		return ref, nil
	}

	out, mergeErr := git(ctx, repo, "merge", "--no-ff", "-m", "merge "+branch, branch)
	if mergeErr == nil {
		ref.Merged = true
		ref.Output = strings.TrimSpace(out)
		return ref, nil
	}

	// 失败后必须区分"冲突"和"其它错误"：冲突要 abort + 交清单，
	// 其它错误（权限、锁）abort 也可能失败，原样把输出交回去。
	conflicted, _ := git(ctx, repo, "diff", "--name-only", "--diff-filter=U")
	ref.Conflicts = splitLines(conflicted)
	_, _ = git(ctx, repo, "merge", "--abort")
	if len(ref.Conflicts) > 0 {
		ref.Output = strings.TrimSpace(out)
		return ref, fmt.Errorf("合并冲突，已中止合并（主仓回到合并前状态）；冲突文件：%s",
			strings.Join(ref.Conflicts, ", "))
	}
	return ref, fmt.Errorf("合并失败：%s", strings.TrimSpace(out))
}

// mainRepoOf 从工作树路径反推主仓根。
//
// 在工作树里跑 rev-parse --show-toplevel 得到的是工作树自己，
// 必须用 --git-common-dir 拿到与主仓共享的 .git，再取它的父目录。
func mainRepoOf(ctx context.Context, wtPath string) (string, error) {
	out, err := git(ctx, wtPath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("%s 不是 git 工作树：%w", wtPath, err)
	}
	common := strings.TrimSpace(out)
	if common == "" {
		return "", fmt.Errorf("%s 不是 git 工作树", wtPath)
	}
	// --git-common-dir 在普通仓库里就是 <root>/.git
	if filepath.Base(common) != ".git" {
		return "", fmt.Errorf("%s 指向的公共目录不是 .git（%s）", wtPath, common)
	}
	return filepath.Dir(common), nil
}

func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			out = append(out, t)
		}
	}
	return out
}
