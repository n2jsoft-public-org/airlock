package mcpserve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// testServer is an MCP server with a single ping tool.
func testServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "ping"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
		return nil, struct{}{}, nil
	})
	return server
}

func mountTestServer(t *testing.T, host *Host, slug string) Endpoint {
	t.Helper()
	endpoint, err := host.Mount(slug, testServer())
	if err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func toolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestServeRegistersAndCloseCleansUp(t *testing.T) {
	useIsolatedLocks(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleaned := 0
	handle, err := Serve(NewHost(), &ServeConfig{
		Server: testServer(), Recorder: NewRecorder(""), Name: "acme-dev-platform", ProjectDir: dir,
		Cleanup: func() error { cleaned++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(handle.Display(), "read-only · acme-dev-platform -> http://127.0.0.1:") || !strings.HasSuffix(handle.Display(), "(.mcp.json)") {
		t.Fatalf("display = %q", handle.Display())
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, ".mcp.json")), "acme-dev-platform") {
		t.Fatal(".mcp.json lacks the server")
	}
	if msg := handle.Close(); msg != `MCP server "acme-dev-platform" stopped and removed from .mcp.json` {
		t.Fatalf("close message = %q", msg)
	}
	if again := handle.Close(); again != `MCP server "acme-dev-platform" stopped and removed from .mcp.json` || cleaned != 1 {
		t.Fatalf("second close = %q, cleanups = %d", again, cleaned)
	}
	if strings.Contains(readFile(t, filepath.Join(dir, ".mcp.json")), "acme-dev-platform") {
		t.Fatal(".mcp.json still lists the server")
	}
}

func TestServeWithoutClientConfigShowsToken(t *testing.T) {
	useIsolatedLocks(t)
	handle, err := Serve(NewHost(), &ServeConfig{Server: testServer(), Recorder: NewRecorder(""), Name: "acme-dev-cache", ProjectDir: t.TempDir(), AllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if !strings.HasPrefix(handle.Display(), "WRITES ENABLED · acme-dev-cache") || !strings.Contains(handle.Display(), "Authorization: Bearer ") {
		t.Fatalf("display = %q", handle.Display())
	}
	if !handle.AllowsWrites() {
		t.Fatal("AllowsWrites() = false")
	}
	var _ Session = handle
}

func TestServeReportsCleanupErrors(t *testing.T) {
	useIsolatedLocks(t)
	handle, err := Serve(NewHost(), &ServeConfig{Server: testServer(), Recorder: NewRecorder(""), Name: "acme-dev-x", ProjectDir: t.TempDir(),
		Cleanup: func() error { return os.ErrClosed }})
	if err != nil {
		t.Fatal(err)
	}
	if msg := handle.Close(); !strings.HasPrefix(msg, `Warning: MCP server "acme-dev-x" stopped with errors:`) {
		t.Fatalf("close message = %q", msg)
	}
}
