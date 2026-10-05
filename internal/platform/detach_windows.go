package platform

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// StartDetached makes cmd run on its own: a hidden console, no terminal streams, its own process
// group. Its console children (aws, session-manager-plugin, taskkill) inherit the hidden console:
// with DETACHED_PROCESS each would open a visible window, whose closing would kill the tunnel.
func StartDetached(cmd *exec.Cmd) {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP}
}

// IgnoreHangup does nothing: Windows sends no hangup to detached processes.
func IgnoreHangup() {}
