//go:build unix

package platform

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// ProcessName returns the executable name of pid; an error when pid does not run.
func ProcessName(pid int) (string, error) {
	if !ProcessAlive(pid) {
		return "", fmt.Errorf("process %d does not run", pid)
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output() // #nosec G204 -- a numeric pid.
	if err != nil {
		return "", fmt.Errorf("process %d does not run: %w", pid, err)
	}
	return filepath.Base(strings.TrimSpace(string(out))), nil
}

// TerminatePID asks pid's process group to stop, as TerminateGroup does for a started command.
func TerminatePID(pid int) error {
	if pid <= 1 { // kill(-1) would signal every process the user can signal.
		return fmt.Errorf("invalid pid %d", pid)
	}
	return syscall.Kill(-pid, syscall.SIGTERM)
}
