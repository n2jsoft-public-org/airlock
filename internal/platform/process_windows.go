package platform

import (
	"errors"
	"os/exec"
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

// StartInOwnGroup makes cmd start in a process group of its own, so the console's Ctrl+C reaches
// airlock only and TerminateGroup decides when cmd stops.
func StartInOwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// TerminateGroup stops the started cmd and the processes it spawned (session-manager-plugin for
// aws). Windows has no termination signal for console processes, so the tree is ended forcibly.
func TerminateGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)) // #nosec G204 -- the only argument is a pid.
	if err := kill.Run(); err != nil {
		_ = cmd.Process.Kill() //nolint:errcheck // the process may already have ended.
	}
}
