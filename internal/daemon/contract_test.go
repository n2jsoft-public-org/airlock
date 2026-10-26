package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/sessions"
	"github.com/n2jsoft-public-org/airlock/internal/sessions/sessionstest"
)

// shortSocket returns a socket path short enough for macOS (t.TempDir paths are too long).
func shortSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "al")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ignore(os.RemoveAll(dir)) })
	return filepath.Join(dir, "d.sock")
}

// serve runs a server over service on a fresh socket and returns a connected client.
func serve(t *testing.T, service sessions.Service, hooks ServerHooks) (*Server, *Client, string) {
	t.Helper()
	socket := shortSocket(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(service, "v-test", hooks)
	go func() { ignore(server.Serve(listener)) }()
	t.Cleanup(func() { server.Close("test over") })
	client, err := Dial(t.Context(), socket, "v-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ignore(client.Close()) })
	return server, client, socket
}

func TestClientServerContract(t *testing.T) {
	sessionstest.RunContract(t, func(t *testing.T, runner *sessionstest.Runner, launcher *sessionstest.Launcher) sessions.Service {
		m := sessions.NewManager(context.Background(), sessions.Config{Runner: sessions.Fixed(runner), Launch: launcher.Launch})
		t.Cleanup(m.Shutdown)
		_, client, _ := serve(t, m, ServerHooks{})
		return client
	})
}

// shortDir returns a directory short enough for a socket on macOS.
func shortDir(t *testing.T) string {
	t.Helper()
	return filepath.Dir(shortSocket(t))
}
