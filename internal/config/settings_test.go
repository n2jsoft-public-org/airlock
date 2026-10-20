package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	settings, err := Settings(filepath.Join(t.TempDir(), "config.json"), nil)
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
	if got := settingOf(t, settings, "aws.jumphostTag").Value; got != "Usage=jumphost" {
		t.Fatalf("jumphostTag = %q", got)
	}
	if got := settingOf(t, settings, "aws.defaultRegion").Value; got != "" {
		t.Fatalf("defaultRegion = %q, want none", got)
	}
}

func TestSettingsTellFileValuesApart(t *testing.T) {
	path := writeConfig(t, `{"aws": {"defaultRegion": "eu-west-3"}, "modules": {"sql": {"readOnlySecretTag": "AccessRole=ro-access"}}}`)
	settings, err := Settings(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := settingOf(t, settings, "aws.defaultRegion"); s.Value != "eu-west-3" || s.Source != SourceFile {
		t.Fatalf("defaultRegion = %+v", s)
	}
	if s := settingOf(t, settings, "modules.sql.readOnlySecretTag"); s.Value != "AccessRole=ro-access" || s.Source != SourceFile {
		t.Fatalf("readOnlySecretTag = %+v", s)
	}
	if s := settingOf(t, settings, "mcp.serverPrefix"); s.Value != "airlock" || s.Source != SourceDefault {
		t.Fatalf("mcpServerPrefix = %+v", s)
	}
}

func TestSettingsRejectInvalidFile(t *testing.T) {
	path := writeConfig(t, `{"jumphostTag": "Usage"}`)
	if _, err := Settings(path, nil); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want the invalid file named", err)
	}
}

func TestSetCreatesTheFileWithOnlyThatKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "airlock", "config.json")
	if err := Set(path, nil, "aws.defaultRegion", " eu-west-3 "); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "{\n  \"aws\": {\n    \"defaultRegion\": \"eu-west-3\"\n  }\n}\n"; got != want {
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
	path := writeConfig(t, `{"mcp": {"serverPrefix": "acme"}, "aws": {"defaultRegion": "eu-west-3"}}`)
	if err := Set(path, nil, "aws.jumphostTag", "Env=bastion"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"aws\": {\n    \"defaultRegion\": \"eu-west-3\",\n    \"jumphostTag\": \"Env=bastion\"\n  },\n  \"mcp\": {\n    \"serverPrefix\": \"acme\"\n  }\n}\n"
	if string(data) != want {
		t.Fatalf("file = %q, want %q", data, want)
	}
}

func TestSetValidatesKeyAndValue(t *testing.T) {
	path := writeConfig(t, `{"aws": {"defaultRegion": "eu-west-3"}}`)

	err := Set(path, nil, "jumphost", "Env=bastion")
	if err == nil || !strings.Contains(err.Error(), `unknown setting "jumphost"`) || !strings.Contains(err.Error(), "aws.jumphostTag") {
		t.Fatalf("unknown key err = %v, want the valid keys listed", err)
	}
	if err = Set(path, nil, "aws.jumphostTag", "Env"); err == nil || !strings.Contains(err.Error(), "aws.jumphostTag") {
		t.Fatalf("malformed tag err = %v", err)
	}
	if err = Set(path, nil, "mcp.serverPrefix", " "); err == nil {
		t.Fatal("an empty MCP server prefix must be refused")
	}

	cfg, err := Load(path, nil)
	if err != nil || cfg.DefaultRegion != "eu-west-3" || cfg.JumphostTag.String() != "Usage=jumphost" {
		t.Fatalf("refused sets must leave the file unchanged: %+v, %v", cfg, err)
	}
}

func TestSetClearsAnOptionalTag(t *testing.T) {
	path := writeConfig(t, `{"modules": {"sql": {"readOnlySecretTag": "AccessRole=ro-access"}}}`)
	if err := Set(path, nil, "modules.sql.readOnlySecretTag", ""); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, nil)
	if err != nil || !cfg.ReadOnlySecretTag.IsZero() {
		t.Fatalf("cfg = %+v, %v, want the read-only secret tag cleared", cfg, err)
	}
}

