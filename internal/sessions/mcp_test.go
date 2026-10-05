package sessions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/sessions/sessionstest"
)

func TestManagerMCPServer(t *testing.T) {
	runner, launcher := sessionstest.NewRunner(), sessionstest.NewLauncher()
	runner.Direct("billing")
	server := mcp.NewServer(&mcp.Implementation{Name: "billing", Version: "1"}, nil)
	runner.WithMCP("billing", &sessionstest.MCPServer{S: server, Writes: true})
	runner.Direct("plain")
	m := sessions.NewManager(context.Background(), sessions.Config{Runner: sessions.Fixed(runner), Launch: launcher.Launch})
	t.Cleanup(m.Shutdown)

	id, err := m.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: "direct", Name: "billing", OpenedBy: "claude-code"}})
	if err != nil {
		t.Fatal(err)
	}
	access, err := m.MCPServer(id)
	if err != nil || access.Server != server || !access.Writes || access.ResourceType != "direct" {
		t.Fatalf("access = %+v, %v", access, err)
	}
	snapshot, err := m.Snapshot(t.Context())
	if err != nil || snapshot.Sessions[0].OpenedBy != "claude-code" {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
	if err = m.Log(id, "opened by claude-code"); err != nil {
		t.Fatal(err)
	}

	plain, err := m.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: "direct", Name: "plain"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.MCPServer(plain); !errors.Is(err, sessions.ErrNoMCPServer) {
		t.Fatalf("err = %v, want ErrNoMCPServer", err)
	}
	if _, err = m.MCPServer(999); !errors.Is(err, sessions.ErrUnknownSession) {
		t.Fatalf("err = %v, want ErrUnknownSession", err)
	}
	if err = m.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if _, err = m.MCPServer(id); !errors.Is(err, sessions.ErrSessionEnded) {
		t.Fatalf("err = %v, want ErrSessionEnded", err)
	}
}
