package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDaemonLogRotatesOnItsFirstWriteOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "daemon.log")
	writeTestFile(t, path, "running daemon\n")
	// A daemon refused by the lock never writes: the log of the running one stays in place.
	refused := newDaemonLog(path)
	if err := refused.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readTarget(t, path); got != "running daemon\n" {
		t.Fatalf("daemon.log = %q, want it untouched", got)
	}
	log := newDaemonLog(path)
	for _, line := range []string{"started\n", "stopping\n"} {
		if _, err := log.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readTarget(t, path+".1"); got != "running daemon\n" {
		t.Fatalf("daemon.log.1 = %q, want the previous log", got)
	}
	if got := readTarget(t, path); got != "started\nstopping\n" {
		t.Fatalf("daemon.log = %q", got)
	}
	if _, err := os.Stat(path + ".2"); err == nil {
		t.Fatal("one rotation per daemon")
	}
}

func TestDaemonLogKeepsWarningsForItsFirstWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	writeTestFile(t, path, "running daemon\n")
	// A refused daemon whose lookups failed still leaves the running daemon's log in place.
	refused := newDaemonLog(path)
	refused.warnLookup("MCP queries will not be logged", errors.New("no home"))
	if err := refused.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readTarget(t, path); got != "running daemon\n" {
		t.Fatalf("daemon.log = %q, want it untouched", got)
	}
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Fatal("a refused daemon must not rotate the log")
	}
	log := newDaemonLog(path)
	log.warnLookup("MCP queries will not be logged", errors.New("no home"))
	log.warnLookup("nothing failed", nil)
	if _, err := log.Write([]byte("started\n")); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	got := readTarget(t, path)
	if !strings.HasSuffix(got, " Warning: MCP queries will not be logged: no home\nstarted\n") || strings.Contains(got, "nothing failed") {
		t.Fatalf("daemon.log = %q, want the warning before the first line", got)
	}
}

func TestDaemonLogWithoutPathDiscards(t *testing.T) {
	log := newDaemonLog("")
	log.warnLookup("MCP queries will not be logged", errors.New("no home"))
	if n, err := log.Write([]byte("started\n")); err != nil || n != len("started\n") {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
}

// The runner's progress lines get the time the daemon's own lines have, even when a line arrives
// in several writes.
func TestStampedWriterTimesEveryLine(t *testing.T) {
	var b strings.Builder
	w := newStampedWriter(&b)
	w.now = func() time.Time { return time.Date(2026, 10, 5, 16, 15, 6, 0, time.UTC) }
	for _, chunk := range []string{"Using AWS profile/region... dev/eu-west-3\nUsing ", "jumphost... i-1\n"} {
		if n, err := w.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	want := "2026-10-05T16:15:06Z Using AWS profile/region... dev/eu-west-3\n2026-10-05T16:15:06Z Using jumphost... i-1\n"
	if got := b.String(); got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}
