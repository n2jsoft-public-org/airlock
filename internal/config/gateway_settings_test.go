package config

import (
	"strings"
	"testing"
	"time"
)

func TestGatewaySettingsDefaults(t *testing.T) {
	cfg := Defaults()
	if !cfg.MCPGateway || cfg.MCPGatewayPort != 7821 || cfg.AgentAutoConnect != AutoConnectNone ||
		cfg.AgentWrites || cfg.AgentIdleTimeout != 30*time.Minute {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestGatewaySettingsLoad(t *testing.T) {
	path := writeConfig(t, `{"mcp": {"gateway": {"enabled": false, "port": 8123}},
		"agent": {"autoConnect": "NON-PROD", "writes": "true", "idleTimeout": "1h30m"}}`)
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPGateway || cfg.MCPGatewayPort != 8123 || cfg.AgentAutoConnect != AutoConnectNonProd ||
		!cfg.AgentWrites || cfg.AgentIdleTimeout != 90*time.Minute {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestDaemonIdleTimeout(t *testing.T) {
	if got := Defaults().DaemonIdleTimeout; got != 10*time.Minute {
		t.Fatalf("default = %v, want 10m", got)
	}
	cfg, err := Load(writeConfig(t, `{"daemon": {"idleTimeout": "8h"}}`), nil)
	if err != nil || cfg.DaemonIdleTimeout != 8*time.Hour {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
	for _, body := range []string{`{"daemon": {"idleTimeout": "30s"}}`, `{"daemon": {"idleTimeout": "never"}}`} {
		if _, err := Load(writeConfig(t, body), nil); err == nil || !strings.Contains(err.Error(), "daemon.idleTimeout") {
			t.Errorf("%s: err = %v, want one naming daemon.idleTimeout", body, err)
		}
	}
}

func TestGatewaySettingsRejectInvalidValues(t *testing.T) {
	cases := map[string]string{
		`{"mcp": {"gateway": {"enabled": "yes"}}}`: "mcp.gateway.enabled",
		`{"mcp": {"gateway": {"port": 80}}}`:       "mcp.gateway.port",
		`{"mcp": {"gateway": {"port": "70000"}}}`:  "mcp.gateway.port",
		`{"agent": {"autoConnect": "some"}}`:       "agent.autoConnect",
		`{"agent": {"writes": 1}}`:                 "agent.writes",
		`{"agent": {"idleTimeout": "30s"}}`:        "agent.idleTimeout",
		`{"agentIdleTimeout": "never"}`:            "agent.idleTimeout",
	}
	for body, key := range cases {
		if _, err := Load(writeConfig(t, body), nil); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: err = %v, want one naming %s", body, err, key)
		}
	}
}

func TestGatewaySettingsRender(t *testing.T) {
	settings, err := Settings(writeConfig(t, `{"agent": {"idleTimeout": "2h"}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, s := range settings {
		values[s.Name] = s.Value
	}
	want := map[string]string{"mcp.gateway.enabled": "true", "mcp.gateway.port": "7821", "agent.autoConnect": "none",
		"agent.writes": "false", "agent.idleTimeout": "2h"}
	for key, value := range want {
		if values[key] != value {
			t.Errorf("%s = %q, want %q", key, values[key], value)
		}
	}
}

func TestSetValidatesGatewaySettings(t *testing.T) {
	path := writeConfig(t, `{}`)
	if err := Set(path, nil, "agent.autoConnect", "all"); err != nil {
		t.Fatal(err)
	}
	if err := Set(path, nil, "agent.idleTimeout", "10s"); err == nil {
		t.Fatal("a timeout under 1m must be refused")
	}
	cfg, err := Load(path, nil)
	if err != nil || cfg.AgentAutoConnect != AutoConnectAll || cfg.AgentIdleTimeout != 30*time.Minute {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
}
