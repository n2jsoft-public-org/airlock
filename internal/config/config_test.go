package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/awstag"
	"github.com/n2jsoft/airlock/internal/dbuser"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaults(t *testing.T) {
	d := Defaults()
	if d.JumphostTag != (awstag.Tag{Key: "Usage", Value: "jumphost"}) || d.MCPServerPrefix != "airlock" {
		t.Fatalf("Defaults = %+v", d)
	}
	if !d.DefaultDBUser.IsZero() || !d.ReadOnlySecretTag.IsZero() || d.DefaultRegion != "" {
		t.Fatalf("Defaults must leave the user, secret tag and region unset: %+v", d)
	}
}

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.json"), nil)
	if err != nil || !reflect.DeepEqual(cfg, Defaults()) {
		t.Fatalf("Load(missing) = %+v, %v", cfg, err)
	}
}

func TestLoadEmptyPathGivesDefaults(t *testing.T) {
	cfg, err := Load("", nil)
	if err != nil || !reflect.DeepEqual(cfg, Defaults()) {
		t.Fatalf("Load(\"\") = %+v, %v", cfg, err)
	}
}

func TestLoadACMEPreset(t *testing.T) {
	path := writeConfig(t, `{
  "defaultRegion": "eu-west-3",
  "defaultDbUser": "acme_ro",
  "readOnlySecretTag": "AccessRole=ro-access",
  "mcpServerPrefix": "acme"
}`)
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		JumphostTag:       awstag.Tag{Key: "Usage", Value: "jumphost"},
		DefaultDBUser:     dbuser.Defaults{IAM: "acme_ro"},
		ReadOnlySecretTag: awstag.Tag{Key: "AccessRole", Value: "ro-access"},
		MCPServerPrefix:   "acme",
		DefaultRegion:     "eu-west-3",
		Environments:      "prod!=",
		MCPClients:        "auto",
		Mode:              ModeCLI,
		DaemonIdleTimeout: 10 * time.Minute,
		MCPGateway:        true,
		MCPGatewayPort:    DefaultGatewayPort,
		AgentAutoConnect:  AutoConnectNone,
		AgentIdleTimeout:  30 * time.Minute,
		RandomLocalPort:   true,
		Language:          LanguageAuto,
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("Load = %+v, want %+v", cfg, want)
	}
}

