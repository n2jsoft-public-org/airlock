package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/daemon"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/platform"
	"github.com/n2jsoft/airlock/internal/version"
)

// daemonTestEnv points the daemon's files to temporary directories; it returns the home and the
// daemon log path.
func daemonTestEnv(t *testing.T) (home, logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the daemon's cache directory is not under HOME on Windows")
	}
	home = shortTempDir(t)
	configRoot := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	return home, filepath.Join(configRoot, "airlock", "logs", "daemon.log")
}

func runDaemonCmd(ctx context.Context, args ...string) error {
	cmd := newDaemonCmd()
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	return cmd.ExecuteContext(ctx)
}

func TestDaemonRunLogsWhyItCannotStart(t *testing.T) {
	home, logPath := daemonTestEnv(t)
	// The daemon's directory cannot be created: a file is in the way (macOS, then Linux cache).
	writeTestFile(t, filepath.Join(home, "Library", "Caches", "airlock"), "")
	writeTestFile(t, filepath.Join(home, "cache", "airlock"), "")
	err := runDaemonCmd(context.Background(), "run")
	if err == nil {
		t.Fatal("the daemon must not start")
	}
	if got := readTarget(t, logPath); !strings.Contains(got, "daemon failed: "+err.Error()) {
		t.Fatalf("daemon.log = %q, want the error %q", got, err)
	}
}

func TestRefusedDaemonLeavesTheRunningDaemonsLog(t *testing.T) {
	_, logPath := daemonTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runDaemonCmd(ctx, "run") }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the daemon did not stop")
		}
	})
	paths, err := daemon.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		if client, _ := daemon.Running(ctx, paths, version.String()); client != nil { //nolint:errcheck // same version.
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the daemon did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := runDaemonCmd(context.Background(), "run"); !errors.Is(err, daemon.ErrAlreadyRunning) {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
	if got := readTarget(t, logPath); !strings.Contains(got, "started") {
		t.Fatalf("daemon.log = %q, want the running daemon's", got)
	}
	if _, err := os.Stat(logPath + ".1"); err == nil {
		t.Fatal("a refused daemon must not rotate the log")
	}
}

func TestWaitDaemonUnlockedWaitsForTheLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	holder, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- a test file.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := holder.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := platform.LockFile(holder); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := waitDaemonUnlocked(short, path); err == nil || !strings.Contains(err.Error(), "did not exit") {
		t.Fatalf("err = %v, want a timeout while the lock is held", err)
	}
	released := make(chan error, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		released <- platform.UnlockFile(holder)
	}()
	start := time.Now()
	if err := waitDaemonUnlocked(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("returned after %v, before the lock was released", elapsed)
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}
}

func TestDaemonLogStaysEnglishInFrench(t *testing.T) {
	i18n.SetLanguage("fr")
	t.Cleanup(func() { i18n.SetLanguage("en") })
	home, logPath := daemonTestEnv(t)
	writeTestFile(t, filepath.Join(home, "Library", "Caches", "airlock"), "")
	writeTestFile(t, filepath.Join(home, "cache", "airlock"), "")
	if err := runDaemonCmd(context.Background(), "run"); err == nil {
		t.Fatal("the daemon must not start")
	}
	if got := readTarget(t, logPath); !strings.Contains(got, "daemon failed: ") {
		t.Fatalf("daemon.log = %q, want the English log line", got)
	}
}
