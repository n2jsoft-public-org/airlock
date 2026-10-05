package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func settingOf(t *testing.T, settings []Setting, key string) Setting {
	t.Helper()
	for _, s := range settings {
		if s.Name == key {
			return s
		}
	}
	t.Fatalf("no setting %q in %+v", key, settings)
	return Setting{}
}

func TestSettingsWithoutFileAreDefaults(t *testing.T) {
	settings, err := Settings(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(settings) != len(Keys) {
		t.Fatalf("got %d settings, want one per key", len(settings))
	}
	for idx, s := range settings {
		if s.Name != Keys[idx].Name || s.Source != SourceDefault {
			t.Fatalf("settings[%d] = %+v, want %s from the defaults", idx, s, Keys[idx].Name)
		}
	}
	if got := settingOf(t, settings, "jumphostTag").Value; got != "Usage=jumphost" {
		t.Fatalf("jumphostTag = %q", got)
	}
	if got := settingOf(t, settings, "defaultRegion").Value; got != "" {
		t.Fatalf("defaultRegion = %q, want none", got)
	}
}

func TestSettingsTellFileValuesApart(t *testing.T) {
	path := writeConfig(t, `{"defaultRegion": "eu-west-3", "readOnlySecretTag": "AccessRole=ro-access"}`)
	settings, err := Settings(path)
	if err != nil {
		t.Fatal(err)
	}
	if s := settingOf(t, settings, "defaultRegion"); s.Value != "eu-west-3" || s.Source != SourceFile {
		t.Fatalf("defaultRegion = %+v", s)
	}
	if s := settingOf(t, settings, "readOnlySecretTag"); s.Value != "AccessRole=ro-access" || s.Source != SourceFile {
		t.Fatalf("readOnlySecretTag = %+v", s)
	}
	if s := settingOf(t, settings, "mcpServerPrefix"); s.Value != "airlock" || s.Source != SourceDefault {
		t.Fatalf("mcpServerPrefix = %+v", s)
	}
}

func TestSettingsRejectInvalidFile(t *testing.T) {
	path := writeConfig(t, `{"jumphostTag": "Usage"}`)
	if _, err := Settings(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want the invalid file named", err)
	}
}

func TestSetCreatesTheFileWithOnlyThatKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "airlock", "config.json")
	if err := Set(path, "defaultRegion", " eu-west-3 "); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "{\n  \"defaultRegion\": \"eu-west-3\"\n}\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("file mode = %o, want 600", mode)
		}
	}
}

