// Package worktree 给每个会话准备一份独立的 git 工作树。
//
// 动机：多个 agent 改同一份代码会互相踩（README 的「没做」第一条）。
// 工作树不是复制一份仓库：它和主仓库共用 .git，只是换一个检出目录和分支，
// 所以磁盘开销小、提交历史也仍然连在一起。
//
// 安全边界：
//   - 只在真正的 git 仓库里工作，找不到仓库就明确报错，不猜。
//   - 工作树放在仓库旁边的 .mad-worktrees/<仓库名>/ 下，不写进仓库本身，
//     避免把用户的 git status 弄脏。
//   - 删除时只删登记过的路径；分支保留，未合并的提交不会凭空消失。
package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotRepo 表示目录不在 git 仓库里。
var ErrNotRepo = errors.New("这个文件夹不是 git 仓库，无法做 worktree 隔离")

// Info 是一份工作树的身份。Base 是创建时的提交，用来回答「从哪儿分出来的」。
type Info struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
	Base   string `json:"base,omitempty"`
}

// Root 返回 dir 所在仓库的根目录。
func Root(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", ErrNotRepo
	}
	root := strings.TrimSpace(out)
	if root == "" {
		return "", ErrNotRepo
	}
	return filepath.Clean(root), nil
}

// Add 为 name 建一份工作树，并开出 mad/<name> 分支。
//
// dir 可以是仓库根，也可以是仓库里的子目录；最终都归到根上。
func Add(ctx context.Context, dir, name string) (Info, error) {
	root, err := Root(ctx, dir)
	if err != nil {
		return Info{}, err
	}
	slug := slugify(name)
	if slug == "" {
		slug = "agent"
	}
	branch := "mad/" + slug
	slug = uniqueSlug(root, slug)
	if slug != strings.TrimPrefix(branch, "mad/") {
		branch = "mad/" + slug
	}

	path := filepath.Join(filepath.Dir(root), worktreeDirName, filepath.Base(root), slug)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Info{}, fmt.Errorf("创建 worktree 目录失败：%w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return Info{}, fmt.Errorf("worktree 目录已存在：%s", path)
	}

	base, _ := git(ctx, root, "rev-parse", "HEAD")
	base = strings.TrimSpace(base)
	if _, err := git(ctx, root, "worktree", "add", "-b", branch, path, "HEAD"); err != nil {
		return Info{}, fmt.Errorf("git worktree add 失败：%w", err)
	}
	return Info{Repo: root, Path: path, Branch: branch, Base: base}, nil
}

// Remove 删掉一份工作树。分支不删：里面可能还有没合并的提交。
// 路径不存在时只做 prune，让 git 的登记也清掉。
func Remove(ctx context.Context, info Info) error {
	if info.Repo == "" || info.Path == "" {
		return nil
	}
	if _, err := os.Stat(info.Path); os.IsNotExist(err) {
		_, _ = git(ctx, info.Repo, "worktree", "prune")
		return nil
	}
	if _, err := git(ctx, info.Repo, "worktree", "remove", "--force", info.Path); err != nil {
		return fmt.Errorf("git worktree remove 失败：%w", err)
	}
	return nil
}

// Managed 报告 path 是不是我们自己建出来的那份工作树（避免误删用户目录）。
func Managed(repo, path string) bool {
	if repo == "" || path == "" {
		return false
	}
	root := filepath.Join(filepath.Dir(repo), worktreeDirName, filepath.Base(repo))
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return strings.HasPrefix(filepath.Clean(abs), filepath.Clean(root)+string(filepath.Separator))
}

const worktreeDirName = ".mad-worktrees"

// slugify 把会话名压成能进分支名的形式。中文名会退化成 agent-<hash>，
// 保证同一台机器上不会因为编码不同生成非法 ref。
func slugify(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			if r == '.' && b.Len() == 0 {
				continue
			}
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-._")
	if len(out) > 32 {
		out = strings.Trim(out[:32], "-._")
	}
	return out
}

func uniqueSlug(root, slug string) string {
	base := slug
	for i := 2; i < 100; i++ {
		if !branchExists(root, "mad/"+slug) {
			return slug
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
	return fmt.Sprintf("%s-%d", base, time.Now().UnixNano())
}

func branchExists(root, branch string) bool {
	_, err := git(context.Background(), root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// git 跑一条 git 命令。统一带超时，避免 gc 或锁等待把调用方卡死。
func git(parent context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return string(out), nil
}
