package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/jumphost"
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

func TestRunnerSourceLastKeepsTheValidRunner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"defaultDbUser": "one"}`)
	source := newRunnerSource(path, func(cfg config.Config) (*jumphost.Runner, error) {
		return jumphost.NewRunner(jumphost.RunnerOptions{DefaultDBUser: cfg.DefaultDBUser}), nil
	})
	if _, ok := source.Last(); ok {
		t.Fatal("Last reports a runner before the first opening")
	}
	first, err := source.Current()
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, `{"defaultDbUser": `)
	setModTime(t, path, time.Now().Add(time.Second))
	if _, err = source.Current(); err == nil {
		t.Fatal("an invalid file fails the opening")
	}
	if last, ok := source.Last(); !ok || last != first {
		t.Fatalf("Last = %p, %v; want the runner of the valid file", last, ok)
	}
}

func TestRunnerSourceNeverReturnsANilRunnerInterface(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"defaultDbUser": `)
	source := newRunnerSource(path, func(config.Config) (*jumphost.Runner, error) {
		return jumphost.NewRunner(jumphost.RunnerOptions{}), nil
	})
	runner, err := source.Source()()
	if err == nil || runner != nil {
		t.Fatalf("runner = %v, err = %v; want a nil interface and an error", runner, err)
	}
}
