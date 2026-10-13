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
	path := writeConfig(t, `{"mcpGateway": "false", "mcpGatewayPort": "8123", "agentAutoConnect": "NON-PROD",
		"agentWrites": "true", "agentIdleTimeout": "1h30m"}`)
	cfg, err := Load(path)
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
	cfg, err := Load(writeConfig(t, `{"daemonIdleTimeout": "8h"}`))
	if err != nil || cfg.DaemonIdleTimeout != 8*time.Hour {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
	for _, body := range []string{`{"daemonIdleTimeout": "30s"}`, `{"daemonIdleTimeout": "never"}`} {
		if _, err := Load(writeConfig(t, body)); err == nil || !strings.Contains(err.Error(), "daemonIdleTimeout") {
			t.Errorf("%s: err = %v, want one naming daemonIdleTimeout", body, err)
		}
	}
}

func TestGatewaySettingsRejectInvalidValues(t *testing.T) {
	cases := map[string]string{
		`{"mcpGateway": "yes"}`:         "mcpGateway",
		`{"mcpGatewayPort": "80"}`:      "mcpGatewayPort",
		`{"mcpGatewayPort": "70000"}`:   "mcpGatewayPort",
		`{"agentAutoConnect": "some"}`:  "agentAutoConnect",
		`{"agentWrites": "1"}`:          "agentWrites",
		`{"agentIdleTimeout": "30s"}`:   "agentIdleTimeout",
		`{"agentIdleTimeout": "never"}`: "agentIdleTimeout",
	}
	for body, key := range cases {
		if _, err := Load(writeConfig(t, body)); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: err = %v, want one naming %s", body, err, key)
		}
	}
}

func TestGatewaySettingsRender(t *testing.T) {
	settings, err := Settings(writeConfig(t, `{"agentIdleTimeout": "2h"}`))
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, s := range settings {
		values[s.Name] = s.Value
	}
	want := map[string]string{"mcpGateway": "true", "mcpGatewayPort": "7821", "agentAutoConnect": "none",
		"agentWrites": "false", "agentIdleTimeout": "2h"}
	for key, value := range want {
		if values[key] != value {
			t.Errorf("%s = %q, want %q", key, values[key], value)
		}
	}
}

func TestSetValidatesGatewaySettings(t *testing.T) {
	path := writeConfig(t, `{}`)
	if err := Set(path, "agentAutoConnect", "all"); err != nil {
		t.Fatal(err)
	}
	if err := Set(path, "agentIdleTimeout", "10s"); err == nil {
		t.Fatal("a timeout under 1m must be refused")
	}
	cfg, err := Load(path)
	if err != nil || cfg.AgentAutoConnect != AutoConnectAll || cfg.AgentIdleTimeout != 30*time.Minute {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
}
