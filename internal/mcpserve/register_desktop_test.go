package mcpserve

import (
	"path/filepath"
	"reflect"
	"testing"
)

// useFakeExecutable makes the airlock binary written in stdio entries path.
func useFakeExecutable(t *testing.T, path string) {
	t.Helper()
	previous := executable
	executable = func() (string, error) { return path, nil }
	t.Cleanup(func() { executable = previous })
}

func TestClaudeUserAlsoWritesClaudeDesktopWhenInstalled(t *testing.T) {
	useIsolatedLocks(t)
	home := useFakeHome(t, nil)
	useFakeExecutable(t, "/opt/airlock/bin/airlock")
	desktopDir := filepath.Join(home, "config", "Claude")
	mustMkdir(t, filepath.Join(home, "config"))
	mustMkdir(t, desktopDir)
	desktop := filepath.Join(desktopDir, "claude_desktop_config.json")
	mustWriteFile(t, desktop, []byte("{\n  \"globalShortcut\": \"\"\n}\n"))
	entry := sampleEntry("http://127.0.0.1:1/mcp/a")

	written, err := RegisterClients(t.TempDir(), mustParse(t, "claude:user"), entry)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(home, ".claude.json"), desktop}; !reflect.DeepEqual(written, want) {
		t.Fatalf("written = %v, want %v", written, want)
	}
	want := "{\n  \"globalShortcut\": \"\",\n  \"mcpServers\": {\n    \"acme-dev-app\": {\n" +
		"      \"env\": {\n        \"AIRLOCK_MCP_TOKEN\": \"tok\"\n      },\n" +
		"      \"command\": \"/opt/airlock/bin/airlock\",\n" +
		"      \"args\": [\n        \"mcp-proxy\",\n        \"http://127.0.0.1:1/mcp/a\"\n      ]\n    }\n  }\n}\n"
	if got := readFile(t, desktop); got != want {
		t.Fatalf("claude_desktop_config.json =\n%s", got)
	}

	files, err := EntryFiles(mustParse(t, "claude:user"), entry)
	if err != nil || !reflect.DeepEqual(files, written) {
		t.Fatalf("EntryFiles = %v, %v", files, err)
	}
	other := entry
	other.URL = "http://127.0.0.1:2/mcp/b"
	if changed, otherErr := UnregisterClients("", mustParse(t, "claude:user"), other); otherErr != nil || len(changed) != 0 {
		t.Fatalf("an entry of another URL is not ours: %v, %v", changed, otherErr)
	}
	changed, err := UnregisterClients("", mustParse(t, "claude:user"), entry)
	if err != nil || !reflect.DeepEqual(changed, written) {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
	if got := readFile(t, desktop); got != "{\n  \"globalShortcut\": \"\",\n  \"mcpServers\": {}\n}\n" {
		t.Fatalf("claude_desktop_config.json after removal =\n%s", got)
	}
}

func TestClaudeUserSkipsClaudeDesktopWhenNotInstalled(t *testing.T) {
	useIsolatedLocks(t)
	home := useFakeHome(t, nil)

	written, err := RegisterClients(t.TempDir(), mustParse(t, "claude:user"), sampleEntry("http://127.0.0.1:1/mcp/a"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(home, ".claude.json")}; !reflect.DeepEqual(written, want) {
		t.Fatalf("written = %v, want %v", written, want)
	}
	if exists(filepath.Join(home, "config", "Claude")) {
		t.Fatal("the Claude Desktop directory must not be created")
	}
}

func TestClaudeProjectLeavesClaudeDesktopAlone(t *testing.T) {
	useIsolatedLocks(t)
	home := useFakeHome(t, nil)
	mustMkdir(t, filepath.Join(home, "config"))
	mustMkdir(t, filepath.Join(home, "config", "Claude"))
	dir := t.TempDir()

	written, err := RegisterClients(dir, mustParse(t, "claude"), sampleEntry("http://127.0.0.1:1/mcp/a"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{filepath.Join(dir, ".mcp.json")}; !reflect.DeepEqual(written, want) {
		t.Fatalf("written = %v, want %v", written, want)
	}
}