func TestSetRefusesToRewriteAnInvalidFile(t *testing.T) {
	path := writeConfig(t, `{not json`)
	if err := Set(path, nil, "aws.defaultRegion", "eu-west-3"); err == nil {
		t.Fatal("Set must not rewrite a file it cannot parse")
	}
	data, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir.
	if err != nil || string(data) != "{not json" {
		t.Fatalf("file was rewritten: %q (%v)", data, err)
	}
}

func TestUnsetRemovesTheKey(t *testing.T) {
	path := writeConfig(t, `{"aws": {"defaultRegion": "eu-west-3"}, "mcp": {"serverPrefix": "acme"}}`)
	removed, err := Unset(path, nil, "aws.defaultRegion")
	if err != nil || !removed {
		t.Fatalf("Unset = %v, %v", removed, err)
	}
	settings, err := Settings(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := settingOf(t, settings, "aws.defaultRegion"); s.Source != SourceDefault || s.Value != "" {
		t.Fatalf("defaultRegion = %+v, want the default again", s)
	}
	if s := settingOf(t, settings, "mcp.serverPrefix"); s.Value != "acme" {
		t.Fatalf("mcpServerPrefix = %+v, the other keys must stay", s)
	}

	removed, err = Unset(path, nil, "aws.defaultRegion")
	if err != nil || removed {
		t.Fatalf("second Unset = %v, %v, want nothing to remove", removed, err)
	}
	if _, err := Unset(path, nil, "region"); err == nil {
		t.Fatal("unknown keys must be refused")
	}
	if removed, err := Unset(filepath.Join(t.TempDir(), "absent.json"), nil, "aws.defaultRegion"); err != nil || removed {
		t.Fatalf("Unset without file = %v, %v", removed, err)
	}
}

func TestWriteTemplateListsEveryKeyWithItsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "airlock", "config.json")
	if err := WriteTemplate(path, nil, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "language": "auto",
  "organizationConfig": "",
  "mode": "cli",
  "environments": "prod!=",
  "aws": {
    "defaultRegion": "",
    "jumphostTag": "Usage=jumphost",
    "randomLocalPort": true
  },
  "mcp": {
    "serverPrefix": "airlock",
    "clients": "auto",
    "gateway": {
      "enabled": true,
      "port": 7821
    }
  },
  "agent": {
    "autoConnect": "none",
    "writes": false,
    "idleTimeout": "30m"
  },
  "daemon": {
    "idleTimeout": "10m"
  },
  "modules": {
    "sql": {
      "enabled": true,
      "defaultDbUser": "",
      "readOnlySecretTag": ""
    },
    "k8s": {
      "enabled": true
    },
    "cache": {
      "enabled": true
    },
    "dynamodb": {
      "enabled": true
    },
    "s3": {
      "enabled": true
    },
    "logs": {
      "enabled": true
    }
  }
}
`
	if string(data) != want {
		t.Fatalf("template = %q, want %q", data, want)
	}
	if cfg, err := Load(path, nil); err != nil || !reflect.DeepEqual(cfg, Defaults()) {
		t.Fatalf("the template must load as the defaults: %+v, %v", cfg, err)
	}
}

func TestWriteTemplateRefusesAnExistingFileUnlessForced(t *testing.T) {
	path := writeConfig(t, `{"mcpServerPrefix": "acme"}`)
	if err := WriteTemplate(path, nil, false); !errors.Is(err, ErrFileExists) {
		t.Fatalf("err = %v, want ErrFileExists", err)
	}

	if err := WriteTemplate(path, nil, true); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".bak") // #nosec G304 -- test file under t.TempDir.
	if err != nil || string(backup) != `{"mcpServerPrefix": "acme"}` {
		t.Fatalf("backup = %q, %v, want the previous file", backup, err)
	}
	if cfg, err := Load(path, nil); err != nil || !reflect.DeepEqual(cfg, Defaults()) {
		t.Fatalf("forced template = %+v, %v", cfg, err)
	}
}

func TestSetNamesTheFileWhenAnotherKeyIsInvalid(t *testing.T) {
	path := writeConfig(t, `{"aws": {"jumphostTag": "Usage"}}`)
	err := Set(path, nil, "aws.defaultRegion", "eu-west-3")
	if err == nil || !strings.Contains(err.Error(), "invalid config "+path+": aws.jumphostTag") {
		t.Fatalf("err = %v, want the invalid file and key named", err)
	}
	if err := Set(path, nil, "aws.jumphostTag", "Env=bastion"); err != nil {
		t.Fatalf("setting the invalid key must repair the file: %v", err)
	}
	if cfg, err := Load(path, nil); err != nil || cfg.JumphostTag.String() != "Env=bastion" {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
}

func TestSetEnvironmentsRemovesProductionProfiles(t *testing.T) {
	path := writeConfig(t, `{"defaultRegion": "eu-west-3", "productionProfiles": "*prod*"}`)
	if err := Set(path, nil, "environments", "prod!=*prod*; dev=*dev*"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "productionProfiles") || !strings.Contains(string(raw), `"environments": "prod!=*prod*; dev=*dev*"`) {
		t.Fatalf("file = %s", raw)
	}
}

func TestSetProductionProfilesIsRefused(t *testing.T) {
	path := writeConfig(t, `{}`)
	err := Set(path, nil, "productionProfiles", "*prod*,*-live")
	if err == nil || !strings.Contains(err.Error(), "airlock config set environments 'prod!=*prod*,*-live'") {
		t.Fatalf("err = %v, want the equivalent command", err)
	}
	if removed, err := Unset(writeConfig(t, `{"productionProfiles": "*prod*"}`), nil, "productionProfiles"); err != nil || !removed {
		t.Fatalf("Unset = %v, %v; the deprecated key must stay removable", removed, err)
	}
}

func TestSettingsListProductionProfilesAsDeprecated(t *testing.T) {
	settings, err := Settings(writeConfig(t, `{"productionProfiles": "*prod*"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := settingOf(t, settings, "environments"); s.Value != "prod!=*prod*" || s.Source != SourceFile {
		t.Fatalf("environments = %+v, want the migrated value from the file", s)
	}
	if s := settingOf(t, settings, "productionProfiles"); s.Source != SourceDeprecated || s.Value != "*prod*" {
		t.Fatalf("productionProfiles = %+v, want it listed as deprecated", s)
	}
	clean, err := Settings(writeConfig(t, `{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range clean {
		if s.Name == "productionProfiles" {
			t.Fatal("productionProfiles is listed although the file does not set it")
		}
	}
}

func TestSetEnvironmentsRefusesToDropTheProductionPatterns(t *testing.T) {
	const content = `{"productionProfiles": "*prod*,*-live"}`
	path := writeConfig(t, content)
	err := Set(path, nil, "environments", "staging=*stg*")
	if err == nil || !strings.Contains(err.Error(), "productionProfiles flags *prod*,*-live") ||
		!strings.Contains(err.Error(), "airlock config set environments 'prod!=*prod*,*-live; staging=*stg*'") ||
		!strings.Contains(err.Error(), "airlock config unset productionProfiles") {
		t.Fatalf("err = %v, want the refusal with both ways out", err)
	}
	if raw, readErr := os.ReadFile(path); readErr != nil || string(raw) != content { // #nosec G304 -- test file under t.TempDir.
		t.Fatalf("file = %q (%v), want it unchanged", raw, readErr)
	}
	if err = Set(path, nil, "environments", "live!=; staging=*stg*"); err == nil || !strings.Contains(err.Error(), "live!=*prod*,*-live; staging=*stg*") {
		t.Fatalf("err = %v, want the suggestion to keep the production name", err)
	}
}

func TestSetEnvironmentsAllowedWhenProductionKeepsPatterns(t *testing.T) {
	path := writeConfig(t, `{"productionProfiles": "*prod*"}`)
	if err := Set(path, nil, "environments", "prod!=*prod*,*-live; staging=*stg*"); err != nil {
		t.Fatal(err)
	}
	if cfg, err := Load(path, nil); err != nil || cfg.Environments != "prod!=*prod*,*-live; staging=*stg*" {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
}

func TestSetEnvironmentsAllowedWithoutProductionProfiles(t *testing.T) {
	for _, content := range []string{`{}`, `{"productionProfiles": " , "}`} {
		path := writeConfig(t, content)
		if err := Set(path, nil, "environments", "staging=*stg*"); err != nil {
			t.Fatalf("%s: %v", content, err)
		}
	}
}

func TestUnsetEnvironmentsClearsTheLegacyKeyToo(t *testing.T) {
	path := writeConfig(t, `{"defaultRegion": "eu-west-3", "productionProfiles": "*prod*"}`)
	removed, err := Unset(path, nil, "environments")
	if err != nil || !removed {
		t.Fatalf("Unset = %v, %v, want removed", removed, err)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir.
	if err != nil || strings.Contains(string(raw), "productionProfiles") || !strings.Contains(string(raw), "defaultRegion") {
		t.Fatalf("file = %s (%v)", raw, err)
	}
	if removed, err = Unset(path, nil, "environments"); err != nil || removed {
		t.Fatalf("second Unset = %v, %v, want not set", removed, err)
	}
}

func TestFlatFileLoadsAndIsRewrittenNestedOnSet(t *testing.T) {
	path := writeConfig(t, `{"jumphostTag": "Env=bastion", "mcpGatewayPort": "8123", "agentWrites": "true",
		"defaultDbUser": "app_ro", "productionProfiles": "*prod*"}`)
	cfg, err := Load(path, nil)
	if err != nil || cfg.JumphostTag.String() != "Env=bastion" || cfg.MCPGatewayPort != 8123 || !cfg.AgentWrites ||
		cfg.DefaultDBUser.IAM != "app_ro" {
		t.Fatalf("Load = %+v, %v, want the flat keys read", cfg, err)
	}
	if raw, readErr := os.ReadFile(path); readErr != nil || !strings.Contains(string(raw), `"jumphostTag"`) { // #nosec G304 -- test file under t.TempDir.
		t.Fatalf("file = %s (%v), want it untouched by Load", raw, readErr)
	}
	settings, err := Settings(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := settingOf(t, settings, "aws.jumphostTag"); s.Value != "Env=bastion" || s.Source != SourceFile {
		t.Fatalf("aws.jumphostTag = %+v, want the flat value from the file", s)
	}
	if s := settingOf(t, settings, "jumphostTag"); s.Value != "Env=bastion" || s.Source != SourceDeprecated {
		t.Fatalf("jumphostTag = %+v, want it listed as deprecated", s)
	}

	if err = Set(path, nil, "aws.defaultRegion", "eu-west-3"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "aws": {
    "defaultRegion": "eu-west-3",
    "jumphostTag": "Env=bastion"
  },
  "mcp": {
    "gateway": {
      "port": 8123
    }
  },
  "agent": {
    "writes": true
  },
  "modules": {
    "sql": {
      "defaultDbUser": "app_ro"
    }
  },
  "productionProfiles": "*prod*"
}
`
	if string(raw) != want {
		t.Fatalf("file = %s, want %s", raw, want)
	}
	if after, err := Load(path, nil); err != nil || after.JumphostTag != cfg.JumphostTag || after.Environments != cfg.Environments {
		t.Fatalf("rewritten file loads as %+v, %v", after, err)
	}
}

func TestNestedKeyWinsOverItsFlatKey(t *testing.T) {
	path := writeConfig(t, `{"jumphostTag": "Env=old", "aws": {"jumphostTag": "Env=new"}}`)
	settings, err := Settings(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := settingOf(t, settings, "aws.jumphostTag"); s.Value != "Env=new" {
		t.Fatalf("aws.jumphostTag = %+v, want the nested value", s)
	}
	if s := settingOf(t, settings, "jumphostTag"); s.Value != "Env=old (ignored: aws.jumphostTag is set)" {
		t.Fatalf("jumphostTag = %+v, want it flagged as ignored", s)
	}
}

func TestDisabledModulesMapsToModulesEnabled(t *testing.T) {
	path := writeConfig(t, `{"disabledModules": "K8s, s3", "modules": {"s3": {"enabled": true}}}`)
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.DisabledModules, []string{"k8s"}) {
		t.Fatalf("DisabledModules = %v, want k8s only (s3 enabled by its own key)", cfg.DisabledModules)
	}
	settings, err := Settings(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := settingOf(t, settings, "modules.k8s.enabled"); s.Value != "false" || s.Source != SourceFile {
		t.Fatalf("modules.k8s.enabled = %+v", s)
	}
	if s := settingOf(t, settings, "disabledModules"); s.Source != SourceDeprecated {
		t.Fatalf("disabledModules = %+v, want it listed as deprecated", s)
	}
	if _, err = Load(writeConfig(t, `{"disabledModules": "kafka"}`), nil); err == nil || !strings.Contains(err.Error(), "disabledModules") {
		t.Fatalf("err = %v, want the unknown module refused naming disabledModules", err)
	}
	if err = Set(path, nil, "disabledModules", "sql"); err == nil || !strings.Contains(err.Error(), "modules.<name>.enabled") {
		t.Fatalf("err = %v, want the replacing key named", err)
	}
}

func TestSetAcceptsTheFlatNames(t *testing.T) {
	path := writeConfig(t, `{}`)
	if err := Set(path, nil, "mcpGateway", "false"); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "{\n  \"mcp\": {\n    \"gateway\": {\n      \"enabled\": false\n    }\n  }\n}\n" { // #nosec G304 -- test file under t.TempDir.
		t.Fatalf("file = %q (%v)", raw, err)
	}
	if removed, err := Unset(path, nil, "mcpGateway"); err != nil || !removed {
		t.Fatalf("Unset = %v, %v", removed, err)
	}
	if newKey, ok := Renamed("mcpGateway"); !ok || newKey != "mcp.gateway.enabled" {
		t.Fatalf("Renamed = %q, %v", newKey, ok)
	}
	if _, ok := Renamed("mode"); ok {
		t.Fatal("mode is not renamed")
	}
}

func TestValuesAcceptStringsAndJSONTypes(t *testing.T) {
	for _, body := range []string{
		`{"mcp": {"gateway": {"enabled": "false", "port": "8123"}}, "modules": {"sql": {"enabled": "false"}}}`,
		`{"mcp": {"gateway": {"enabled": false, "port": 8123}}, "modules": {"sql": {"enabled": false}}}`,
	} {
		cfg, err := Load(writeConfig(t, body), nil)
		if err != nil || cfg.MCPGateway || cfg.MCPGatewayPort != 8123 || !reflect.DeepEqual(cfg.DisabledModules, []string{"sql"}) {
			t.Fatalf("%s: %+v, %v", body, cfg, err)
		}
	}
	for _, body := range []string{`{"aws": {"region": "x"}}`, `{"aws": "x"}`, `{"mode": ["cli"]}`} {
		if _, err := Load(writeConfig(t, body), nil); err == nil {
			t.Fatalf("%s: loaded, want it refused", body)
		}
	}
}

func TestSetOrganizationConfigAcceptsPathAndHTTPS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, v := range []string{"/etc/acme/airlock.json", "https://config.acme.test/airlock.json",
		"https://github.com/acme/infra/blob/main/airlock.json"} {
		if err := Set(path, nil, "organizationConfig", v); err != nil {
			t.Fatalf("Set(%q) = %v", v, err)
		}
	}
}

func TestSetOrganizationConfigRefusesHTTPAndRelative(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, v := range []string{"http://config.acme.test/airlock.json", "airlock.json", "./x.json", "https://"} {
		if err := Set(path, nil, "organizationConfig", v); err == nil {
			t.Fatalf("Set(%q) accepted", v)
		}
	}
}

func TestSetOrganizationConfigRefusesWhatTheResolverRefuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	for _, v := range []string{"https://user:s3cr3t@config.acme.test/airlock.json", "https://github.com/acme/infra"} {
		err := Set(path, nil, "organizationConfig", v)
		if err == nil {
			t.Fatalf("Set(%q) accepted", v)
		}
		if strings.Contains(err.Error(), "s3cr3t") {
			t.Fatalf("Set(%q) = %v: the error echoes the credentials", v, err)
		}
	}
	if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), "s3cr3t") { // #nosec G304 -- test file under t.TempDir.
		t.Fatalf("config.json holds the credentials: %s", data)
	}
}

