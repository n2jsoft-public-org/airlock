package uninstall

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/keychain"
)

// machine is a fake home: airlock installed by install.sh in ~/.local/bin, with its PATH block in
// .zshrc, and its user files in ~/.config/airlock.
type machine struct {
	keychain                          *keychain.MemoryStore
	home, binDir, exe, zshrc, dataDir string
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	home := t.TempDir()
	m := &machine{
		home:     home,
		binDir:   filepath.Join(home, ".local", "bin"),
		zshrc:    filepath.Join(home, ".zshrc"),
		dataDir:  filepath.Join(home, ".config", "airlock"),
		keychain: keychain.NewMemoryStore(),
	}
	m.exe = filepath.Join(m.binDir, "airlock")
	writeFile(t, m.exe, "binary")
	writeFile(t, m.zshrc, "alias ll='ls -l'\n\n# Added by the airlock installer\nexport PATH=\""+m.binDir+":$PATH\"\n")
	writeFile(t, filepath.Join(m.dataDir, "connections.json"), "{}")
	writeFile(t, filepath.Join(m.dataDir, "logs", "mcp-queries-2026-10-01.jsonl"), "{}\n")
	if err := m.keychain.Set("111/eu-west-3/rds/archive/app", "s3cret"); err != nil {
		t.Fatal(err)
	}
	return m
}

