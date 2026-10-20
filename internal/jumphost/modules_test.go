package jumphost

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareRefusesDisabledModule(t *testing.T) {
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, DisabledModules: []string{"sql", "cache"}})
	for _, resourceType := range []string{"rds", "direct", "docker", "docker-cache", "ElastiCache"} {
		if !runner.ModuleDisabled(resourceType) {
			t.Errorf("ModuleDisabled(%s) = false, want true", resourceType)
		}
		if _, err := runner.Prepare(context.Background(), Options{ResourceType: resourceType, Name: "x"}); err == nil || !strings.Contains(err.Error(), "enabled in config.json") {
			t.Errorf("Prepare(%s) error = %v, want the disabled module", resourceType, err)
		}
	}
	for _, resourceType := range []string{"eks", "dynamodb", "s3", "logs"} {
		if runner.ModuleDisabled(resourceType) {
			t.Errorf("ModuleDisabled(%s) = true, want false", resourceType)
		}
	}
}

func TestPrepareRefusesDisabledS3(t *testing.T) {
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, DisabledModules: []string{"s3"}})
	if _, err := runner.Prepare(context.Background(), Options{ResourceType: "s3"}); err == nil || !strings.Contains(err.Error(), "enabled in config.json") {
		t.Fatalf("Prepare(s3) error = %v, want the disabled module", err)
	}
	if runner.ModuleDisabled("dynamodb") {
		t.Fatal("disabling s3 must leave dynamodb on")
	}
}

func TestListResourceSummariesRefusesDisabledModule(t *testing.T) {
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, DisabledModules: []string{"k8s"}})
	if _, err := runner.ListResourceSummaries(context.Background(), &Options{ResourceType: "eks"}); err == nil || !strings.Contains(err.Error(), "enabled in config.json") {
		t.Fatalf("ListResourceSummaries(eks) error = %v, want the disabled module", err)
	}
	if _, err := runner.FindResource(context.Background(), &Options{ResourceType: "eks", Name: "x"}, ""); err == nil || !strings.Contains(err.Error(), "enabled in config.json") {
		t.Fatalf("FindResource(eks) error = %v, want the disabled module", err)
	}
}

func TestListSavedConnectionsLeavesOutDisabledModules(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	path := filepath.Join(t.TempDir(), "saved-connections.json")
	direct := `{"engine": "sqlserver", "host": "billing.db.internal", "user": "mcp_ro", "used_at": "2026-10-01T00:00:00Z", "mcp_allow_writes": false}`
	if err := os.WriteFile(path, []byte(`{"version": 1, "connections": {"billing": `+direct+`}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	count := func(disabled ...string) int {
		t.Helper()
		runner := NewRunner(&RunnerOptions{PrefsPath: path, DisabledModules: disabled})
		saved, err := runner.ListSavedConnections(context.Background(), "", "")
		if err != nil {
			t.Fatal(err)
		}
		return len(saved)
	}
	if got := count("s3"); got != 1 {
		t.Fatalf("with s3 disabled, %d saved connections, want the direct one", got)
	}
	if got := count("sql"); got != 0 {
		t.Fatalf("with sql disabled, %d saved connections, want none", got)
	}
}
