package daemon

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/platform"
)

func TestKillLeftoversOnlyKillsAWSProcesses(t *testing.T) {
	store := newProcStore(filepath.Join(t.TempDir(), "processes.json"))
	unrelated := exec.Command("sleep", "30")
	if runtime.GOOS == "windows" {
		unrelated = exec.Command("ping", "-n", "30", "127.0.0.1")
	}
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ignore(unrelated.Process.Kill()); ignore(unrelated.Wait()) })
	// A crashed daemon recorded this PID for an aws process; it now belongs to sleep.
	if err := store.add(unrelated.Process.Pid); err != nil {
		t.Fatal(err)
	}

	killed := store.killLeftovers()
	if killed != 0 {
		t.Fatalf("killed = %d, want 0: the PID is not an aws process anymore", killed)
	}
	time.Sleep(100 * time.Millisecond)
	if !platform.ProcessAlive(unrelated.Process.Pid) {
		t.Fatal("an unrelated process was killed")
	}
	if pids, err := store.read(); err != nil || len(pids) != 0 {
		t.Fatalf("the store must be emptied: %v, %v", pids, err)
	}
}
