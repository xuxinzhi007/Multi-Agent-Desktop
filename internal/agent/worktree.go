package agent

import (
	"context"
	"fmt"
	"time"

	"multi-agent-desktop/internal/worktree"
)

// EnableWorktree 给一个会话开独立 git 工作树。
//
// 原工作目录必须是 git 仓库（或其子目录）。创建后 WorkDir 指向工作树，
// 这个会话后续的文件操作和 shell 都只落在它自己的分支上，
// 不会再和其他会话抢同一份检出。重复调用是幂等的。
func (m *Manager) EnableWorktree(ctx context.Context, id string) error {
	a, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("agent %s not found", id)
	}
	sn := a.Snapshot()
	if sn.Worktree != "" {
		return nil
	}
	dir := sn.WorkDir
	if dir == "" {
		return fmt.Errorf("这个会话还没有文件夹，先选一个 git 仓库")
	}
	info, err := worktree.Add(ctx, dir, sn.Name)
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.Worktree = info.Path
	a.WorktreeBranch = info.Branch
	a.WorktreeRepo = info.Repo
	a.WorktreeFrom = dir
	a.WorkDir = info.Path
	a.mu.Unlock()

	m.emit("agent.config", a.viewMap())
	m.goSave()
	return nil
}

// DisableWorktree 收掉工作树，把会话还原到原来的目录。
// 分支保留：里面可能还有没合并的提交。正在跑的一轮不会被主动打断，
// 下一轮开始前工具就会回到原目录。
//
// 解绑和物理删除分开：删目录失败（被占用、权限、仓库已移走）时，
// 会话仍须能回到原目录，否则用户被永久卡在隔离状态里关不掉。
// 删不掉的路径作为提示交回去，不吞掉。
func (m *Manager) DisableWorktree(ctx context.Context, id string) error {
	a, ok := m.Get(id)
	if !ok {
		return fmt.Errorf("agent %s not found", id)
	}
	sn := a.Snapshot()
	if sn.Worktree == "" {
		return nil
	}
	info := worktree.Info{Repo: sn.WorktreeRepo, Path: sn.Worktree, Branch: sn.WorktreeBranch}
	removeErr := worktree.Remove(ctx, info)

	a.mu.Lock()
	from := a.WorktreeFrom
	if from == "" {
		from = a.WorktreeRepo
	}
	a.WorkDir = from
	a.Worktree = ""
	a.WorktreeBranch = ""
	a.WorktreeRepo = ""
	a.WorktreeFrom = ""
	a.mu.Unlock()

	m.emit("agent.config", a.viewMap())
	m.goSave()
	if removeErr != nil {
		return fmt.Errorf("已回到原目录，但工作树没能自动删除：%w（路径 %s，可手动删）", removeErr, info.Path)
	}
	return nil
}

// WorktreeInfo 返回会话当前的工作树信息。没有隔离时 ok 为 false。
func (m *Manager) WorktreeInfo(id string) (worktree.Info, bool) {
	a, ok := m.Get(id)
	if !ok {
		return worktree.Info{}, false
	}
	sn := a.Snapshot()
	if sn.Worktree == "" {
		return worktree.Info{}, false
	}
	return worktree.Info{Repo: sn.WorktreeRepo, Path: sn.Worktree, Branch: sn.WorktreeBranch}, true
}

func (m *Manager) disableWorktreeQuiet(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = m.DisableWorktree(ctx, id)
}
