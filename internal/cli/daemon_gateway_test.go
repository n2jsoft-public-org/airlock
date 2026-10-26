package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/config"
	"github.com/n2jsoft-public-org/airlock/internal/daemon"
	"github.com/n2jsoft-public-org/airlock/internal/gateway"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
	"github.com/n2jsoft-public-org/airlock/internal/sessions"
	"github.com/n2jsoft-public-org/airlock/internal/sessions/sessionstest"
)

func TestGatewaySettingsFromConfig(t *testing.T) {
	cfg := config.Defaults()
	cfg.Environments, cfg.AgentAutoConnect, cfg.AgentWrites, cfg.AgentIdleTimeout = "prod!=*prod*", config.AutoConnectNonProd, true, time.Hour
	s := gatewaySettings(&cfg)
	if s.AutoConnect != gateway.AutoConnectNonProd || !s.AgentWrites || s.IdleTimeout != time.Hour || !s.Environments.Match("team-prod").Production {
		t.Fatalf("settings = %+v", s)
	}
}

func testSource(path string) *runnerSource[*daemonRunner] {
	return newRunnerSource(path, nil, func(cfg config.Config, _ *config.Organization) (*daemonRunner, error) {
		return &daemonRunner{cfg: cfg}, nil
	})
}

func gatewayEnv(t *testing.T) daemon.GatewayEnv {
	t.Helper()
	m := sessions.NewManager(context.Background(), sessions.Config{Runner: sessions.Fixed(sessionstest.NewRunner()), Launch: sessionstest.NewLauncher().Launch})
	t.Cleanup(m.Shutdown)
	return daemon.GatewayEnv{Manager: m, Activity: func() {}, Log: func(string, ...any) {}}
}

func TestStartGatewayFuncTurnedOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"mcpGateway": "false"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := startGatewayFunc(path, t.TempDir(), "", testSource(path), nil)(t.Context(), gatewayEnv(t))
	if err == nil || !strings.Contains(err.Error(), "mcp.gateway.enabled") {
		t.Fatalf("err = %v", err)
	}
}

func TestStartGatewayFuncServesOnTheConfiguredPort(t *testing.T) {
	t.Cleanup(mcpserve.IsolateLocks(t.TempDir()))
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := free.Addr().(*net.TCPAddr).Port //nolint:errcheck // a TCP listener.
	_ = free.Close()                        //nolint:errcheck // a probe listener.
	path := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(path, []byte(fmt.Sprintf(`{"mcpGatewayPort": "%d", "mcpClients": "none"}`, port)), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := startGatewayFunc(path, t.TempDir(), "", testSource(path), nil)(t.Context(), gatewayEnv(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }() //nolint:errcheck // a test teardown.
	if status := g.Status(); !strings.Contains(status, fmt.Sprintf("127.0.0.1:%d/mcp", port)) || !strings.Contains(status, "not registered") {
		t.Fatalf("status = %q", status)
	}
}

// F5: a dashboard tells why the gateway did not start, unless the user turned it off.
func TestGatewayNotice(t *testing.T) {
	cases := map[string]string{
		"off: cannot listen on 127.0.0.1:7821 (set mcp.gateway.port to another port): address already in use": "MCP gateway off: cannot listen on 127.0.0.1:7821 (set mcp.gateway.port to another port): address already in use — see `airlock daemon status`",
		"off: " + gatewayTurnedOff:        "",
		"off":                             "",
		"":                                "",
		"on http://127.0.0.1:7821/mcp":    "",
		"off: failed to read config.json": "MCP gateway off: failed to read config.json — see `airlock daemon status`",
	}
	for gatewayStatus, want := range cases {
		if got := gatewayNotice(&daemon.Status{Gateway: gatewayStatus}); got != want {
			t.Errorf("gatewayNotice(%q) = %q, want %q", gatewayStatus, got, want)
		}
	}
}
