package kubeconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// deadPID stands for a process that ended; processAlive reports it so in these tests.
const deadPID = 999999

func withDeadPID(t *testing.T) {
	t.Helper()
	alive := processAlive
	processAlive = func(pid int) bool { return pid != deadPID && alive(pid) }
	t.Cleanup(func() { processAlive = alive })
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, os.ErrNotExist)
}

func TestWriteRecordsItsOwner(t *testing.T) {
	path, _, err := Write(t.TempDir(), testCluster())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := os.ReadFile(path + ownerSuffix) // #nosec G304 -- test file.
	if err != nil || strings.TrimSpace(string(owner)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("owner = %q, %v, want this process", owner, err)
	}
}

func TestRemoveOrphansKeepsLiveSessions(t *testing.T) {
	withDeadPID(t)
	dir := t.TempDir()
	live, _, err := Write(dir, testCluster())
	if err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(dir, "airlock-dev-eu-west-1-old.json")
	writeFile(t, dead, "{}")
	writeFile(t, dead+ownerSuffix, strconv.Itoa(deadPID))
	unowned := filepath.Join(dir, "airlock-dev-eu-west-1-unowned.json")
	writeFile(t, unowned, "{}")
	other := filepath.Join(dir, "notes.txt")
	writeFile(t, other, "kept")

	removed, err := RemoveOrphans(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 || exists(dead) || exists(dead+ownerSuffix) || exists(unowned) {
		t.Fatalf("removed = %v, want the files of the ended process and the unowned one", removed)
	}
	if !exists(live) || !exists(other) {
		t.Fatal("RemoveOrphans removed the kubeconfig of a running session, or a file it does not own")
	}
}

func TestRemoveOrphansWithoutDirectory(t *testing.T) {
	if removed, err := RemoveOrphans(filepath.Join(t.TempDir(), "absent")); err != nil || len(removed) != 0 {
		t.Fatalf("removed = %v, err = %v, want nothing to do", removed, err)
	}
}

func TestRemoveLeavesAFileTakenOverByAnotherProcess(t *testing.T) {
	path, remove, err := Write(t.TempDir(), testCluster())
	if err != nil {
		t.Fatal(err)
	}
	// Another airlock opened the same cluster, profile and region: the file is now its own.
	writeFile(t, path+ownerSuffix, strconv.Itoa(os.Getpid()+1))
	if err := remove(); err != nil {
		t.Fatal(err)
	}
	if !exists(path) {
		t.Fatal("remove deleted the kubeconfig of another airlock")
	}
}
