package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/config"
)

func TestRunnerSourceReloadsChangedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"mcpServerPrefix": "one"}`)
	var built []string
	source := newRunnerSource(path, func(cfg config.Config) (sessionsRunner, error) {
		built = append(built, cfg.MCPServerPrefix)
		return nil, nil
	})
	if _, err := source.Current(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Current(); err != nil || len(built) != 1 {
		t.Fatalf("an unchanged file must not rebuild: %v, %v", built, err)
	}
	later := time.Now().Add(time.Second)
	writeTestFile(t, path, `{"mcpServerPrefix": "two"}`)
	setModTime(t, path, later)
	if _, err := source.Current(); err != nil || len(built) != 2 || built[1] != "two" {
		t.Fatalf("built = %v, err = %v", built, err)
	}
	writeTestFile(t, path, `{"mcpServerPrefix": `)
	setModTime(t, path, later.Add(time.Second))
	if _, err := source.Current(); err == nil {
		t.Fatal("an invalid file fails the opening")
	}
	writeTestFile(t, path, `{"mcpServerPrefix": "two"}`)
	setModTime(t, path, later.Add(2*time.Second))
	if _, err := source.Current(); err != nil {
		t.Fatalf("a fixed file applies again: %v", err)
	}
}

func setModTime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}
