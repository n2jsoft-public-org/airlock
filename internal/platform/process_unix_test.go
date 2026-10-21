//go:build unix

package platform

import (
	"bufio"
	"os/exec"
	"testing"
	"time"
)

func TestTerminateGroupKillsAGroupIgnoringSIGTERM(t *testing.T) {
	previous := groupKillDelay
	groupKillDelay = 300 * time.Millisecond
	t.Cleanup(func() { groupKillDelay = previous })
	// Like a stuck session-manager-plugin: SIGTERM does nothing.
	cmd := exec.Command("sh", "-c", `trap "" TERM; echo ready; sleep 30`)
	StartInOwnGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sh: %v", err)
	}
	// SIGTERM is ignored once the trap is set.
	if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	start := time.Now()
	TerminateGroup(cmd)
	if elapsed := time.Since(start); elapsed < groupKillDelay {
		t.Fatalf("TerminateGroup returned after %v, before the kill delay", elapsed)
	}
	select {
	case <-done: //nolint:errcheck // a killed process exits with an error.
	case <-time.After(5 * time.Second):
		t.Fatal("the group still runs after TerminateGroup")
	}
	deadline := time.Now().Add(5 * time.Second)
	for GroupAlive(cmd.Process.Pid) {
		if time.Now().After(deadline) {
			t.Fatal("a process of the group survived SIGKILL")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
