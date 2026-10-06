package mcpserve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAuditLogAppendsJSONLinesPerDay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	log := NewAuditLog(dir)
	at := time.Date(2026, 9, 29, 14, 5, 0, 0, time.Local)

	first := testBaseEvent()
	first.Time, first.Tool, first.Database, first.SQL, first.MaxRows = at, "query", "archive", "SELECT * FROM users", 200
	first.Rows, first.Duration = new(int64(3)), 1500*time.Microsecond
	second := testBaseEvent()
	second.Time, second.Tool, second.SQL, second.Error = at.Add(time.Minute), "query", "DELETE FROM users", "rejected"

	for _, e := range []ToolEvent{first, second} {
		if err := log.Write(&e); err != nil {
			t.Fatal(err)
		}
	}

	path := filepath.Join(dir, "mcp-queries-2026-09-29.jsonl")
	lines := strings.Split(strings.TrimSuffix(readFile(t, path), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("log has %d lines, want 2:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"server": "acme-dev-app", "profile": "dev", "region": "eu-west-3", "target": "app", "engine": "postgres",
		"access": "read-only", "tool": "query", "database": "archive", "sql": "SELECT * FROM users",
		"max_rows": 200.0, "rows": 3.0, "duration_ms": 1.5,
	}
	for key, value := range want {
		if record[key] != value {
			t.Errorf("%s = %v, want %v", key, record[key], value)
		}
	}
	if at, ok := record["time"].(string); !ok {
		t.Errorf("time = %v, want a string", record["time"])
	} else if _, err := time.Parse(time.RFC3339Nano, at); err != nil {
		t.Errorf("time = %v: %v", record["time"], err)
	}
	if !strings.Contains(lines[1], `"error":"rejected"`) || strings.Contains(lines[1], `"rows"`) {
		t.Errorf("second line = %s", lines[1])
	}

	if runtime.GOOS != "windows" {
		assertMode(t, dir, 0o700)
		assertMode(t, path, 0o600)
	}
}

func TestAuditLogDir(t *testing.T) {
	if got := NewAuditLog("/tmp/logs").Dir(); got != "/tmp/logs" {
		t.Fatalf("Dir() = %q", got)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %o, want %o", path, got, want)
	}
}