func TestLoadDefaultDBUserPerEngine(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{"defaultDbUser": "app_ro,SQLServer=mcp_ro"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultDBUser != (dbuser.Defaults{IAM: "app_ro", SQLServer: "mcp_ro"}) {
		t.Fatalf("DefaultDBUser = %+v", cfg.DefaultDBUser)
	}
	values := cfg.values()
	if values["modules.sql.defaultDbUser"] != "app_ro,sqlserver=mcp_ro" {
		t.Fatalf("defaultDbUser value = %q, want the normalized form", values["modules.sql.defaultDbUser"])
	}
}

func TestLoadRejectsMalformedDefaultDBUser(t *testing.T) {
	path := writeConfig(t, `{"defaultDbUser": "oracle=ora_ro"}`)
	_, err := Load(path, nil)
	if err == nil || !strings.Contains(err.Error(), "defaultDbUser") || !strings.Contains(err.Error(), path) {
		t.Fatalf("Load(malformed defaultDbUser) error = %v", err)
	}
}

func TestLoadOverridesJumphostTag(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{"jumphostTag": "Role=bastion"}`), nil)
	if err != nil || cfg.JumphostTag != (awstag.Tag{Key: "Role", Value: "bastion"}) {
		t.Fatalf("Load = %+v, %v", cfg, err)
	}
}

func TestLoadRejectsInvalidJSON(t *testing.T) {
	path := writeConfig(t, `{"defaultRegion": "eu-west-3",}`)
	_, err := Load(path, nil)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("Load(invalid) error = %v, want one naming %s", err, path)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	// encoding/json matches keys case-insensitively, so a typo means a key that does not exist.
	path := writeConfig(t, `{"dbUser": "acme_ro"}`)
	_, err := Load(path, nil)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "dbUser") {
		t.Fatalf("Load(unknown key) error = %v, want one naming the file and the key", err)
	}
}

func TestLoadRejectsMalformedTag(t *testing.T) {
	path := writeConfig(t, `{"readOnlySecretTag": "ro-access"}`)
	_, err := Load(path, nil)
	if err == nil || !strings.Contains(err.Error(), "readOnlySecretTag") || !strings.Contains(err.Error(), path) {
		t.Fatalf("Load(malformed tag) error = %v", err)
	}
}

func TestApplyFlagsWinOverFile(t *testing.T) {
	cfg := Config{JumphostTag: awstag.Tag{Key: "Usage", Value: "jumphost"}, MCPServerPrefix: "acme",
		ReadOnlySecretTag: awstag.Tag{Key: "A", Value: "b"}}
	got, err := cfg.Apply(Overrides{
		JumphostTag:       new("Role=bastion"),
		ReadOnlySecretTag: new(""),
		MCPServerPrefix:   new("team"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.JumphostTag != (awstag.Tag{Key: "Role", Value: "bastion"}) || got.MCPServerPrefix != "team" || !got.ReadOnlySecretTag.IsZero() {
		t.Fatalf("Apply = %+v", got)
	}
}

func TestApplyNilKeepsFile(t *testing.T) {
	cfg := Defaults()
	cfg.MCPServerPrefix = "acme"
	got, err := cfg.Apply(Overrides{})
	if err != nil || !reflect.DeepEqual(got, cfg) {
		t.Fatalf("Apply(no overrides) = %+v, %v", got, err)
	}
}

func TestApplyRejectsMalformedFlag(t *testing.T) {
	d := Defaults()
	_, err := d.Apply(Overrides{JumphostTag: new("Usage")})
	if err == nil || !strings.Contains(err.Error(), "--jumphost-tag") {
		t.Fatalf("Apply(malformed) error = %v, want one naming --jumphost-tag", err)
	}
}

func TestApplyRejectsEmptyJumphostTagAndPrefix(t *testing.T) {
	d := Defaults()
	if _, err := d.Apply(Overrides{JumphostTag: new("")}); err == nil {
		t.Fatal("an empty --jumphost-tag must be rejected: the jumphost lookup needs a tag")
	}
	if _, err := d.Apply(Overrides{MCPServerPrefix: new("  ")}); err == nil {
		t.Fatal("an empty --mcp-server-prefix must be rejected")
	}
}

func TestLoadEnvironments(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{"environments": " Staging=*stg* ; prod!=*prod* "}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environments != "prod!=*prod*; staging=*stg*" {
		t.Fatalf("Environments = %q, want the normalized setting", cfg.Environments)
	}
	if env := cfg.EnvironmentSet().Match("team-stg"); env.Name != "staging" {
		t.Fatalf("Match = %+v", env)
	}
	if d := Defaults(); d.Environments != "prod!=" || !d.EnvironmentSet().Match("team-prod").IsZero() {
		t.Fatalf("default = %q: no profile is production until a team sets its patterns", d.Environments)
	}
}

func TestLoadEnvironmentsMigratesProductionProfiles(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{"productionProfiles": " *PROD*, ,*-live "}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environments != "prod!=*PROD*,*-live" || !cfg.EnvironmentSet().Match("shop-live").Production {
		t.Fatalf("Environments = %q, want productionProfiles read as prod!=<patterns>", cfg.Environments)
	}
}

func TestLoadEnvironmentsWinOverProductionProfiles(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{"environments": "live!=*-live", "productionProfiles": "*prod*,team-["}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environments != "live!=*-live" {
		t.Fatalf("Environments = %q; want environments, the deprecated key ignored", cfg.Environments)
	}
}

func TestLoadRejectsInvalidEnvironments(t *testing.T) {
	path := writeConfig(t, `{"environments": "prod=*prod*"}`)
	if _, err := Load(path, nil); err == nil || !strings.Contains(err.Error(), "environments") || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want the key and the file named", err)
	}
	if _, err := Load(writeConfig(t, `{"productionProfiles": "*prod*,team-["}`), nil); err == nil || !strings.Contains(err.Error(), "productionProfiles") {
		t.Fatalf("err = %v, want an invalid productionProfiles named while it is read", err)
	}
}

func TestLoadMCPClients(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{"mcpClients": " Codex , claude:USER,codex "}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCPClients != "codex,claude:user" {
		t.Fatalf("MCPClients = %q, want it normalized", cfg.MCPClients)
	}
	sel := cfg.MCPClientSelection()
	if sel.Detect || len(sel.Targets) != 2 {
		t.Fatalf("selection = %+v", sel)
	}
	defaults := Defaults()
	if sel := defaults.MCPClientSelection(); !sel.Detect || len(sel.Targets) != 0 {
		t.Fatalf("default selection = %+v, want auto-detection", sel)
	}
}

func TestLoadRejectsUnknownMCPClient(t *testing.T) {
	_, err := Load(writeConfig(t, `{"mcpClients": "claude,emacs"}`), nil)
	if err == nil || !strings.Contains(err.Error(), "mcp.clients") || !strings.Contains(err.Error(), "emacs") {
		t.Fatalf("err = %v, want the key and the client named", err)
	}
}

func TestApplyMCPClientsFlag(t *testing.T) {
	d := Defaults()
	cfg, err := d.Apply(Overrides{MCPClients: new("none")})
	if err != nil || cfg.MCPClients != "none" {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
	if _, err := d.Apply(Overrides{MCPClients: new("codex:team")}); err == nil || !strings.Contains(err.Error(), "--mcp-clients") {
		t.Fatalf("err = %v, want the flag named", err)
	}
}

func TestModeSetting(t *testing.T) {
	defaults := Defaults()
	if defaults.Mode != ModeCLI {
		t.Fatalf("default mode = %q", defaults.Mode)
	}
	cfg, err := Load(writeConfig(t, `{"mode": " Daemon "}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeDaemon {
		t.Fatalf("mode = %q", cfg.Mode)
	}
	if _, err = Load(writeConfig(t, `{"mode": "service"}`), nil); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("err = %v", err)
	}
	out, err := defaults.Apply(Overrides{Mode: new("daemon")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Mode != ModeDaemon {
		t.Fatalf("override = %q", out.Mode)
	}
}

func TestParseModules(t *testing.T) {
	got, err := parseModules(" DynamoDB, sql,,sql ")
	if err != nil || !reflect.DeepEqual(got, []string{"sql", "dynamodb"}) {
		t.Fatalf("parseModules = %v, %v; want [sql dynamodb] in Modules order", got, err)
	}
	if got, err = parseModules(" "); err != nil || got != nil {
		t.Fatalf("parseModules(blank) = %v, %v; want none", got, err)
	}
	if _, err = parseModules("sql,kafka"); err == nil {
		t.Fatal("parseModules(kafka) succeeded; want an unknown module error")
	}
}
