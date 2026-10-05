//go:build unix

package platform

import "testing"

func TestStartDetachedStartsANewSession(t *testing.T) {
	cmd := sleeper()
	StartDetached(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatalf("SysProcAttr = %+v, want Setsid", cmd.SysProcAttr)
	}
}
