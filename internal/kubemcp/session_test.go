package kubemcp

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/mcpserve"
)

func TestStartWithoutCAServesNothing(t *testing.T) {
	handle, err := Start(mcpserve.NewHost(), &SessionConfig{Target: "platform", Profile: "dev"})
	if handle != nil || err != nil {
		t.Fatalf("Start = %v, %v; want nil, nil", handle, err)
	}
}

func TestStartRegistersReadOnlyServer(t *testing.T) {
	t.Cleanup(mcpserve.IsolateLocks(t.TempDir()))
	api := httptest.NewTLSServer(newFakeAPI())
	defer api.Close()
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".claude"), 0o750); err != nil {
		t.Fatal(err)
	}

	handle, err := Start(mcpserve.NewHost(), &SessionConfig{
		AWS: staticTokenAWS(), ProjectDir: project, Profile: "dev", Region: "eu-west-3", Target: "platform",
		EndpointHost: "example.com", ClusterCA: caOf(api), LocalPort: 15443, ServerPrefix: "acme",
	})
	if err != nil {
		t.Fatal(err)
	}
	if handle.AllowsWrites() || !strings.HasPrefix(handle.Display(), "read-only · acme-dev-platform-eks -> http://127.0.0.1:") {
		t.Fatalf("display = %q, writes = %v", handle.Display(), handle.AllowsWrites())
	}
	raw, err := os.ReadFile(filepath.Join(project, ".mcp.json")) // #nosec G304 -- test file.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"acme-dev-platform-eks"`) {
		t.Fatalf(".mcp.json = %s", raw)
	}
	if msg := handle.Close(); !strings.Contains(msg, "stopped and removed from .mcp.json") {
		t.Fatalf("close = %q", msg)
	}
}

func TestStartRejectsAnInvalidCA(t *testing.T) {
	_, err := Start(mcpserve.NewHost(), &SessionConfig{Target: "platform", ClusterCA: "LS0tLS1CRUdJTg==", LocalPort: 1})
	if err == nil || !strings.Contains(err.Error(), "certificate authority") {
		t.Fatalf("err = %v", err)
	}
}
