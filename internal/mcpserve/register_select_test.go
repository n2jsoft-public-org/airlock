package mcpserve

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/aiclient"
)

// useFakeHome points the user-level client files into a temporary home, and returns it.
func useFakeHome(t *testing.T, env map[string]string) string {
	t.Helper()
	home := t.TempDir()
	previousHome, previousConfig, previousEnv := userHomeDir, userConfigDir, getenv
	userHomeDir = func() (string, error) { return home, nil }
	userConfigDir = func() (string, error) { return filepath.Join(home, "config"), nil }
	getenv = func(key string) string { return env[key] }
	t.Cleanup(func() { userHomeDir, userConfigDir, getenv = previousHome, previousConfig, previousEnv })
	return home
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
}

func mustParse(t *testing.T, s string) aiclient.Selection {
	t.Helper()
	sel, err := aiclient.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return sel
}

func TestRegisterClientsWritesExplicitProjectClientsWithoutDetection(t *testing.T) {
	useIsolatedLocks(t)
	useFakeHome(t, nil)
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ".vscode")) // detected, but not selected

	written, err := RegisterClients(dir, mustParse(t, "codex,cursor,gemini"), sampleEntry("http://127.0.0.1:1/mcp/a"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(dir, ".codex", "config.toml"),
		filepath.Join(dir, ".cursor", "mcp.json"),
		filepath.Join(dir, ".gemini", "settings.json"),
	}
	if !reflect.DeepEqual(written, want) {
		t.Fatalf("written = %v, want %v", written, want)
	}
	if exists(filepath.Join(dir, ".vscode", "mcp.json")) {
		t.Fatal("a client left out of the selection must not be written")
	}

	cursor := readFile(t, filepath.Join(dir, ".cursor", "mcp.json"))
	if want := "{\n  \"mcpServers\": {\n    \"acme-dev-app\": {\n      \"headers\": {\n        \"Authorization\": \"Bearer tok\"\n      },\n      \"url\": \"http://127.0.0.1:1/mcp/a\"\n    }\n  }\n}\n"; cursor != want {
		t.Fatalf("cursor =\n%s", cursor)
	}
	gemini := readFile(t, filepath.Join(dir, ".gemini", "settings.json"))
	if !strings.Contains(gemini, `"httpUrl": "http://127.0.0.1:1/mcp/a"`) || strings.Contains(gemini, `"url"`) {
		t.Fatalf("gemini =\n%s", gemini)
	}
	codex := readFile(t, filepath.Join(dir, ".codex", "config.toml"))
	if want := "[mcp_servers.acme-dev-app]\nurl = \"http://127.0.0.1:1/mcp/a\"\nhttp_headers = { Authorization = \"Bearer tok\" }\n"; codex != want {
		t.Fatalf("codex =\n%s", codex)
	}

	changed, err := UnregisterClients(dir, mustParse(t, "codex,cursor,gemini"), sampleEntry("http://127.0.0.1:1/mcp/a"))
	if err != nil || !reflect.DeepEqual(changed, want) {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
	if got := readFile(t, filepath.Join(dir, ".codex", "config.toml")); got != "" {
		t.Fatalf("codex after removal = %q", got)
	}
}

func TestRegisterClientsWritesUserFiles(t *testing.T) {
	useIsolatedLocks(t)
	home := useFakeHome(t, nil)
	dir := t.TempDir()

	written, err := RegisterClients(dir, mustParse(t, "claude:user,vscode:user,codex:user,cursor:user,gemini:user"),
		sampleEntry("http://127.0.0.1:1/mcp/a"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, "config", "Code", "User", "mcp.json"),
		filepath.Join(home, ".codex", "config.toml"),
		filepath.Join(home, ".cursor", "mcp.json"),
		filepath.Join(home, ".gemini", "settings.json"),
	}
	if !reflect.DeepEqual(written, want) {
		t.Fatalf("written = %v, want %v", written, want)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("the project must stay untouched: %v, %v", entries, err)
	}
}

func TestRegisterClientsUserFilesFollowTheClientsHomeVariables(t *testing.T) {
	useIsolatedLocks(t)
	env := map[string]string{"CLAUDE_CONFIG_DIR": t.TempDir(), "CODEX_HOME": t.TempDir()}
	useFakeHome(t, env)

	written, err := RegisterClients(t.TempDir(), mustParse(t, "claude:user,codex:user"), sampleEntry("http://127.0.0.1:1/mcp/a"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(env["CLAUDE_CONFIG_DIR"], ".claude.json"), filepath.Join(env["CODEX_HOME"], "config.toml")}
	if !reflect.DeepEqual(written, want) {
		t.Fatalf("written = %v, want %v", written, want)
	}
}

func TestRegisterClientsAutoCombinesWithUserFiles(t *testing.T) {
	useIsolatedLocks(t)
	home := useFakeHome(t, nil)
	dir := t.TempDir()
	writeEmptyMCPJSON(t, dir)

	// claude (project) is both detected and selected: it is written once.
	written, err := RegisterClients(dir, mustParse(t, "auto,claude,codex:user"), sampleEntry("http://127.0.0.1:1/mcp/a"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, ".mcp.json"), filepath.Join(home, ".codex", "config.toml")}
	if !reflect.DeepEqual(written, want) {
		t.Fatalf("written = %v, want %v", written, want)
	}
}

func TestRegisterClientsNoneWritesNothing(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	writeEmptyMCPJSON(t, dir)

	written, err := RegisterClients(dir, mustParse(t, "none"), sampleEntry("http://127.0.0.1:1/mcp/a"))
	if err != nil || len(written) != 0 || readFile(t, filepath.Join(dir, ".mcp.json")) != "{}" {
		t.Fatalf("written = %v, err = %v", written, err)
	}
}

func TestRegisterClientsHomeDirectoryKeepsUserScope(t *testing.T) {
	useIsolatedLocks(t)
	home := useFakeHome(t, nil)

	written, err := RegisterClients(home, mustParse(t, "codex,codex:user"), sampleEntry("http://127.0.0.1:1/mcp/a"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(home, ".codex", "config.toml")}; !reflect.DeepEqual(written, want) {
		t.Fatalf("written = %v, want %v", written, want)
	}
}

func TestUnregisterClientsAutoCoversUndetectedClients(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ".cursor"))
	entry := sampleEntry("http://127.0.0.1:1/mcp/a")
	if _, err := RegisterClients(dir, aiclient.AutoDetect, entry); err != nil {
		t.Fatal(err)
	}

	changed, err := UnregisterClients(dir, aiclient.AutoDetect, entry)
	if err != nil || len(changed) != 1 || !strings.HasSuffix(changed[0], filepath.Join(".cursor", "mcp.json")) {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
}
