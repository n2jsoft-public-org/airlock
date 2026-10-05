package platform

import (
	"testing"

	"golang.org/x/sys/windows"
)

// A DETACHED_PROCESS daemon would give every console child (aws, session-manager-plugin) a
// visible console window, whose closing kills the tunnel.
func TestStartDetachedHidesTheConsoleOfTheChildren(t *testing.T) {
	cmd := sleeper()
	StartDetached(cmd)
	want := uint32(windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags != want {
		t.Fatalf("SysProcAttr = %+v, want CreationFlags %#x", cmd.SysProcAttr, want)
	}
}
