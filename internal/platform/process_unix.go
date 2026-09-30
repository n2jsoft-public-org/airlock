//go:build unix

package platform

import (
	"errors"
	"os/exec"
	"syscall"
)

// ProcessAlive reports whether pid runs, even as another user.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// StartInOwnGroup makes cmd start in a process group of its own, so TerminateGroup also reaches
// the processes it spawns (session-manager-plugin for aws).
func StartInOwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// TerminateGroup asks the started cmd and the processes it spawned to stop.
func TerminateGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) //nolint:errcheck // the group may already have ended.
}
