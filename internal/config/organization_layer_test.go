package config

import (
	"reflect"
	"slices"
	"testing"
)

func TestLoadOrderDefaultsOrganizationLocal(t *testing.T) {
	org, _ := mustOrg(t, `{"settings":{"aws":{"jumphostTag":"Role=org"},"mcp":{"serverPrefix":"acme"}}}`)
	cfg, err := Load(writeConfig(t, `{"mcp":{"serverPrefix":"mine"}}`), org)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JumphostTag.String() != "Role=org" || cfg.MCPServerPrefix != "mine" || cfg.Mode != ModeCLI {
		t.Fatalf("Load = %+v", cfg)
	}
}

func TestLoadLockWinsOverLocal(t *testing.T) {
	org, _ := mustOrg(t, `{"settings":{"agent":{"writes":false}},"locked":["agent.writes"]}`)
	cfg, err := Load(writeConfig(t, `{"agent":{"writes":true}}`), org)
	if err != nil || cfg.AgentWrites {
		t.Fatalf("Load = %+v, %v; want agent.writes false", cfg, err)
	}
}

func TestLoadLockIgnoresLegacyLocalKeys(t *testing.T) {
	org, _ := mustOrg(t, `{"settings":{"agent":{"writes":false},"modules":{"s3":{"enabled":false},"k8s":{"enabled":true}},
		"environments":"prod!=*live*"},"locked":["agent.writes","modules.s3.enabled","modules.k8s.enabled","environments"]}`)
	cfg, err := Load(writeConfig(t, `{"agentWrites":true,"disabledModules":"k8s","productionProfiles":"*prod*"}`), org)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentWrites || !reflect.DeepEqual(cfg.DisabledModules, []string{"s3"}) || cfg.Environments != "prod!=*live*" {
		t.Fatalf("Load = %+v; want the organization's values", cfg)
	}
	// An empty disabledModules turns nothing off: the organization's s3 stays off.
	cfg, err = Load(writeConfig(t, `{"disabledModules":""}`), org)
	if err != nil || !slices.Equal(cfg.DisabledModules, []string{"s3"}) {
		t.Fatalf("Load = %+v, %v", cfg, err)
	}
}

func TestLoadNilOrganizationUnchanged(t *testing.T) {
	path := writeConfig(t, `{"jumphostTag":"Role=bastion","mode":"daemon","agent":{"writes":true},
		"disabledModules":"s3","productionProfiles":"*prod*","mcpServerPrefix":"acme"}`)
	f, err := readFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := f.apply(new(Defaults()))
	if err != nil {
		t.Fatal(err)
	}
	empty, _ := mustOrg(t, `{}`)
	for _, org := range []*Organization{nil, empty} {
		if cfg, loadErr := Load(path, org); loadErr != nil || !reflect.DeepEqual(cfg, want) {
			t.Fatalf("Load(%v) = %+v, %v; want %+v", org, cfg, loadErr, want)
		}
	}
}

func TestSettingsSourcesAndLocks(t *testing.T) {
	org, _ := mustOrg(t, `{"settings":{"mode":"daemon","agent":{"writes":false}},"locked":["agent.writes"]}`)
	settings, err := Settings(writeConfig(t, `{"agent":{"writes":true},"aws":{"defaultRegion":"eu-west-3"}}`), org)
	if err != nil {
		t.Fatal(err)
	}
	if s := settingOf(t, settings, "mode"); s.Source != SourceOrganization || s.Value != ModeDaemon || s.Locked {
		t.Errorf("mode = %+v", s)
	}
	if s := settingOf(t, settings, "agent.writes"); s.Source != SourceOrganization || !s.Locked || s.Ignored != "true" || s.Value != "false" {
		t.Errorf("agent.writes = %+v", s)
	}
	if s := settingOf(t, settings, "aws.defaultRegion"); s.Source != SourceFile || s.Shadows {
		t.Errorf("aws.defaultRegion = %+v", s)
	}
	if s := settingOf(t, settings, "mcp.serverPrefix"); s.Source != SourceDefault {
		t.Errorf("mcp.serverPrefix = %+v", s)
	}
}

func TestSettingsLockIgnoresLegacyKeys(t *testing.T) {
	org, _ := mustOrg(t, `{"settings":{"agent":{"writes":false},"modules":{"s3":{"enabled":true}},"environments":"prod!=*live*"},
		"locked":["agent.writes","modules.s3.enabled","environments"]}`)
	settings, err := Settings(writeConfig(t, `{"agentWrites":" true ","disabledModules":"s3","productionProfiles":"*prod*"}`), org)
	if err != nil {
		t.Fatal(err)
	}
	for name, ignored := range map[string]string{"agent.writes": "true", "modules.s3.enabled": "false", "environments": "*prod*"} {
		if s := settingOf(t, settings, name); !s.Locked || s.Ignored != ignored {
			t.Errorf("%s = %+v, want Ignored %q", name, s, ignored)
		}
	}
	for name, value := range map[string]string{
		"agentWrites":        "true" + lockedSuffix,
		"disabledModules":    "s3" + lockedSuffix,
		"productionProfiles": "*prod*" + lockedSuffix,
	} {
		if s := settingOf(t, settings, name); s.Source != SourceDeprecated || s.Value != value {
			t.Errorf("%s = %+v, want %q", name, s, value)
		}
	}
}

func TestSettingsShadows(t *testing.T) {
	org, _ := mustOrg(t, `{"settings":{"mcp":{"serverPrefix":"acme"}}}`)
	for _, tc := range []struct {
		local   string
		shadows bool
	}{{"mine", true}, {" acme ", false}} {
		settings, err := Settings(writeConfig(t, `{"mcp":{"serverPrefix":"`+tc.local+`"}}`), org)
		if err != nil {
			t.Fatal(err)
		}
		if s := settingOf(t, settings, "mcp.serverPrefix"); s.Source != SourceFile || s.Shadows != tc.shadows {
			t.Errorf("local %q: %+v, want Shadows %v", tc.local, s, tc.shadows)
		}
	}
}
