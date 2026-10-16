package mcpserve

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/n2jsoft/airlock/internal/aiclient"
)

// useIsolatedLocks keeps config locks of the test in its own directory.
func useIsolatedLocks(t *testing.T) {
	t.Helper()
	t.Cleanup(IsolateLocks(t.TempDir()))
}

func sampleEntry(url string) ClientEntry {
	return ClientEntry{Name: "acme-dev-app", URL: url, Token: "tok"}
}

// writeEmptyMCPJSON creates an empty .mcp.json in dir, which auto-detection takes for Claude Code.
func writeEmptyMCPJSON(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterClientsSkipsProjectsWithoutClients(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	// .claude/ alone does not make airlock create a .mcp.json.
	if err := os.Mkdir(filepath.Join(dir, ".claude"), 0o750); err != nil {
		t.Fatal(err)
	}

	written, err := RegisterClients(dir, aiclient.AutoDetect, sampleEntry("http://127.0.0.1:1/mcp/a"))
	if err != nil || len(written) != 0 {
		t.Fatalf("written = %v, err = %v", written, err)
	}
	if exists(filepath.Join(dir, ".mcp.json")) || exists(filepath.Join(dir, ".vscode")) {
		t.Fatal("no client file may be created")
	}
}

func TestRegisterClientsWritesClaudeAndVSCodeFiles(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	writeEmptyMCPJSON(t, dir)
	mustMkdir(t, filepath.Join(dir, ".vscode"))

	written, err := RegisterClients(dir, aiclient.AutoDetect, sampleEntry("http://127.0.0.1:1/mcp/a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 2 {
		t.Fatalf("written = %v", written)
	}

	claude := readFile(t, filepath.Join(dir, ".mcp.json"))
	want := `{
  "mcpServers": {
    "acme-dev-app": {
      "headers": {
        "Authorization": "Bearer tok"
      },
      "type": "http",
      "url": "http://127.0.0.1:1/mcp/a"
    }
  }
}
`
	if claude != want {
		t.Fatalf(".mcp.json =\n%s\nwant\n%s", claude, want)
	}
	if vscode := readFile(t, filepath.Join(dir, ".vscode", "mcp.json")); !strings.HasPrefix(vscode, "{\n  \"servers\": {\n    \"acme-dev-app\"") {
		t.Fatalf(".vscode/mcp.json =\n%s", vscode)
	}
}

func TestRegisterClientsPreservesOtherKeysAndOrder(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	original := `{"zeta": 1, "mcpServers": {"other": {"command": "x"}}, "alpha": [1, 2]}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := RegisterClients(dir, aiclient.AutoDetect, sampleEntry("http://127.0.0.1:1/mcp/a")); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, path)
	zeta, servers, other, ours, alpha := strings.Index(content, `"zeta"`), strings.Index(content, `"mcpServers"`),
		strings.Index(content, `"other"`), strings.Index(content, `"acme-dev-app"`), strings.Index(content, `"alpha"`)
	if zeta >= servers || servers >= other || other >= ours || ours >= alpha {
		t.Fatalf("keys reordered or lost:\n%s", content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode changed to %v", info.Mode().Perm())
	}
}

func TestRegisterClientsRejectsJSONWithComments(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	path := filepath.Join(dir, ".vscode", "mcp.json")
	mustMkdir(t, filepath.Join(dir, ".vscode"))
	original := "{\n  // my servers\n  \"servers\": {}\n}\n"
	mustWriteFile(t, path, []byte(original))

	_, err := RegisterClients(dir, aiclient.AutoDetect, sampleEntry("http://127.0.0.1:1/mcp/a"))
	if err == nil || !strings.Contains(err.Error(), "comments are not supported") {
		t.Fatalf("unexpected error: %v", err)
	}
	if readFile(t, path) != original {
		t.Fatal("a file that cannot be parsed must be left untouched")
	}
}

func TestUnregisterClientsRemovesOnlyOwnEntry(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	mustWriteFile(t, path, []byte(`{"mcpServers": {"other": {"command": "x"}}}`))
	ours := sampleEntry("http://127.0.0.1:1/mcp/a")
	if _, err := RegisterClients(dir, aiclient.AutoDetect, ours); err != nil {
		t.Fatal(err)
	}

	changed, err := UnregisterClients(dir, aiclient.AutoDetect, ours)
	if err != nil || len(changed) != 1 {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
	content := readFile(t, path)
	if strings.Contains(content, "acme-dev-app") || !strings.Contains(content, `"other"`) {
		t.Fatalf("unexpected content:\n%s", content)
	}
}

func TestUnregisterClientsKeepsEntryReplacedByAnotherSession(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	writeEmptyMCPJSON(t, dir)
	first, second := sampleEntry("http://127.0.0.1:1/mcp/a"), sampleEntry("http://127.0.0.1:2/mcp/b")
	if _, err := RegisterClients(dir, aiclient.AutoDetect, first); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterClients(dir, aiclient.AutoDetect, second); err != nil {
		t.Fatal(err)
	}

	changed, err := UnregisterClients(dir, aiclient.AutoDetect, first)
	if err != nil || len(changed) != 0 {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
	if content := readFile(t, filepath.Join(dir, ".mcp.json")); !strings.Contains(content, "127.0.0.1:2") {
		t.Fatalf("entry of the other session was removed:\n%s", content)
	}
}

func TestUnregisterClientsWithoutFiles(t *testing.T) {
	useIsolatedLocks(t)
	changed, err := UnregisterClients(t.TempDir(), aiclient.AutoDetect, sampleEntry("http://127.0.0.1:1/mcp/a"))
	if err != nil || len(changed) != 0 {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
}

func TestRegisterClientsConcurrentSessionsKeepBothEntries(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	writeEmptyMCPJSON(t, dir)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry := ClientEntry{Name: fmt.Sprintf("acme-%c", 'a'+i), URL: "http://127.0.0.1:1/mcp/x", Token: "t"}
			if _, err := RegisterClients(dir, aiclient.AutoDetect, entry); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	content := readFile(t, filepath.Join(dir, ".mcp.json"))
	for i := range 10 {
		if name := fmt.Sprintf("acme-%c", 'a'+i); !strings.Contains(content, `"`+name+`"`) {
			t.Fatalf("entry %s lost:\n%s", name, content)
		}
	}
}

func TestRegisterClientsKeepsIndentation(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	mustWriteFile(t, path, []byte("{\n\t\"mcpServers\": {}\n}\n"))

	if _, err := RegisterClients(dir, aiclient.AutoDetect, sampleEntry("http://127.0.0.1:1/mcp/a")); err != nil {
		t.Fatal(err)
	}
	if content := readFile(t, path); !strings.HasPrefix(content, "{\n\t\"mcpServers\": {\n\t\t\"acme-dev-app\"") {
		t.Fatalf("indentation not kept:\n%s", content)
	}
}

func TestRegisterClientsSkipsTheHomeDirectory(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	writeEmptyMCPJSON(t, dir)
	mustMkdir(t, filepath.Join(dir, ".vscode"))
	previous := userHomeDir
	userHomeDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userHomeDir = previous })

	written, err := RegisterClients(dir, aiclient.AutoDetect, sampleEntry("http://127.0.0.1:1/mcp/a"))
	if err != nil || len(written) != 0 {
		t.Fatalf("written = %v, err = %v", written, err)
	}
	if readFile(t, filepath.Join(dir, ".mcp.json")) != "{}" || exists(filepath.Join(dir, ".vscode", "mcp.json")) {
		t.Fatal("must not write into the user's home directory")
	}
}

func TestRegisterClientsNewFileGetsRestrictivePermissions(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()

	if _, err := RegisterClients(dir, aiclient.Selection{Targets: []aiclient.Target{{Client: aiclient.Claude, Scope: aiclient.ScopeProject}}},
		sampleEntry("http://127.0.0.1:1/mcp/a")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("new file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestRegisterClientsKeepsHTMLCharactersByteForByte(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	original := `{"mcpServers": {"other": {"command": "a && b <x>"}}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := RegisterClients(dir, aiclient.AutoDetect, sampleEntry("http://127.0.0.1:1/mcp/a")); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, path)
	if !strings.Contains(content, `"a && b <x>"`) {
		t.Fatalf("HTML characters were escaped:\n%s", content)
	}
}

func TestRegisterClientsReplacesStaleEntryOfSameTarget(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	writeEmptyMCPJSON(t, dir)
	// A crashed session left its entry behind; the next session of the same target takes the name over.
	if _, err := RegisterClients(dir, aiclient.AutoDetect, sampleEntry("http://127.0.0.1:1/mcp/crashed")); err != nil {
		t.Fatal(err)
	}

	if _, err := RegisterClients(dir, aiclient.AutoDetect, sampleEntry("http://127.0.0.1:2/mcp/new")); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, filepath.Join(dir, ".mcp.json"))
	if strings.Contains(content, "crashed") || strings.Count(content, `"acme-dev-app"`) != 1 {
		t.Fatalf("stale entry not replaced:\n%s", content)
	}
}

func TestDetectClients(t *testing.T) {
	dir := t.TempDir()
	if got := DetectClients(dir); got == nil || len(got) != 0 {
		t.Fatalf("empty dir: got %#v, want a non-nil empty slice", got)
	}
	writeEmptyMCPJSON(t, dir)
	for _, name := range []string{".cursor", ".vscode"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	want := []aiclient.Client{aiclient.Claude, aiclient.VSCode, aiclient.Cursor}
	if got := DetectClients(dir); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
