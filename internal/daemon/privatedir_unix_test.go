//go:build unix

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirOfAnotherUserIsRefused(t *testing.T) {
	dir := shortDir(t)
	if err := checkDirOwner(dir, os.Getuid()+1, false); err == nil || !strings.Contains(err.Error(), "belongs to another user") {
		t.Fatalf("err = %v", err)
	}
	if err := checkDirOwner(dir, os.Getuid(), true); err != nil {
		t.Fatal(err)
	}
}

func TestDirOpenToOthersIsRefused(t *testing.T) {
	dir := shortDir(t)
	if err := os.Chmod(dir, 0o755); err != nil { // #nosec G302 -- the mode under test.
		t.Fatal(err)
	}
	if err := checkPrivateDir(dir); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunMakesItsDirectoryAndSocketPrivate(t *testing.T) {
	paths := testPaths(t)
	if err := os.Chmod(paths.Dir, 0o755); err != nil { // #nosec G302 -- the mode Run must fix.
		t.Fatal(err)
	}
	runDaemon(t, paths, time.Hour)
	assertPerm(t, paths.Dir, 0o700)
	assertPerm(t, paths.Socket, 0o600)
}

func TestRunningWithoutDaemonDirectoryIsNoDaemon(t *testing.T) {
	paths, err := PathsIn(filepath.Join(shortDir(t), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if client, err := Running(context.Background(), paths, "v-test"); client != nil || err != nil {
		t.Fatalf("client = %v, err = %v", client, err)
	}
}
