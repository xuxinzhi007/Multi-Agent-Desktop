//go:build windows

package proc

import (
	"errors"
	"os/exec"
	"strconv"
	"syscall"
)

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func asExitError(err error, target **exec.ExitError) bool {
	return errors.As(err, target)
}

// killGroup 在 Windows 上结束整个进程树。
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return exec.Command("taskkill", "/T", "/F", "/PID", itoa(cmd.Process.Pid)).Run()
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
