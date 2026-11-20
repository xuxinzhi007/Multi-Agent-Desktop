// Package proc 提供跨平台子进程管理。
//
// 存在的理由：exec.CommandContext 默认只 kill 直接子进程。
// CLI agent（claude / codex）会派生出自己的子进程（node、git、ripgrep…），
// 这些孙进程会继续持有 stdout —— 于是取消 ctx 之后事件流永远不关闭，
// 上层 worker 卡在 range 上，「停止」按钮看起来完全没反应。
//
// 解法是把子进程放进独立进程组，取消时杀整组。
package proc

import (
	"context"
	"os"
	"os/exec"
	"runtime"
)

// Shell 选一个不读登录脚本的 shell。
//
// zsh -lc 会加载 .zprofile / .zshrc。这些脚本经常去碰桌面、文稿、下载，
// macOS 就按文件夹各弹一次权限，而且每条命令都会再走一遍。
// -df 关掉全局和用户的启动文件。Windows 用 PowerShell -NoProfile，同一件事。
// Windows PowerShell 5.1 往管道写 UTF-16，调用方应走 CommandContext，它会先切到 UTF-8。
func Shell() (string, []string) {
	if runtime.GOOS == "windows" {
		return "powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command"}
	}
	if _, err := os.Stat("/bin/zsh"); err == nil {
		return "/bin/zsh", []string{"-dfc"}
	}
	return "/bin/sh", []string{"-c"}
}

// utf8Prefix 让 Windows PowerShell 5.1 的管道输出变成 UTF-8，而不是 UTF-16。
const utf8Prefix = "$OutputEncoding = [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false; "

// CommandContext 用 Shell 执行一条脚本。Windows 上会先把输出编码改成 UTF-8。
func CommandContext(ctx context.Context, script string) *exec.Cmd {
	if ctx == nil {
		ctx = context.Background()
	}
	name, args := Shell()
	if runtime.GOOS == "windows" {
		script = utf8Prefix + script
	}
	return exec.CommandContext(ctx, name, append(args, script)...)
}

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
