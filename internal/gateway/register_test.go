package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/aiclient"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// isolateClients keeps the client files and their locks in temporary directories; it returns the
// directory of Claude Code's user file.
func isolateClients(t *testing.T) string {
	t.Helper()
	t.Cleanup(mcpserve.IsolateLocks(t.TempDir()))
	t.Cleanup(mcpserve.IsolateUserConfigDir(t.TempDir()))
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	return dir
}

func TestLoadTokenCreatesKeepsAndRepairs(t *testing.T) {
	dir := t.TempDir()
	token, created, err := loadToken(dir)
	if err != nil || !created || len(token) != 64 || strings.ToLower(token) != token {
		t.Fatalf("token = %q, created = %v, %v", token, created, err)
	}
	info, err := os.Stat(filepath.Join(dir, TokenFileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token file = %v, %v", info, err)
	}
	again, created, err := loadToken(dir)
	if err != nil || created || again != token {
		t.Fatalf("the token must survive restarts: %q %v %v", again, created, err)
	}
	if err = os.WriteFile(filepath.Join(dir, TokenFileName), []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repaired, created, err := loadToken(dir)
	if err != nil || !created || repaired == token || len(repaired) != 64 {
		t.Fatalf("a malformed token must be replaced: %q %v %v", repaired, created, err)
	}
}

func TestRegisterOnlyInUserFiles(t *testing.T) {
	claudeDir := isolateClients(t)
	sel, err := aiclient.Parse("auto,claude:user,codex")
	if err != nil {
		t.Fatal(err)
	}
	entry := mcpserve.ClientEntry{Name: "airlock", URL: URL(7821), Token: strings.Repeat("a", 64)}
	reg := register(sel, entry)
	if reg.err != nil || len(reg.written) != 1 || reg.written[0] != filepath.Join(claudeDir, ".claude.json") {
		t.Fatalf("written = %v, %v", reg.written, reg.err)
	}
	raw, err := os.ReadFile(filepath.Join(claudeDir, ".claude.json")) //nolint:gosec // a file of the test's temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Servers map[string]struct {
			Headers map[string]string `json:"headers"`
			URL     string            `json:"url"`
		} `json:"mcpServers"`
	}
	if err = json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if got := file.Servers["airlock"]; got.URL != "http://127.0.0.1:7821/mcp" || got.Headers["Authorization"] != "Bearer "+entry.Token {
		t.Fatalf("entry = %+v", got)
	}
	if !strings.Contains(reg.status(), "registered in") {
		t.Fatalf("status = %q", reg.status())
	}
	if err = reg.remove(); err != nil {
		t.Fatal(err)
	}
	if raw, err = os.ReadFile(filepath.Join(claudeDir, ".claude.json")); err != nil || strings.Contains(string(raw), `"airlock"`) { //nolint:gosec // a file of the test's temporary directory.
		t.Fatalf("the entry must be removed: %s", raw)
	}
}

func TestStatusWithoutUserClientShowsTheToken(t *testing.T) {
	isolateClients(t)
	token := strings.Repeat("b", 64)
	reg := register(aiclient.AutoDetect, mcpserve.ClientEntry{Name: "airlock", URL: URL(7821), Token: token})
	if status := reg.status(); !strings.Contains(status, "not registered") || !strings.Contains(status, token) {
		t.Fatalf("status = %q", status)
	}
	// M4: daemon.log gets the status without the token.
	if logged := reg.statusWithoutToken(); !strings.Contains(logged, "not registered") || strings.Contains(logged, token) {
		t.Fatalf("logged status = %q", logged)
	}
}
