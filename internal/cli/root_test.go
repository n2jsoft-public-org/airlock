package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOverridesFromFlagsOnlyChangedFlags(t *testing.T) {
	cmd := NewRootCmd()
	if err := cmd.ParseFlags([]string{"--mcp-server-prefix", "acme", "--read-only-secret-tag", ""}); err != nil {
		t.Fatal(err)
	}
	o := overridesFromFlags(cmd.Flags())
	if o.JumphostTag != nil {
		t.Fatalf("JumphostTag = %q, want nil when the flag is not given", *o.JumphostTag)
	}
	if o.MCPServerPrefix == nil || *o.MCPServerPrefix != "acme" {
		t.Fatalf("MCPServerPrefix = %v", o.MCPServerPrefix)
	}
	if o.ReadOnlySecretTag == nil || *o.ReadOnlySecretTag != "" {
		t.Fatal("an explicitly empty --read-only-secret-tag must override the file")
	}
}

func TestLoadConfigReadsXDGConfigFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	want := filepath.Join(root, "airlock", "config.json")
	writeTestFile(t, want, `{"mcpServerPrefix": "acme", "defaultRegion": "eu-west-3"}`)

	cmd := NewRootCmd()
	if err := cmd.ParseFlags([]string{"--mcp-server-prefix", "team"}); err != nil {
		t.Fatal(err)
	}
	cfg, path, err := loadConfig(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if path != want || cfg.MCPServerPrefix != "team" || cfg.DefaultRegion != "eu-west-3" {
		t.Fatalf("loadConfig = %+v, %q", cfg, path)
	}
}

func TestLoadConfigRejectsBadFlag(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cmd := NewRootCmd()
	if err := cmd.ParseFlags([]string{"--jumphost-tag", "Usage"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadConfig(cmd); err == nil {
		t.Fatal("a malformed --jumphost-tag must fail before connecting")
	}
}

func TestRootCommandLeavesErrorPrintingToMain(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cmd := NewRootCmd()
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	cmd.SetOut(&stderr)
	cmd.SetArgs([]string{"--jumphost-tag", "Usage", "rds", "x", "5432"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("a malformed --jumphost-tag must fail")
	}
	if strings.Contains(stderr.String(), "invalid tag") {
		t.Fatalf("cobra printed the error, main prints it again:\n%s", stderr.String())
	}
}
