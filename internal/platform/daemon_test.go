package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTryLockFileFailsWhileHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	first, second := openLockFile(t, path), openLockFile(t, path)
	if err := TryLockFile(first); err != nil {
		t.Fatal(err)
	}
	if err := TryLockFile(second); !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
	if err := UnlockFile(first); err != nil {
		t.Fatal(err)
	}
	if err := TryLockFile(second); err != nil {
		t.Fatalf("after unlock: %v", err)
	}
}

func TestProcessNameAndTerminatePID(t *testing.T) {
	cmd := sleeper()
	StartInOwnGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() }) //nolint:errcheck // best effort: the process may already have ended.
	name, err := ProcessName(cmd.Process.Pid)
	if err != nil || !strings.Contains(strings.ToLower(name), sleeperName()) {
		t.Fatalf("name = %q, err = %v", name, err)
	}
	if err := TerminatePID(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done: //nolint:errcheck // a terminated process exits with an error.
	case <-time.After(5 * time.Second):
		t.Fatal("process still running")
	}
	if _, err := ProcessName(cmd.Process.Pid); err == nil {
		t.Fatal("an ended process has no name")
	}
}

func TestStartDetachedLeavesNoTerminal(t *testing.T) {
	cmd := sleeper()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	StartDetached(cmd)
	if cmd.Stdin != nil || cmd.Stdout != nil || cmd.Stderr != nil {
		t.Fatal("a detached process must not keep the terminal's streams")
	}
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr must detach the process")
	}
}

func sleeper() *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("ping", "-n", "30", "127.0.0.1")
	}
	return exec.Command("sleep", "30")
}

func sleeperName() string {
	if runtime.GOOS == "windows" {
		return "ping"
	}
	return "sleep"
}

func TestTerminatePIDRejectsPIDsThatAreNoProcessGroup(t *testing.T) {
	for _, pid := range []int{0, -1, 1} {
		if err := TerminatePID(pid); err == nil {
			t.Fatalf("TerminatePID(%d) = nil, want an error", pid)
		}
	}
}
