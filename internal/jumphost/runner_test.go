package jumphost

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/n2jsoft/airlock/internal/connections"
)

func TestPrepareWritesLockedForcesReadOnly(t *testing.T) {
	started := stubDirectMCP(t)
	runner := directRunner(t, &RunnerOptions{Containers: fakeContainers{"pg": pgContainer()}, WritesLocked: true})
	session, err := runner.Prepare(context.Background(), Options{ResourceType: "docker", Name: "pg", MCPAllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	if len(*started) != 1 || (*started)[0].AllowWrites {
		t.Fatalf("started = %+v; want one read-only MCP server", *started)
	}
}

func TestSaveConnectionPrefsWritesLockedKeepsTheSavedWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "saved-connections.json")
	databases := connections.NewStore(path).Databases()
	if err := databases.Put("", billingDatabase()); err != nil {
		t.Fatal(err)
	}
	options := directOptions("", false)
	options.MCPAllowWrites = true
	if err := NewRunner(&RunnerOptions{PrefsPath: path, Databases: databases}).SaveConnectionPrefs(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	locked := NewRunner(&RunnerOptions{PrefsPath: path, Databases: databases, WritesLocked: true})
	options.MCPAllowWrites = false
	if err := locked.SaveConnectionPrefs(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if prefs, ok := locked.LoadConnectionPrefs(context.Background(), options); !ok || !prefs.MCPAllowWrites {
		t.Fatalf("prefs = %+v, %v; a lock ignores the saved writes, it does not delete them", prefs, ok)
	}
}
