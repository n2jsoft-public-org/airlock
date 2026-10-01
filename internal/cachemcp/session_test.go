package cachemcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/mcpserve"
)

func TestStartSkipsMemcached(t *testing.T) {
	handle, err := Start(mcpserve.NewHost(), &SessionConfig{Engine: "memcached", Target: "legacy"})
	if handle != nil || err != nil {
		t.Fatalf("Start = %v, %v; want nil, nil", handle, err)
	}
}

func TestStartRegistersAndSwitchesCredentials(t *testing.T) {
	t.Cleanup(mcpserve.IsolateLocks(t.TempDir()))
	m := startRedis(t)
	m.RequireUserAuth("app", "one")
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".vscode"), 0o750); err != nil {
		t.Fatal(err)
	}
	handle, err := Start(mcpserve.NewHost(), &SessionConfig{
		Credentials: PasswordCredentials("app", "one"), ProjectDir: project, Profile: "dev", Region: "eu-west-3",
		Target: "sessions", Engine: "redis", LocalPort: portOf(t, m), ServerPrefix: "acme", AllowWrites: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if !handle.AllowsWrites() || !strings.HasPrefix(handle.Display(), "WRITES ENABLED · acme-dev-sessions-cache -> http://127.0.0.1:") ||
		!strings.Contains(handle.Display(), filepath.Join(".vscode", "mcp.json")) {
		t.Fatalf("display = %q", handle.Display())
	}
	m.RequireUserAuth("app", "two")
	if err := handle.UseCredentials(PasswordCredentials("app", "two")); err != nil {
		t.Fatal(err)
	}
}

func TestStartCallsOnRejectedOnce(t *testing.T) {
	t.Cleanup(mcpserve.IsolateLocks(t.TempDir()))
	m := startRedis(t)
	m.RequireUserAuth("app", "good")
	rejected := 0
	handle, err := Start(mcpserve.NewHost(), &SessionConfig{
		Credentials: PasswordCredentials("app", "bad"), OnRejected: func() { rejected++ },
		ProjectDir: t.TempDir(), Target: "sessions", Engine: "valkey", LocalPort: portOf(t, m),
	})
	if err != nil {
		t.Fatal(err)
	}
	if handle == nil {
		t.Fatal("no MCP server started for valkey")
	}
	defer handle.Close()
	for range 2 {
		if _, err := handle.cache.Get(t.Context(), &getKeyInput{Key: "k"}); err == nil {
			t.Fatal("Get with a rejected password succeeded")
		}
	}
	if rejected != 1 {
		t.Fatalf("OnRejected called %d times, want 1", rejected)
	}
}
