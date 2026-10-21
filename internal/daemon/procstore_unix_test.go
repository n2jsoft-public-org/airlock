//go:build unix

package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/platform"
)

// TestHelperSleep is the body of the processes startNamed runs: the test binary, copied under another name.
func TestHelperSleep(t *testing.T) {
	if os.Getenv("AIRLOCK_TEST_SLEEP") == "" {
		t.Skip("helper of startNamed")
	}
	time.Sleep(30 * time.Second)
}

// startNamed runs a copy of the test binary named name (system binaries cannot be copied on macOS),
// in a process group of its own like an aws process.
func startNamed(t *testing.T, name string) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self) // #nosec G304 -- the test binary.
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(copyPath, data, 0o700); err != nil { // #nosec G306 G703 -- an executable test copy in a temp dir.
		t.Fatal(err)
	}
	cmd := exec.Command(copyPath, "-test.run=^TestHelperSleep$") // #nosec G204 -- a copy of the test binary.
	cmd.Env = append(os.Environ(), "AIRLOCK_TEST_SLEEP=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ignore(cmd.Process.Kill()); ignore(cmd.Wait()) })
	time.Sleep(200 * time.Millisecond) // let it exec: ps names the process after its binary.
	return cmd
}

func TestKillLeftoversSpareAwsVaultButStopAws(t *testing.T) {
	store := newProcStore(filepath.Join(t.TempDir(), "processes.json"))
	vault, aws := startNamed(t, "aws-vault"), startNamed(t, "aws")
	for _, c := range []*exec.Cmd{vault, aws} {
		if err := store.add(c.Process.Pid); err != nil {
			t.Fatal(err)
		}
	}
	if killed := store.killLeftovers(); killed != 1 {
		t.Fatalf("killed = %d, want 1 (aws only)", killed)
	}
	done := make(chan struct{})
	go func() { ignore(aws.Wait()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the aws process was not stopped")
	}
	if !platform.ProcessAlive(vault.Process.Pid) {
		t.Fatal("aws-vault was killed")
	}
}

func TestKillLeftoversStopsTheGroupOfAnEndedAws(t *testing.T) {
	store := newProcStore(filepath.Join(t.TempDir(), "processes.json"))
	// The group leader (aws) ends, a process it spawned (session-manager-plugin) keeps running.
	leader := exec.Command("/bin/sh", "-c", "/bin/sleep 30 >/dev/null 2>&1 & echo $!")
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := leader.Output()
	if err != nil {
		t.Fatal(err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ignore(syscall.Kill(child, syscall.SIGKILL)) })
	if err := store.add(leader.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if killed := store.killLeftovers(); killed != 1 {
		t.Fatalf("killed = %d, want the group of the ended aws", killed)
	}
	for deadline := time.Now().Add(5 * time.Second); platform.ProcessAlive(child); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the process left in the group was not stopped")
		}
	}
}