func TestWriteTemplateLeavesOutTheOrganizationKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	org, _, err := ParseOrganization("src", []byte(`{"settings":{"mode":"daemon","aws":{"jumphostTag":"Role=bastion"}},"locked":["aws.jumphostTag"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = WriteTemplate(path, org, false); err != nil {
		t.Fatal(err)
	}
	f, err := readFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.sets("mode") || f.sets("aws.jumphostTag") || !f.sets("aws.defaultRegion") {
		t.Fatalf("template sets mode %v, aws.jumphostTag %v, aws.defaultRegion %v", f.sets("mode"), f.sets("aws.jumphostTag"), f.sets("aws.defaultRegion"))
	}
	if cfg, loadErr := Load(path, org); loadErr != nil || cfg.Mode != ModeDaemon {
		t.Fatalf("the organization value must apply: %+v, %v", cfg, loadErr)
	}
}

func TestWriteTemplateForcedKeepsThePersonalKeys(t *testing.T) {
	path := writeConfig(t, `{"language": "fr", "organizationConfig": "/etc/acme/airlock.json", "mode": "daemon"}`)
	if err := WriteTemplate(path, nil, true); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, nil)
	if err != nil || cfg.Language != "fr" || cfg.OrganizationConfig != "/etc/acme/airlock.json" || cfg.Mode != ModeCLI {
		t.Fatalf("forced template = %+v, %v", cfg, err)
	}
}

func TestExportNeverCarriesOrganizationConfig(t *testing.T) {
	path := writeConfig(t, `{"mode": "cli", "organizationConfig": "https://config.acme.test/airlock.json"}`)
	shared, err := Export(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := decodeShare(shared)
	if err != nil {
		t.Fatal(err)
	}
	if f.OrganizationConfig != nil {
		t.Fatalf("the shared string carries organizationConfig: %q", *f.OrganizationConfig)
	}
}

func TestValidateUsesTheSetRule(t *testing.T) {
	dir := t.TempDir()
	for _, c := range [][2]string{{"mcp.gateway.port", "7822"}, {"agentWrites", "true"}, {"mcp.clients", "auto,claude:user"}} {
		if err := Validate(c[0], c[1]); err != nil {
			t.Errorf("Validate(%q, %q) = %v", c[0], c[1], err)
		}
	}
	for _, c := range [][2]string{{"mcp.gateway.port", "70000"}, {"aws.jumphostTag", "nokey"}, {"productionProfiles", "x"}, {"nope", "x"}} {
		if err := Validate(c[0], c[1]); err == nil {
			t.Errorf("Validate(%q, %q) = nil, want an error", c[0], c[1])
		}
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("entries = %v, %v; want no file", entries, err)
	}
}