func (m *machine) env() *Env {
	return &Env{
		GOOS:       "linux",
		Executable: m.exe,
		Profiles:   ShellProfiles(m.home, func(string) string { return "" }),
		DataDir:    m.dataDir,
		Keychain:   m.keychain,
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func kinds(items []Item) []Kind {
	got := make([]Kind, 0, len(items))
	for _, item := range items {
		got = append(got, item.Kind)
	}
	return got
}

func assertKinds(t *testing.T, items []Item, want ...Kind) {
	t.Helper()
	got := kinds(items)
	if len(got) != len(want) {
		t.Fatalf("items = %v, want kinds %v", items, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("items = %v, want kinds %v", items, want)
		}
	}
}

func assertKept(t *testing.T, plan *Plan, substring string) {
	t.Helper()
	for _, kept := range plan.Kept {
		if strings.Contains(kept, substring) {
			return
		}
	}
	t.Fatalf("Kept = %q, want a line containing %q", plan.Kept, substring)
}

func TestNewPlanKeepsDataWithoutPurge(t *testing.T) {
	m := newMachine(t)
	plan, err := NewPlan(m.env(), false)
	if err != nil {
		t.Fatal(err)
	}
	assertKinds(t, plan.Remove, Binary, ProfileEntry)
	if plan.Remove[0].Path != m.exe || plan.Remove[1].Path != m.zshrc {
		t.Fatalf("Remove = %v", plan.Remove)
	}
	assertKept(t, &plan, m.dataDir)
	assertKept(t, &plan, "--purge")
	assertKept(t, &plan, "dataSources.xml")
}

func TestNewPlanWithPurge(t *testing.T) {
	m := newMachine(t)
	plan, err := NewPlan(m.env(), true)
	if err != nil {
		t.Fatal(err)
	}
	assertKinds(t, plan.Remove, Binary, ProfileEntry, UserData, KeychainSecrets)
	for _, kept := range plan.Kept {
		if strings.Contains(kept, "--purge") {
			t.Fatalf("Kept = %q: nothing is left for --purge", plan.Kept)
		}
	}
}

func TestNewPlanPurgeWithoutDataOrKeychain(t *testing.T) {
	m := newMachine(t)
	env := m.env()
	env.DataDir = filepath.Join(m.home, "missing")
	env.Keychain = nil
	plan, err := NewPlan(env, true)
	if err != nil {
		t.Fatal(err)
	}
	assertKinds(t, plan.Remove, Binary, ProfileEntry)
}

func TestNewPlanKeepsPathOfSharedFolder(t *testing.T) {
	m := newMachine(t)
	writeFile(t, filepath.Join(m.binDir, "claude"), "another tool")
	plan, err := NewPlan(m.env(), false)
	if err != nil {
		t.Fatal(err)
	}
	assertKinds(t, plan.Remove, Binary)
	assertKept(t, &plan, "PATH")
}

func TestNewPlanIgnoresLeftoversOfUpdates(t *testing.T) {
	m := newMachine(t)
	writeFile(t, m.exe+".old", "previous")
	writeFile(t, filepath.Join(m.binDir, ".airlock.new"), "staged")
	plan, err := NewPlan(m.env(), false)
	if err != nil {
		t.Fatal(err)
	}
	assertKinds(t, plan.Remove, Binary, ProfileEntry)
}

func TestNewPlanGoInstall(t *testing.T) {
	m := newMachine(t)
	env := m.env()
	env.Executable = filepath.Join(m.home, "go", "bin", "airlock")
	writeFile(t, env.Executable, "binary")
	plan, err := NewPlan(env, false)
	if err != nil {
		t.Fatal(err)
	}
	// The .zshrc block belongs to ~/.local/bin, not to this binary's folder.
	assertKinds(t, plan.Remove, Binary)
}

func TestNewPlanWindows(t *testing.T) {
	m := newMachine(t)
	env := m.env()
	env.GOOS = "windows"
	env.UserPath = func() (string, error) { return `C:\tools;` + m.binDir, nil }
	plan, err := NewPlan(env, false)
	if err != nil {
		t.Fatal(err)
	}
	assertKinds(t, plan.Remove, Binary, InstallDir, UserPathEntry)

	env.UserPath = func() (string, error) { return "", errors.New("registry locked") }
	if _, err := NewPlan(env, false); err == nil {
		t.Fatal("a user PATH that cannot be read must fail the plan")
	}
}

func TestApply(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows deletes the binary after airlock exits")
	}
	m := newMachine(t)
	env := m.env()
	env.GOOS = runtime.GOOS
	plan, err := NewPlan(env, true)
	if err != nil {
		t.Fatal(err)
	}
	var done []Item
	if err = Apply(&plan, env, func(item Item) { done = append(done, item) }); err != nil {
		t.Fatal(err)
	}
	assertKinds(t, done, ProfileEntry, UserData, KeychainSecrets, Binary)

	for _, gone := range []string{m.exe, m.dataDir} {
		if _, statErr := os.Stat(gone); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%s still exists (%v)", gone, statErr)
		}
	}
	zshrc, err := os.ReadFile(m.zshrc)
	if err != nil {
		t.Fatal(err)
	}
	if string(zshrc) != "alias ll='ls -l'\n" {
		t.Fatalf(".zshrc = %q", zshrc)
	}
	if _, err := m.keychain.Get("111/eu-west-3/rds/archive/app"); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry still there (%v)", err)
	}
	// The folder is a standard one, other tools install into it later.
	if _, err := os.Stat(m.binDir); err != nil {
		t.Fatalf("%s was removed: %v", m.binDir, err)
	}
}

func TestApplyRemovesTheFishFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows deletes the binary after airlock exits")
	}
	m := newMachine(t)
	env := m.env()
	env.GOOS = runtime.GOOS
	fish := env.Profiles[3]
	writeFile(t, fish, "\n# Added by the airlock installer\nfish_add_path -g \""+m.binDir+"\"\n")
	plan, err := NewPlan(env, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(&plan, env, func(Item) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fish); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s still exists (%v)", fish, err)
	}
}

func TestApplyGoesOnAfterAFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows deletes the binary after airlock exits")
	}
	m := newMachine(t)
	env := m.env()
	env.GOOS = runtime.GOOS
	m.keychain.Err = errors.New("keychain locked")
	plan, err := NewPlan(env, true)
	if err != nil {
		t.Fatal(err)
	}
	err = Apply(&plan, env, func(Item) {})
	if err == nil || !strings.Contains(err.Error(), "keychain locked") {
		t.Fatalf("Apply = %v, want the keychain error", err)
	}
	if _, statErr := os.Stat(m.exe); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the binary must still be removed after a keychain failure (%v)", statErr)
	}
}
