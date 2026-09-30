package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestProcessAliveReportsCurrentProcess(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Fatal("ProcessAlive(current pid) = false, want true")
	}
}

func TestProcessAliveRejectsNonPositivePID(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if ProcessAlive(pid) {
			t.Fatalf("ProcessAlive(%d) = true, want false", pid)
		}
	}
}

func TestProcessAliveReportsEndedProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$") // #nosec G204 G702 -- reruns this test binary, running no test.
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if ProcessAlive(cmd.Process.Pid) {
		t.Fatalf("ProcessAlive(%d) = true after the process ended", cmd.Process.Pid)
	}
}

func TestTerminateGroupStopsStartedCommand(t *testing.T) {
	name, args := "sleep", []string{"30"}
	if runtime.GOOS == "windows" {
		name, args = "ping", []string{"-n", "30", "127.0.0.1"}
	}
	cmd := exec.Command(name, args...)
	StartInOwnGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start %s: %v", name, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	TerminateGroup(cmd)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill() //nolint:errcheck // best effort before failing.
		t.Fatal("command still runs after TerminateGroup")
	}
}

func TestTerminateGroupIgnoresUnstartedCommand(t *testing.T) {
	TerminateGroup(nil)
	TerminateGroup(exec.Command("unused"))
}

func TestLockFileExcludesOtherHolders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lock")
	first := openLockFile(t, path)
	second := openLockFile(t, path)

	if err := LockFile(first); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan error, 1)
	go func() { acquired <- LockFile(second) }()

	select {
	case <-acquired:
		t.Fatal("second LockFile returned while the first lock was held")
	case <-time.After(200 * time.Millisecond):
	}

	if err := UnlockFile(first); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second LockFile did not return after UnlockFile")
	}
	if err := UnlockFile(second); err != nil {
		t.Fatal(err)
	}
}

func openLockFile(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- test file under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})
	return file
}
