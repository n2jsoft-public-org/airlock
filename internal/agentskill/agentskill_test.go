package agentskill

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/aiclient"
)

func fakeHome(t *testing.T, claudeConfigDir string) string {
	t.Helper()
	home := t.TempDir()
	userHomeDir = func() (string, error) { return home, nil }
	getenv = func(key string) string {
		if key == "CLAUDE_CONFIG_DIR" {
			return claudeConfigDir
		}
		return ""
	}
	t.Cleanup(func() { userHomeDir, getenv = os.UserHomeDir, os.Getenv })
	return home
}

func targets(scope aiclient.Scope, clients ...aiclient.Client) []aiclient.Target {
	out := make([]aiclient.Target, len(clients))
	for idx, client := range clients {
		out[idx] = aiclient.Target{Client: client, Scope: scope}
	}
	return out
}

func TestPaths(t *testing.T) {
	home := fakeHome(t, "")
	project := t.TempDir()
	claude := filepath.Join(project, ".claude", "skills", "airlock", "SKILL.md")
	agents := filepath.Join(project, ".agents", "skills", "airlock", "SKILL.md")

	got, err := Paths(project, targets(aiclient.ScopeProject, aiclient.Clients...))
	if err != nil || !slices.Equal(got, []string{claude, agents}) {
		t.Fatalf("project paths = %v, %v; want %v", got, err, []string{claude, agents})
	}
	got, err = Paths(project, targets(aiclient.ScopeProject, aiclient.Codex, aiclient.Gemini))
	if err != nil || !slices.Equal(got, []string{agents}) {
		t.Fatalf("codex+gemini paths = %v, %v; want %v", got, err, []string{agents})
	}
	got, err = Paths(project, targets(aiclient.ScopeUser, aiclient.Clients...))
	want := []string{filepath.Join(home, ".claude", "skills", "airlock", "SKILL.md"), filepath.Join(home, ".agents", "skills", "airlock", "SKILL.md")}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("user paths = %v, %v; want %v", got, err, want)
	}
	if _, err = Paths(project, targets(aiclient.ScopeProject, "emacs")); err == nil {
		t.Fatal("unknown client: want an error")
	}
}

func TestPathsHonorsClaudeConfigDir(t *testing.T) {
	configDir := t.TempDir()
	fakeHome(t, configDir)
	got, err := Paths("", targets(aiclient.ScopeUser, aiclient.Claude))
	want := []string{filepath.Join(configDir, "skills", "airlock", "SKILL.md")}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("paths = %v, %v; want %v", got, err, want)
	}
}

func TestTargetsDetectsTheProjectClients(t *testing.T) {
	project := t.TempDir()
	for _, name := range []string{".claude", ".cursor"} {
		if err := os.Mkdir(filepath.Join(project, name), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	sel := aiclient.Selection{Detect: true, Targets: targets(aiclient.ScopeUser, aiclient.Codex)}
	want := append(targets(aiclient.ScopeProject, aiclient.Claude, aiclient.Cursor), targets(aiclient.ScopeUser, aiclient.Codex)...)
	if got := Targets(project, sel); !slices.Equal(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	if got := Targets(project, aiclient.Selection{Targets: sel.Targets}); !slices.Equal(got, sel.Targets) {
		t.Fatalf("targets without detection = %v, want %v", got, sel.Targets)
	}
}

func TestFilesGroupTheClientsOfEachFile(t *testing.T) {
	home := fakeHome(t, "")
	project := t.TempDir()
	files, err := Files(project)
	if err != nil {
		t.Fatal(err)
	}
	others := []aiclient.Client{aiclient.VSCode, aiclient.Codex, aiclient.Cursor, aiclient.Gemini}
	want := []File{
		{Path: filepath.Join(project, ".claude", "skills", "airlock", "SKILL.md"), Clients: []aiclient.Client{aiclient.Claude}},
		{Path: filepath.Join(project, ".agents", "skills", "airlock", "SKILL.md"), Clients: others},
		{Path: filepath.Join(home, ".claude", "skills", "airlock", "SKILL.md"), Clients: []aiclient.Client{aiclient.Claude}},
		{Path: filepath.Join(home, ".agents", "skills", "airlock", "SKILL.md"), Clients: others},
	}
	if !slices.EqualFunc(files, want, func(a, b File) bool { return a.Path == b.Path && slices.Equal(a.Clients, b.Clients) }) {
		t.Fatalf("files = %v, want %v", files, want)
	}
}

func TestInstallWritesOnlyStaleFiles(t *testing.T) {
	dir := t.TempDir()
	fresh := filepath.Join(dir, "a", "SKILL.md")
	stale := filepath.Join(dir, "b", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(stale), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	written, err := Install([]string{fresh, stale})
	if err != nil || !slices.Equal(written, []string{fresh, stale}) {
		t.Fatalf("first install = %v, %v", written, err)
	}
	if got, _ := os.ReadFile(stale); !bytes.Equal(got, content) { //nolint:errcheck,gosec // a temp file; compared below.
		t.Fatalf("stale file not refreshed: %q", got)
	}
	if written, err = Install([]string{fresh, stale}); err != nil || len(written) != 0 {
		t.Fatalf("second install = %v, %v; want nothing written", written, err)
	}
}

// The clients require a name matching the directory and a description of at most 1024 characters.
func TestSkillFrontmatter(t *testing.T) {
	front, _, ok := strings.Cut(strings.TrimPrefix(string(content), "---\n"), "\n---\n")
	if !ok {
		t.Fatal("SKILL.md has no frontmatter")
	}
	fields := map[string]string{}
	for line := range strings.SplitSeq(front, "\n") {
		key, value, _ := strings.Cut(line, ": ")
		fields[key] = value
	}
	if fields["name"] != Name {
		t.Errorf("name = %q, want %q", fields["name"], Name)
	}
	if n := len(fields["description"]); n == 0 || n > 1024 {
		t.Errorf("description has %d characters, want 1-1024", n)
	}
}

func TestUserInstalledKeepsDirectoriesTheUserAddedTo(t *testing.T) {
	home := fakeHome(t, "")
	plain := filepath.Join(home, ".agents", "skills", "airlock")
	edited := filepath.Join(home, ".claude", "skills", "airlock")
	if _, err := Install([]string{filepath.Join(plain, "SKILL.md"), filepath.Join(edited, "SKILL.md")}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(edited, "notes.md"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if dirs, err := UserInstalled(); err != nil || !slices.Equal(dirs, []string{plain}) {
		t.Fatalf("dirs = %v, %v; want %v", dirs, err, []string{plain})
	}
}
