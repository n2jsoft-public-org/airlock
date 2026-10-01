package cli

import (
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
		otherInstances: func(string) (int, error) { return 0, nil },
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
	env.otherInstances = func(string) (int, error) { return 1, nil }
	_, err := runUninstallCmd(t, env, "", "--yes")
	if err == nil || !strings.Contains(err.Error(), "quit") {
		t.Fatalf("err = %v, want to quit the other airlock", err)
	}
	assertExists(t, install.exe, true)
}