func TestSetKeepsTheOtherKeysInKeyOrder(t *testing.T) {
	path := writeConfig(t, `{"mcpServerPrefix": "acme", "defaultRegion": "eu-west-3"}`)
	if err := Set(path, "jumphostTag", "Env=bastion"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"jumphostTag\": \"Env=bastion\",\n  \"mcpServerPrefix\": \"acme\",\n  \"defaultRegion\": \"eu-west-3\"\n}\n"
	if string(data) != want {
		t.Fatalf("file = %q, want %q", data, want)
	}
}

func TestSetValidatesKeyAndValue(t *testing.T) {
	path := writeConfig(t, `{"defaultRegion": "eu-west-3"}`)

	err := Set(path, "jumphost", "Env=bastion")
	if err == nil || !strings.Contains(err.Error(), `unknown setting "jumphost"`) || !strings.Contains(err.Error(), "jumphostTag") {
		t.Fatalf("unknown key err = %v, want the valid keys listed", err)
	}
	if err = Set(path, "jumphostTag", "Env"); err == nil || !strings.Contains(err.Error(), "jumphostTag") {
		t.Fatalf("malformed tag err = %v", err)
	}
	if err = Set(path, "mcpServerPrefix", " "); err == nil {
		t.Fatal("an empty MCP server prefix must be refused")
	}

	cfg, err := Load(path)
	if err != nil || cfg.DefaultRegion != "eu-west-3" || cfg.JumphostTag.String() != "Usage=jumphost" {
		t.Fatalf("refused sets must leave the file unchanged: %+v, %v", cfg, err)
	}
}

func TestSetClearsAnOptionalTag(t *testing.T) {
	path := writeConfig(t, `{"readOnlySecretTag": "AccessRole=ro-access"}`)
	if err := Set(path, "readOnlySecretTag", ""); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || !cfg.ReadOnlySecretTag.IsZero() {
		t.Fatalf("cfg = %+v, %v, want the read-only secret tag cleared", cfg, err)
	}
}

func TestSetRefusesToRewriteAnInvalidFile(t *testing.T) {
	path := writeConfig(t, `{not json`)
	if err := Set(path, "defaultRegion", "eu-west-3"); err == nil {
		t.Fatal("Set must not rewrite a file it cannot parse")
	}
	data, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir.
	if err != nil || string(data) != "{not json" {
		t.Fatalf("file was rewritten: %q (%v)", data, err)
	}
}

func TestUnsetRemovesTheKey(t *testing.T) {
	path := writeConfig(t, `{"defaultRegion": "eu-west-3", "mcpServerPrefix": "acme"}`)
	removed, err := Unset(path, "defaultRegion")
	if err != nil || !removed {
		t.Fatalf("Unset = %v, %v", removed, err)
	}
	settings, err := Settings(path)
	if err != nil {
		t.Fatal(err)
	}
	if s := settingOf(t, settings, "defaultRegion"); s.Source != SourceDefault || s.Value != "" {
		t.Fatalf("defaultRegion = %+v, want the default again", s)
	}
	if s := settingOf(t, settings, "mcpServerPrefix"); s.Value != "acme" {
		t.Fatalf("mcpServerPrefix = %+v, the other keys must stay", s)
	}

	removed, err = Unset(path, "defaultRegion")
	if err != nil || removed {
		t.Fatalf("second Unset = %v, %v, want nothing to remove", removed, err)
	}
	if _, err := Unset(path, "region"); err == nil {
		t.Fatal("unknown keys must be refused")
	}
	if removed, err := Unset(filepath.Join(t.TempDir(), "absent.json"), "defaultRegion"); err != nil || removed {
		t.Fatalf("Unset without file = %v, %v", removed, err)
	}
}

func TestWriteTemplateListsEveryKeyWithItsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "airlock", "config.json")
	if err := WriteTemplate(path, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"jumphostTag\": \"Usage=jumphost\",\n  \"defaultDbUser\": \"\",\n  \"readOnlySecretTag\": \"\",\n  \"mcpServerPrefix\": \"airlock\",\n  \"defaultRegion\": \"\",\n  \"productionProfiles\": \"\",\n  \"mcpClients\": \"auto\",\n  \"mode\": \"cli\",\n  \"mcpGateway\": \"true\",\n  \"mcpGatewayPort\": \"7821\",\n  \"agentAutoConnect\": \"none\",\n  \"agentWrites\": \"false\",\n  \"agentIdleTimeout\": \"30m\"\n}\n"
	if string(data) != want {
		t.Fatalf("template = %q, want %q", data, want)
	}
	if cfg, err := Load(path); err != nil || cfg != Defaults() {
		t.Fatalf("the template must load as the defaults: %+v, %v", cfg, err)
	}
}

func TestWriteTemplateRefusesAnExistingFileUnlessForced(t *testing.T) {
	path := writeConfig(t, `{"mcpServerPrefix": "acme"}`)
	if err := WriteTemplate(path, false); !errors.Is(err, ErrFileExists) {
		t.Fatalf("err = %v, want ErrFileExists", err)
	}

	if err := WriteTemplate(path, true); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".bak") // #nosec G304 -- test file under t.TempDir.
	if err != nil || string(backup) != `{"mcpServerPrefix": "acme"}` {
		t.Fatalf("backup = %q, %v, want the previous file", backup, err)
	}
	if cfg, err := Load(path); err != nil || cfg != Defaults() {
		t.Fatalf("forced template = %+v, %v", cfg, err)
	}
}

func TestSetNamesTheFileWhenAnotherKeyIsInvalid(t *testing.T) {
	path := writeConfig(t, `{"jumphostTag": "Usage"}`)
	err := Set(path, "defaultRegion", "eu-west-3")
	if err == nil || !strings.Contains(err.Error(), "invalid config "+path+": jumphostTag") {
		t.Fatalf("err = %v, want the invalid file and key named", err)
	}
	if err := Set(path, "jumphostTag", "Env=bastion"); err != nil {
		t.Fatalf("setting the invalid key must repair the file: %v", err)
	}
	if cfg, err := Load(path); err != nil || cfg.JumphostTag.String() != "Env=bastion" {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
}
