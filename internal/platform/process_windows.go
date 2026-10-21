package platform

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code Windows reports for a process that has not ended (STILL_ACTIVE).
const stillActive = 259

// ProcessAlive reports whether pid runs, even as another user.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) // #nosec G115 -- pid is positive.
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // a query handle holds nothing to flush.

	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return true
	}
	return code == stillActive
}

// GroupAlive is false on Windows: a process tree is reached only through its live root.
func GroupAlive(int) bool { return false }

// StartInOwnGroup makes cmd start in a process group of its own, so the console's Ctrl+C reaches
// airlock only and TerminateGroup decides when cmd stops.
func StartInOwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// TerminateGroup stops the started cmd and the processes it spawned (session-manager-plugin for
// aws). Windows has no termination signal for console processes, so the tree is ended forcibly:
// there is nothing to escalate to, unlike Unix. taskkill /T finds the tree through parent PIDs, so
// a session-manager-plugin whose aws already exited is out of reach (a job object would hold it).
func TerminateGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)) // #nosec G204 -- the only argument is a pid.
	if err := kill.Run(); err != nil {
		_ = cmd.Process.Kill() //nolint:errcheck // the process may already have ended.
	}
}

// ProcessName returns the executable name of pid; an error when pid does not run.
func ProcessName(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid pid %d", pid)
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) // #nosec G115 -- pid is positive.
	if err != nil {
		return "", fmt.Errorf("process %d does not run: %w", pid, err)
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // a query handle holds nothing to flush.
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil || code != stillActive {
		return "", fmt.Errorf("process %d does not run", pid)
	}
	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf)) // #nosec G115 -- MAX_PATH is small.
	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil {
		return "", err
	}
	return filepath.Base(windows.UTF16ToString(buf[:size])), nil
}

// TerminatePID ends pid and the processes it spawned, forcibly as TerminateGroup does.
func TerminatePID(pid int) error {
	if pid <= 1 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run() // #nosec G204 -- a numeric pid.
}
