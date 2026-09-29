package appdir

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- test file.
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestMigrateCopiesConnectionsAndLogs(t *testing.T) {
	root := t.TempDir()
	legacy, target := filepath.Join(root, "jumphost"), filepath.Join(root, "airlock")
	writeFile(t, filepath.Join(legacy, "connections.json"), `{"v":1}`)
	writeFile(t, filepath.Join(legacy, "logs", "mcp-queries-2026-09-29.jsonl"), "{}\n")
	writeFile(t, filepath.Join(legacy, "connections.json.bak"), "old")

	migrated, err := migrateFrom(legacy, target)
	if err != nil || !migrated {
		t.Fatalf("migrateFrom = %v, %v", migrated, err)
	}
	if got := readFile(t, filepath.Join(target, "connections.json")); got != `{"v":1}` {
		t.Fatalf("connections.json = %q", got)
	}
	if got := readFile(t, filepath.Join(target, "logs", "mcp-queries-2026-09-29.jsonl")); got != "{}\n" {
		t.Fatalf("log = %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, "connections.json.bak")); !os.IsNotExist(err) {
		t.Fatal("only connections.json and logs/ are migrated")
	}
	if got := readFile(t, filepath.Join(legacy, "connections.json")); got != `{"v":1}` {
		t.Fatal("the legacy directory must be left untouched")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(target, "connections.json"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("connections.json mode = %v, %v; want 0600", info.Mode().Perm(), err)
		}
		dirInfo, err := os.Stat(filepath.Join(target, "logs"))
		if err != nil || dirInfo.Mode().Perm() != 0o700 {
			t.Fatalf("logs mode = %v, %v; want 0700", dirInfo.Mode().Perm(), err)
		}
	}
}

func TestMigrateSkipsWhenTargetExists(t *testing.T) {
	root := t.TempDir()
	legacy, target := filepath.Join(root, "jumphost"), filepath.Join(root, "airlock")
	writeFile(t, filepath.Join(legacy, "connections.json"), `{"v":1}`)
	writeFile(t, filepath.Join(target, "logs", "x.jsonl"), "keep")

	migrated, err := migrateFrom(legacy, target)
	if err != nil || migrated {
		t.Fatalf("migrateFrom = %v, %v; want skipped", migrated, err)
	}
	if _, err := os.Stat(filepath.Join(target, "connections.json")); !os.IsNotExist(err) {
		t.Fatal("an existing airlock directory must never receive legacy files")
	}
}

func TestMigrateSkipsWithoutLegacyDir(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "airlock")
	migrated, err := migrateFrom(filepath.Join(root, "jumphost"), target)
	if err != nil || migrated {
		t.Fatalf("migrateFrom = %v, %v; want nothing to do", migrated, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("no airlock directory is created when there is nothing to migrate")
	}
}

func TestMigrateLegacyUsesSiblingJumphostDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	writeFile(t, filepath.Join(root, "jumphost", "connections.json"), "{}")

	migrated, err := MigrateLegacy()
	if err != nil || !migrated {
		t.Fatalf("MigrateLegacy = %v, %v", migrated, err)
	}
	if got := readFile(t, filepath.Join(root, "airlock", "connections.json")); got != "{}" {
		t.Fatalf("connections.json = %q", got)
	}
}

func TestMigrateRunsWhenTargetHoldsOnlyConfig(t *testing.T) {
	root := t.TempDir()
	legacy, target := filepath.Join(root, "jumphost"), filepath.Join(root, "airlock")
	writeFile(t, filepath.Join(legacy, "connections.json"), `{"v":1}`)
	writeFile(t, filepath.Join(legacy, "logs", "a.jsonl"), "{}\n")
	// The README has users write config.json before the first run, which creates the directory.
	writeFile(t, filepath.Join(target, "config.json"), `{"mcpServerPrefix":"acme"}`)

	migrated, err := migrateFrom(legacy, target)
	if err != nil || !migrated {
		t.Fatalf("migrateFrom = %v, %v; want the connections and logs copied next to config.json", migrated, err)
	}
	if got := readFile(t, filepath.Join(target, "connections.json")); got != `{"v":1}` {
		t.Fatalf("connections.json = %q", got)
	}
	if got := readFile(t, filepath.Join(target, "logs", "a.jsonl")); got != "{}\n" {
		t.Fatalf("log = %q", got)
	}
	if got := readFile(t, filepath.Join(target, "config.json")); got != `{"mcpServerPrefix":"acme"}` {
		t.Fatalf("config.json must be kept, got %q", got)
	}
}

func TestMigrateReportsNothingWhenLegacyDirIsEmpty(t *testing.T) {
	root := t.TempDir()
	legacy, target := filepath.Join(root, "jumphost"), filepath.Join(root, "airlock")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	migrated, err := migrateFrom(legacy, target)
	if err != nil || migrated {
		t.Fatalf("migrateFrom = %v, %v; want nothing reported for an empty legacy directory", migrated, err)
	}
}
