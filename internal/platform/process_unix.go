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
	"time"
)

// groupKillDelay is how long a terminated process group has to stop before it is killed: a stuck
// session-manager-plugin would keep the tunnel's port.
var groupKillDelay = 5 * time.Second

// ProcessAlive reports whether pid runs, even as another user.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// GroupAlive reports whether a process of the group pid leads runs: a group outlives its leader
// (aws) while a process it spawned (session-manager-plugin) runs, and its id is not reused meanwhile.
func GroupAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(-pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// StartInOwnGroup makes cmd start in a process group of its own, so TerminateGroup also reaches
// the processes it spawns (session-manager-plugin for aws).
func StartInOwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// TerminateGroup asks the started cmd and the processes it spawned to stop, and kills the ones
// still running after groupKillDelay. It returns once the group is gone or killed: cmd must be
// waited for meanwhile, or its ended leader keeps the group alive until killed.
func TerminateGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = stopGroup(cmd.Process.Pid) //nolint:errcheck // the group may already have ended.
}

// stopGroup sends SIGTERM to the group pid leads, then SIGKILL if a process of it still runs after
// groupKillDelay; the error is SIGTERM's.
func stopGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGTERM)
	if err != nil {
		return err
	}
	for deadline := time.Now().Add(groupKillDelay); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if !GroupAlive(pid) {
			return nil
		}
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL) //nolint:errcheck // the group may have ended meanwhile.
	return nil
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

// TerminatePID stops pid's process group as TerminateGroup does for a started command.
func TerminatePID(pid int) error {
	if pid <= 1 { // kill(-1) would signal every process the user can signal.
		return fmt.Errorf("invalid pid %d", pid)
	}
	return stopGroup(pid)
}
