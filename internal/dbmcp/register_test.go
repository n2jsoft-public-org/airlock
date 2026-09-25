package dbmcp

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// useIsolatedLocks keeps config locks of the test in its own directory.
func useIsolatedLocks(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	previous := lockRoot
	lockRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { lockRoot = previous })
}

func sampleEntry(url string) ClientEntry {
	return ClientEntry{Name: "acme-dev-app", URL: url, Token: "tok"}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRegisterClientsSkipsProjectsWithoutClients(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()

	written, err := RegisterClients(dir, sampleEntry("http://127.0.0.1:1/mcp/a"))
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
	_ = os.Mkdir(filepath.Join(dir, ".claude"), 0o750)
	_ = os.Mkdir(filepath.Join(dir, ".vscode"), 0o750)

	written, err := RegisterClients(dir, sampleEntry("http://127.0.0.1:1/mcp/a"))
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

	if _, err := RegisterClients(dir, sampleEntry("http://127.0.0.1:1/mcp/a")); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, path)
	zeta, servers, other, ours, alpha := strings.Index(content, `"zeta"`), strings.Index(content, `"mcpServers"`),
		strings.Index(content, `"other"`), strings.Index(content, `"acme-dev-app"`), strings.Index(content, `"alpha"`)
	if zeta >= servers || servers >= other || other >= ours || ours >= alpha {
		t.Fatalf("keys reordered or lost:\n%s", content)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode changed to %v", info.Mode().Perm())
	}
}

func TestRegisterClientsRejectsJSONWithComments(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	path := filepath.Join(dir, ".vscode", "mcp.json")
	_ = os.Mkdir(filepath.Join(dir, ".vscode"), 0o750)
	original := "{\n  // my servers\n  \"servers\": {}\n}\n"
	_ = os.WriteFile(path, []byte(original), 0o600)

	_, err := RegisterClients(dir, sampleEntry("http://127.0.0.1:1/mcp/a"))
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
	_ = os.WriteFile(path, []byte(`{"mcpServers": {"other": {"command": "x"}}}`), 0o600)
	ours := sampleEntry("http://127.0.0.1:1/mcp/a")
	if _, err := RegisterClients(dir, ours); err != nil {
		t.Fatal(err)
	}

	changed, err := UnregisterClients(dir, ours)
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
	_ = os.Mkdir(filepath.Join(dir, ".claude"), 0o750)
	first, second := sampleEntry("http://127.0.0.1:1/mcp/a"), sampleEntry("http://127.0.0.1:2/mcp/b")
	_, _ = RegisterClients(dir, first)
	_, _ = RegisterClients(dir, second)

	changed, err := UnregisterClients(dir, first)
	if err != nil || len(changed) != 0 {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
	if content := readFile(t, filepath.Join(dir, ".mcp.json")); !strings.Contains(content, "127.0.0.1:2") {
		t.Fatalf("entry of the other session was removed:\n%s", content)
	}
}

func TestUnregisterClientsWithoutFiles(t *testing.T) {
	useIsolatedLocks(t)
	changed, err := UnregisterClients(t.TempDir(), sampleEntry("http://127.0.0.1:1/mcp/a"))
	if err != nil || len(changed) != 0 {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
}

func TestRegisterClientsConcurrentSessionsKeepBothEntries(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	_ = os.Mkdir(filepath.Join(dir, ".claude"), 0o750)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry := ClientEntry{Name: "acme-" + string(rune('a'+i)), URL: "http://127.0.0.1:1/mcp/x", Token: "t"}
			if _, err := RegisterClients(dir, entry); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	content := readFile(t, filepath.Join(dir, ".mcp.json"))
	for i := range 10 {
		if name := "acme-" + string(rune('a'+i)); !strings.Contains(content, `"`+name+`"`) {
			t.Fatalf("entry %s lost:\n%s", name, content)
		}
	}
}

func TestRegisterClientsKeepsIndentation(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	_ = os.WriteFile(path, []byte("{\n\t\"mcpServers\": {}\n}\n"), 0o600)

	if _, err := RegisterClients(dir, sampleEntry("http://127.0.0.1:1/mcp/a")); err != nil {
		t.Fatal(err)
	}
	if content := readFile(t, path); !strings.HasPrefix(content, "{\n\t\"mcpServers\": {\n\t\t\"acme-dev-app\"") {
		t.Fatalf("indentation not kept:\n%s", content)
	}
}

func TestRegisterClientsReplacesStaleEntryOfSameTarget(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	_ = os.Mkdir(filepath.Join(dir, ".claude"), 0o750)
	// A crashed session left its entry behind; the next session of the same target takes the name over.
	_, _ = RegisterClients(dir, sampleEntry("http://127.0.0.1:1/mcp/crashed"))

	if _, err := RegisterClients(dir, sampleEntry("http://127.0.0.1:2/mcp/new")); err != nil {
		t.Fatal(err)
	}
	content := readFile(t, filepath.Join(dir, ".mcp.json"))
	if strings.Contains(content, "crashed") || strings.Count(content, `"acme-dev-app"`) != 1 {
		t.Fatalf("stale entry not replaced:\n%s", content)
	}
}
