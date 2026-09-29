package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/awstag"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func strPtr(s string) *string { return &s }

func TestDefaults(t *testing.T) {
	d := Defaults()
	if d.JumphostTag != (awstag.Tag{Key: "Usage", Value: "jumphost"}) || d.MCPServerPrefix != "airlock" {
		t.Fatalf("Defaults = %+v", d)
	}
	if d.DefaultDBUser != "" || !d.ReadOnlySecretTag.IsZero() || d.DefaultRegion != "" {
		t.Fatalf("Defaults must leave the user, secret tag and region unset: %+v", d)
	}
}

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || cfg != Defaults() {
		t.Fatalf("Load(missing) = %+v, %v", cfg, err)
	}
}

func TestLoadEmptyPathGivesDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil || cfg != Defaults() {
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
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		JumphostTag:       awstag.Tag{Key: "Usage", Value: "jumphost"},
		DefaultDBUser:     "acme_ro",
		ReadOnlySecretTag: awstag.Tag{Key: "AccessRole", Value: "ro-access"},
		MCPServerPrefix:   "acme",
		DefaultRegion:     "eu-west-3",
	}
	if cfg != want {
		t.Fatalf("Load = %+v, want %+v", cfg, want)
	}
}

func TestLoadOverridesJumphostTag(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{"jumphostTag": "Role=bastion"}`))
	if err != nil || cfg.JumphostTag != (awstag.Tag{Key: "Role", Value: "bastion"}) {
		t.Fatalf("Load = %+v, %v", cfg, err)
	}
}

func TestLoadRejectsInvalidJSON(t *testing.T) {
	path := writeConfig(t, `{"defaultRegion": "eu-west-3",}`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("Load(invalid) error = %v, want one naming %s", err, path)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	// encoding/json matches keys case-insensitively, so a typo means a key that does not exist.
	path := writeConfig(t, `{"dbUser": "acme_ro"}`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "dbUser") {
		t.Fatalf("Load(unknown key) error = %v, want one naming the file and the key", err)
	}
}

func TestLoadRejectsMalformedTag(t *testing.T) {
	path := writeConfig(t, `{"readOnlySecretTag": "ro-access"}`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "readOnlySecretTag") || !strings.Contains(err.Error(), path) {
		t.Fatalf("Load(malformed tag) error = %v", err)
	}
}

func TestApplyFlagsWinOverFile(t *testing.T) {
	cfg := Config{JumphostTag: awstag.Tag{Key: "Usage", Value: "jumphost"}, MCPServerPrefix: "acme",
		ReadOnlySecretTag: awstag.Tag{Key: "A", Value: "b"}}
	got, err := cfg.Apply(Overrides{
		JumphostTag:       strPtr("Role=bastion"),
		ReadOnlySecretTag: strPtr(""),
		MCPServerPrefix:   strPtr("team"),
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
	if err != nil || got != cfg {
		t.Fatalf("Apply(no overrides) = %+v, %v", got, err)
	}
}

func TestApplyRejectsMalformedFlag(t *testing.T) {
	d := Defaults()
	_, err := d.Apply(Overrides{JumphostTag: strPtr("Usage")})
	if err == nil || !strings.Contains(err.Error(), "--jumphost-tag") {
		t.Fatalf("Apply(malformed) error = %v, want one naming --jumphost-tag", err)
	}
}

func TestApplyRejectsEmptyJumphostTagAndPrefix(t *testing.T) {
	d := Defaults()
	if _, err := d.Apply(Overrides{JumphostTag: strPtr("")}); err == nil {
		t.Fatal("an empty --jumphost-tag must be rejected: the jumphost lookup needs a tag")
	}
	if _, err := d.Apply(Overrides{MCPServerPrefix: strPtr("  ")}); err == nil {
		t.Fatal("an empty --mcp-server-prefix must be rejected")
	}
}
