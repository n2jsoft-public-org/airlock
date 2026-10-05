//go:build unix

package platform

import (
	"os/exec"
	"os/signal"
	"syscall"
)

// StartDetached makes cmd run on its own: a new session, without the terminal's streams, so
// closing the terminal neither stops it nor breaks its output.
func StartDetached(cmd *exec.Cmd) {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// IgnoreHangup keeps the process running when the terminal it was started from closes.
func IgnoreHangup() {
	signal.Ignore(syscall.SIGHUP)
}
