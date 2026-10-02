package dbmcp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/mcpserve"
)

func testDirectConfig(t *testing.T, engine Engine) *DirectConfig {
	t.Helper()
	return &DirectConfig{
		ProjectDir: t.TempDir(), ServerPrefix: "acme", Name: "legacy-billing", Engine: engine,
		Address: Address{Host: "billing.db.internal", Port: 1433, TLS: TLSRequire},
		User:    "mcp_ro", Password: "s3cret",
	}
}

func TestStartDirectRegistersTheServer(t *testing.T) {
	useIsolatedLocks(t)
	cfg := testDirectConfig(t, EngineSQLServer)
	if err := os.Mkdir(filepath.Join(cfg.ProjectDir, ".claude"), 0o750); err != nil {
		t.Fatal(err)
	}

	handle, err := StartDirect(mcpserve.NewHost(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(handle.Display(), "read-only · acme-legacy-billing -> http://127.0.0.1:") || !strings.HasSuffix(handle.Display(), "(.mcp.json)") {
		t.Fatalf("display = %q", handle.Display())
	}
	if config := readFile(t, filepath.Join(cfg.ProjectDir, ".mcp.json")); !strings.Contains(config, `"acme-legacy-billing"`) || strings.Contains(config, "s3cret") {
		t.Fatalf(".mcp.json = %s", config)
	}

	instructions := connectEndpoint(t, handle.Endpoint()).InitializeResult().Instructions
	if !strings.HasPrefix(instructions, "Database legacy-billing (T-SQL (SQL Server) dialect), reached directly at billing.db.internal:1433. Access is read-only.") {
		t.Fatalf("instructions = %q", instructions)
	}

	if message := handle.Close(); message != `MCP server "acme-legacy-billing" stopped and removed from .mcp.json` {
		t.Fatalf("close message = %q", message)
	}
}

func TestStartDirectWritesExposeExecute(t *testing.T) {
	useIsolatedLocks(t)
	cfg := testDirectConfig(t, EnginePostgres)
	cfg.AllowWrites = true
	handle, err := StartDirect(mcpserve.NewHost(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if !strings.HasPrefix(handle.Display(), "WRITES ENABLED · acme-legacy-billing -> ") || !handle.AllowsWrites() {
		t.Fatalf("display = %q, allows writes = %v", handle.Display(), handle.AllowsWrites())
	}
	if tools := strings.Join(toolNames(t, connectEndpoint(t, handle.Endpoint())), ","); !strings.Contains(tools, "execute") {
		t.Fatalf("tools = %s, want execute", tools)
	}
}

func TestStartDirectRecordsEventsWithoutAWSFields(t *testing.T) {
	useIsolatedLocks(t)
	cfg := testDirectConfig(t, EngineSQLServer)
	cfg.LogDir = filepath.Join(t.TempDir(), "logs")
	handle, err := StartDirect(mcpserve.NewHost(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()

	events := make(chan mcpserve.ToolEvent, 1)
	handle.Subscribe(func(e mcpserve.ToolEvent) { events <- e })
	// The read-only guard rejects the statement before any connection is attempted.
	if result := call(t, connectEndpoint(t, handle.Endpoint()), "query", map[string]any{"sql": "DELETE FROM invoices"}); !result.IsError {
		t.Fatalf("DELETE must be rejected, got %s", resultText(result))
	}
	var e mcpserve.ToolEvent
	select {
	case e = <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("no query event recorded")
	}
	if e.Server != "acme-legacy-billing" || e.Target != "legacy-billing" || e.Profile != "" || e.Region != "" ||
		e.Engine != "sqlserver" || e.Access != "read-only" || e.SQL != "DELETE FROM invoices" {
		t.Fatalf("event = %+v", e)
	}
}

func TestStartDirectSignsInWithThePassword(t *testing.T) {
	useIsolatedLocks(t)
	called := false
	cfg := testDirectConfig(t, EnginePostgres)
	cfg.OnPasswordRejected = func() { called = true }
	handle, err := StartDirect(mcpserve.NewHost(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()

	provider, ok := handle.creds.get().(*passwordProvider)
	if !ok {
		t.Fatalf("provider = %T, want the password", handle.creds.get())
	}
	if provider.user != "mcp_ro" || provider.password != "s3cret" || provider.defaultDatabase != "postgres" {
		t.Fatalf("provider = %+v", provider)
	}
	provider.rejected()
	if !called {
		t.Fatal("OnPasswordRejected was not wired to the password provider")
	}
	if handle.querier.addr != cfg.Address {
		t.Fatalf("querier address = %+v, want %+v", handle.querier.addr, cfg.Address)
	}
}

func TestStartDirectRefusesUnsupportedEngines(t *testing.T) {
	handle, err := StartDirect(mcpserve.NewHost(), testDirectConfig(t, "mysql"))
	if handle != nil || !errors.Is(err, ErrEngineUnsupported) {
		t.Fatalf("handle = %v, err = %v; want nil, ErrEngineUnsupported", handle, err)
	}
}
