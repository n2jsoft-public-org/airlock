package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/sessions/sessionstest"
)

// assertPerm fails t unless path exists with the permission bits perm.
func assertPerm(t *testing.T, path string, perm os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != perm {
		t.Fatalf("%s mode = %v, want %v", path, info.Mode().Perm(), perm)
	}
}

func runDaemon(t *testing.T, paths Paths, idle time.Duration) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	finished := make(chan struct{}) // closed, so the cleanup waits even when a test consumed errs
	go func() {
		defer close(finished)
		errs <- Run(ctx, &RunConfig{Paths: paths, Version: "v-test", Runner: sessions.Fixed(sessionstest.NewRunner()),
			Launch: sessionstest.NewLauncher().Launch, IdleTimeout: idle})
	}()
	waitSocket(t, paths)
	t.Cleanup(func() { cancel(); <-finished })
	return errs
}

func testPaths(t *testing.T) Paths {
	t.Helper()
	paths, err := PathsIn(shortDir(t))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func waitSocket(t *testing.T, paths Paths) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := Dial(context.Background(), paths.Socket, "v-test"); err == nil {
			ignore(c.Close())
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("daemon socket never answered")
}

func TestSecondDaemonRefusesToStart(t *testing.T) {
	paths := testPaths(t)
	runDaemon(t, paths, time.Hour)
	err := Run(context.Background(), &RunConfig{Paths: paths, Version: "v-test", Runner: sessions.Fixed(sessionstest.NewRunner())})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("err = %v", err)
	}
}

func TestStaleSocketIsReplaced(t *testing.T) {
	paths := testPaths(t)
	if err := os.WriteFile(paths.Socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runDaemon(t, paths, time.Hour)
}

func TestSocketIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permissions")
	}
	paths := testPaths(t)
	runDaemon(t, paths, time.Hour)
	assertPerm(t, paths.Dir, 0o700)
}

func TestIdleDaemonStops(t *testing.T) {
	paths := testPaths(t)
	done := runDaemon(t, paths, 200*time.Millisecond)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an idle daemon must stop")
	}
}

func TestEnsureStartsTheDaemonOnce(t *testing.T) {
	paths := testPaths(t)
	starts := 0
	start := func() error {
		starts++
		go func() {
			ignore(Run(context.Background(), &RunConfig{Paths: paths, Version: "v-test", Runner: sessions.Fixed(sessionstest.NewRunner()), IdleTimeout: time.Second}))
		}()
		return nil
	}
	first, err := Ensure(context.Background(), paths, "v-test", start, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { ignore(first.Close()) }()
	second, err := Ensure(context.Background(), paths, "v-test", start, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { ignore(second.Close()) }()
	if starts != 1 {
		t.Fatalf("starts = %d", starts)
	}
	ignore(second.StopDaemon(context.Background()))
}

func TestRunningGivesUpOnADaemonThatNeverAnswers(t *testing.T) {
	paths := testPaths(t)
	l, err := net.Listen("unix", paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { ignore(l.Close()) }()
	go func() {
		var held []net.Conn // accepted and never answered.
		defer func() {
			for _, c := range held {
				ignore(c.Close())
			}
		}()
		for {
			c, acceptErr := l.Accept()
			if acceptErr != nil {
				return
			}
			held = append(held, c)
		}
	}()
	start := time.Now()
	client, err := Running(context.Background(), paths, "v-test")
	if client != nil || err != nil || time.Since(start) > 3*time.Second {
		t.Fatalf("client = %v, err = %v after %v, want nil, nil within 3s", client, err, time.Since(start))
	}
}
