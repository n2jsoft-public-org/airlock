package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenSavedConnectionsMovesTheOldFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv(noUpdateCheckEnv, "1")
	dir := filepath.Join(root, "airlock")
	writeTestFile(t, filepath.Join(dir, "databases.json"),
		`{"version": 1, "databases": {"legacy-billing": {"engine": "sqlserver", "host": "billing.db.internal", "user": "mcp_ro"}}}`)
	writeTestFile(t, filepath.Join(dir, "connections.json"), `{"version": 2, "connections": {}}`)

	var warnings strings.Builder
	store, err := openSavedConnections(&warnings)
	if err != nil {
		t.Fatal(err)
	}
	if store.Path() != filepath.Join(dir, "saved-connections.json") {
		t.Fatalf("path = %s", store.Path())
	}
	if !strings.Contains(warnings.String(), "Moved databases.json and connections.json into "+store.Path()) {
		t.Fatalf("warnings = %q, want the migration notice", warnings.String())
	}
	if _, found, err := store.Databases().Get("legacy-billing"); err != nil || !found {
		t.Fatalf("legacy-billing found %v, err %v; want it moved", found, err)
	}
	for _, old := range []string{"databases.json", "connections.json"} {
		if _, err := os.Stat(filepath.Join(dir, old+".bak")); err != nil {
			t.Fatalf("%s.bak: %v", old, err)
		}
	}

	warnings.Reset()
	if _, err := openSavedConnections(&warnings); err != nil || warnings.Len() != 0 {
		t.Fatalf("second open: warnings %q, err %v; want nothing", warnings.String(), err)
	}
}

func TestOpenSavedConnectionsReportsAnUnreadableOldFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeTestFile(t, filepath.Join(root, "airlock", "connections.json"), "{not json")
	if _, err := openSavedConnections(&strings.Builder{}); err == nil || !strings.Contains(err.Error(), "connections.json") {
		t.Fatalf("err = %v, want the unreadable file named", err)
	}
}
