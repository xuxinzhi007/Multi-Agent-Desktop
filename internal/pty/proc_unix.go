//go:build !windows

package pty

import (
	"errors"
	"os/exec"
	"syscall"
)

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func asExitError(err error, target **exec.ExitError) bool {
	return errors.As(err, target)
}
