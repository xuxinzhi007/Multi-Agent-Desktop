//go:build windows

package pty

import (
	"errors"
	"os/exec"
	"syscall"
)

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func asExitError(err error, target **exec.ExitError) bool {
	return errors.As(err, target)
}
