// Package proc 提供跨平台子进程管理。
//
// 存在的理由：exec.CommandContext 默认只 kill 直接子进程。
// CLI agent（claude / codex）会派生出自己的子进程（node、git、ripgrep…），
// 这些孙进程会继续持有 stdout —— 于是取消 ctx 之后事件流永远不关闭，
// 上层 worker 卡在 range 上，「停止」按钮看起来完全没反应。
//
// 解法是把子进程放进独立进程组，取消时杀整组。
package proc

import "os/exec"

// Setup 把 cmd 配置成独立进程组，并注入取消时杀整组的逻辑。
// 必须在 cmd.Start 之前调用。
func Setup(cmd *exec.Cmd) {
	setProcAttr(cmd)
	cmd.Cancel = func() error {
		return killGroup(cmd)
	}
}

// Exited 判断错误是否为进程以非零码退出（而非启动失败）。
func Exited(err error) bool {
	var ee *exec.ExitError
	return asExitError(err, &ee)
}
