//go:build unix

package daemon

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

// Without platform.IgnoreHangup in Run, the SIGHUP below ends the test binary: that is the failure.
func TestDaemonSurvivesHangup(t *testing.T) {
	paths := testPaths(t)
	runDaemon(t, paths, time.Hour)
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	client, err := Dial(context.Background(), paths.Socket, "v-test")
	if err != nil {
		t.Fatalf("the daemon did not survive the hangup: %v", err)
	}
	defer func() { ignore(client.Close()) }()
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
}
