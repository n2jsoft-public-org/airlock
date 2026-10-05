package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/keychain"
)

// testInstall is airlock installed by install.sh into a fake home, with saved data.
type testInstall struct {
	keychain                  *keychain.MemoryStore
	home, exe, zshrc, dataDir string
}

func newTestInstall(t *testing.T) *testInstall {
	t.Helper()
	home := t.TempDir()
	install := &testInstall{
		home:     home,
		exe:      filepath.Join(home, ".local", "bin", "airlock"),
		zshrc:    filepath.Join(home, ".zshrc"),
		dataDir:  filepath.Join(home, ".config", "airlock"),
		keychain: keychain.NewMemoryStore(),
	}
	writeTestFile(t, install.exe, "airlock")
	writeTestFile(t, install.zshrc, "\n# Added by the airlock installer\nexport PATH=\""+filepath.Dir(install.exe)+":$PATH\"\n")
	writeTestFile(t, filepath.Join(install.dataDir, "connections.json"), "{}")
	if err := install.keychain.Set("111/eu-west-3/rds/archive/app", "s3cret"); err != nil {
		t.Fatal(err)
	}
	return install
}

func (i *testInstall) env(interactive bool) *uninstallEnv {
	return &uninstallEnv{
		goos:           runtime.GOOS,
		executable:     func() (string, error) { return i.exe, nil },
		home:           func() (string, error) { return i.home, nil },
		getenv:         func(string) string { return "" },
		dataDir:        func() (string, error) { return i.dataDir, nil },
		keychain:       func() keychain.Store { return i.keychain },
		userPath:       nil,
		otherInstances: func(string, int) (int, error) { return 0, nil },
		findDaemon:     func(context.Context) *runningDaemon { return nil },
		stopDaemon:     func(context.Context) (bool, error) { return false, errors.New("no daemon to stop") },
		interactive:    func(io.Reader) bool { return interactive },
	}
}

func runUninstallCmd(t *testing.T, env *uninstallEnv, stdin string, args ...string) (string, error) {
	t.Helper()
	cmd := newUninstallCmdWith(env)
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func assertExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if got := err == nil; got != want {
		t.Fatalf("%s exists = %v, want %v (%v)", path, got, want, err)
	}
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows deletes the binary after airlock exits")
	}
}

func TestUninstallRemovesBinaryAndPathKeepsData(t *testing.T) {
	skipOnWindows(t)
	install := newTestInstall(t)
	out, err := runUninstallCmd(t, install.env(false), "", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	assertExists(t, install.exe, false)
	assertExists(t, install.dataDir, true)
	if zshrc := readTarget(t, install.zshrc); zshrc != "" {
		t.Fatalf(".zshrc = %q, want the block removed", zshrc)
	}
	for _, want := range []string{
		"airlock uninstall removes:",
		install.exe,
		"It keeps:",
		"add --purge",
		"Removed " + install.exe,
		"Removed the PATH entry in " + install.zshrc,
		"airlock is uninstalled",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestUninstallPurge(t *testing.T) {
	skipOnWindows(t)
	install := newTestInstall(t)
	out, err := runUninstallCmd(t, install.env(false), "", "--yes", "--purge")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	assertExists(t, install.exe, false)
	assertExists(t, install.dataDir, false)
	if _, err := install.keychain.Get("111/eu-west-3/rds/archive/app"); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry still there (%v)", err)
	}
}

func TestUninstallAsksFirst(t *testing.T) {
	skipOnWindows(t)
	install := newTestInstall(t)
	out, err := runUninstallCmd(t, install.env(true), "n\n")
	if err == nil || !strings.Contains(out, "Remove airlock? [y/N]") {
		t.Fatalf("a no must fail without removing anything: %v\n%s", err, out)
	}
	assertExists(t, install.exe, true)

	if out, err = runUninstallCmd(t, install.env(true), "y\n"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	assertExists(t, install.exe, false)
}

func TestUninstallWithoutTerminalNeedsYes(t *testing.T) {
	install := newTestInstall(t)
	_, err := runUninstallCmd(t, install.env(false), "y\n")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v, want a hint about --yes", err)
	}
	assertExists(t, install.exe, true)
}

func TestUninstallRefusesWhileAnotherAirlockRuns(t *testing.T) {
	install := newTestInstall(t)
	env := install.env(false)
	env.otherInstances = func(string, int) (int, error) { return 1, nil }
	_, err := runUninstallCmd(t, env, "", "--yes")
	if err == nil || !strings.Contains(err.Error(), "quit") {
		t.Fatalf("err = %v, want to quit the other airlock", err)
	}
	assertExists(t, install.exe, true)
}

// withDaemon makes env find a daemon of pid 4242 with sessions; stops counts its stops.
func withDaemon(env *uninstallEnv, sessions int, stops *int, beforeStop func()) {
	env.findDaemon = func(context.Context) *runningDaemon { return &runningDaemon{PID: 4242, Sessions: sessions} }
	env.stopDaemon = func(context.Context) (bool, error) {
		*stops++
		beforeStop()
		return true, nil
	}
}

func TestUninstallAnsweredNoLeavesTheDaemonRunning(t *testing.T) {
	skipOnWindows(t)
	install := newTestInstall(t)
	env := install.env(true)
	stops := 0
	withDaemon(env, 2, &stops, func() {})
	out, err := runUninstallCmd(t, env, "n\n")
	if err == nil || stops != 0 {
		t.Fatalf("a no must leave the daemon and its sessions: err = %v, stops = %d\n%s", err, stops, out)
	}
	if !strings.Contains(out, "Also stops the airlock daemon and its 2 sessions.") {
		t.Fatalf("the plan does not mention the daemon:\n%s", out)
	}
	assertExists(t, install.exe, true)
}

func TestUninstallYesStopsTheDaemonBeforeRemoving(t *testing.T) {
	skipOnWindows(t)
	install := newTestInstall(t)
	env := install.env(false)
	excluded := 0
	env.otherInstances = func(_ string, daemonPID int) (int, error) {
		excluded = daemonPID
		return 0, nil
	}
	stops := 0
	withDaemon(env, 1, &stops, func() { assertExists(t, install.exe, true) })
	out, err := runUninstallCmd(t, env, "", "--yes")
	if err != nil || stops != 1 {
		t.Fatalf("err = %v, stops = %d\n%s", err, stops, out)
	}
	if excluded != 4242 {
		t.Fatalf("the daemon must not count as another airlock: excluded pid = %d", excluded)
	}
	for _, want := range []string{"Also stops the airlock daemon and its 1 session.", "Stopped the airlock daemon and its sessions."} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "Stopped the airlock daemon") > strings.Index(out, "Removed "+install.exe) {
		t.Fatalf("the daemon must stop before the removal:\n%s", out)
	}
	assertExists(t, install.exe, false)
}

func TestUninstallFailsWhenTheDaemonDoesNotStop(t *testing.T) {
	install := newTestInstall(t)
	env := install.env(false)
	env.findDaemon = func(context.Context) *runningDaemon { return &runningDaemon{PID: 4242} }
	env.stopDaemon = func(context.Context) (bool, error) { return false, errors.New("timeout") }
	_, err := runUninstallCmd(t, env, "", "--yes")
	if err == nil || !strings.Contains(err.Error(), "daemon") {
		t.Fatalf("err = %v, want the daemon named", err)
	}
	assertExists(t, install.exe, true)
}
