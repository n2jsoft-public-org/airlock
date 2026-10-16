package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/sharecode"
)

func changeOf(t *testing.T, plan *Plan, key string) Change {
	t.Helper()
	for _, c := range plan.Changes {
		if c.Name == key {
			return c
		}
	}
	t.Fatalf("no change for %q in %+v", key, plan.Changes)
	return Change{}
}

func TestExportImportRoundTrip(t *testing.T) {
	source := writeConfig(t, `{"mcpServerPrefix": "acme", "defaultRegion": "eu-west-3"}`)
	shared, err := Export(source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(shared, "airlock2:") || strings.ContainsAny(shared, " \n{}\"") {
		t.Fatalf("shared = %q, want one opaque airlock2: string", shared)
	}

	target := filepath.Join(t.TempDir(), "airlock", "config.json")
	plan, err := PlanImport(target, nil, shared, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 2 || !plan.HasChanges() {
		t.Fatalf("changes = %+v, want the two exported keys", plan.Changes)
	}
	if c := changeOf(t, &plan, "mcp.serverPrefix"); c.Kind != ChangeAdded || c.Old != "airlock" || !c.OldDefault || c.New != "acme" || c.NewDefault {
		t.Fatalf("mcpServerPrefix change = %+v", c)
	}
	if err = ApplyImport(&plan); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(target, nil)
	if err != nil || cfg.MCPServerPrefix != "acme" || cfg.DefaultRegion != "eu-west-3" {
		t.Fatalf("imported cfg = %+v, %v", cfg, err)
	}
}

func TestExportKeepsTheNamedKeys(t *testing.T) {
	source := writeConfig(t, `{"mcpServerPrefix": "acme", "defaultRegion": "eu-west-3", "mode": "daemon"}`)
	shared, err := Export(source, "aws.defaultRegion", "mode")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanImport(filepath.Join(t.TempDir(), "config.json"), nil, shared, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 2 {
		t.Fatalf("changes = %+v, want defaultRegion and mode only", plan.Changes)
	}
	if changeOf(t, &plan, "aws.defaultRegion").New != "eu-west-3" || changeOf(t, &plan, "mode").New != "daemon" {
		t.Fatalf("changes = %+v", plan.Changes)
	}
}

func TestExportRefusesKeysNotInTheFile(t *testing.T) {
	source := writeConfig(t, `{"mcpServerPrefix": "acme"}`)
	if _, err := Export(source, "aws.defaultRegion"); err == nil || !strings.Contains(err.Error(), "does not set aws.defaultRegion") {
		t.Fatalf("err = %v, want defaultRegion refused", err)
	}
	if _, err := Export(source, "nope"); err == nil || !strings.Contains(err.Error(), `unknown setting "nope"`) {
		t.Fatalf("err = %v, want the unknown key refused", err)
	}
}

func TestExportNeedsAKeyInTheFile(t *testing.T) {
	if _, err := Export(filepath.Join(t.TempDir(), "config.json")); err == nil || !strings.Contains(err.Error(), "nothing to export") {
		t.Fatalf("err = %v, want nothing to export", err)
	}
}

func TestExportRefusesAnInvalidFile(t *testing.T) {
	path := writeConfig(t, `{"jumphostTag": "Usage"}`)
	if _, err := Export(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want the invalid file named", err)
	}
}

func TestImportRejectsDamagedStrings(t *testing.T) {
	shared, err := Export(writeConfig(t, `{"defaultRegion": "eu-west-3"}`))
	if err != nil {
		t.Fatal(err)
	}
	altered := []byte(shared)
	last := len(altered) - 1
	if altered[last] == 'A' {
		altered[last] = 'B'
	} else {
		altered[last] = 'A'
	}
	target := filepath.Join(t.TempDir(), "config.json")
	for name, s := range map[string]string{
		"truncated":    shared[:len(shared)-4],
		"altered":      string(altered),
		"wrong prefix": "airlock3:" + strings.TrimPrefix(shared, "airlock2:"),
		"not base64":   "airlock2:!!!",
		"empty":        "airlock2:",
		"not a string": "hello",
	} {
		if _, err := PlanImport(target, nil, s, false); !errors.Is(err, ErrInvalidShare) {
			t.Fatalf("%s: err = %v, want ErrInvalidShare", name, err)
		}
	}
	if _, err := PlanImport(target, nil, "  "+shared+"\n", false); err != nil {
		t.Fatalf("surrounding spaces from a paste must be accepted: %v", err)
	}
}

func TestImportRejectsUnknownKeysAndInvalidValues(t *testing.T) {
	target := filepath.Join(t.TempDir(), "config.json")
	_, err := PlanImport(target, nil, encodeShare([]byte(`{"futureKey": "x"}`)), false)
	if err == nil || !strings.Contains(err.Error(), "futureKey") || !strings.Contains(err.Error(), "update airlock") {
		t.Fatalf("unknown key err = %v", err)
	}
	_, err = PlanImport(target, nil, encodeShare([]byte(`{"aws": {"jumphostTag": "Usage"}}`)), false)
	if err == nil || !strings.Contains(err.Error(), "aws.jumphostTag") {
		t.Fatalf("invalid value err = %v", err)
	}
}

func TestPlanImportMergesByDefault(t *testing.T) {
	shared := encodeShare([]byte(`{"jumphostTag": "Env=bastion", "mcpServerPrefix": "acme", "defaultRegion": "eu-west-3"}`))
	target := writeConfig(t, `{"jumphostTag": "Usage=jumphost", "defaultDbUser": "acme_ro", "mcpServerPrefix": "acme"}`)

	plan, err := PlanImport(target, nil, shared, false)
	if err != nil {
		t.Fatal(err)
	}
	if c := changeOf(t, &plan, "aws.jumphostTag"); c.Kind != ChangeUpdated || c.Old != "Usage=jumphost" || c.OldDefault || c.New != "Env=bastion" {
		t.Fatalf("jumphostTag = %+v", c)
	}
	if c := changeOf(t, &plan, "mcp.serverPrefix"); c.Kind != ChangeUnchanged {
		t.Fatalf("mcpServerPrefix = %+v", c)
	}
	if c := changeOf(t, &plan, "aws.defaultRegion"); c.Kind != ChangeAdded || c.Old != "" || !c.OldDefault || c.New != "eu-west-3" {
		t.Fatalf("defaultRegion = %+v", c)
	}
	for _, c := range plan.Changes {
		if c.Name == "modules.sql.defaultDbUser" {
			t.Fatalf("a merge must not mention local keys absent from the string: %+v", c)
		}
	}

	if err = ApplyImport(&plan); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(target, nil)
	if err != nil || cfg.DefaultDBUser.String() != "acme_ro" || cfg.JumphostTag.String() != "Env=bastion" || cfg.DefaultRegion != "eu-west-3" {
		t.Fatalf("merged cfg = %+v, %v", cfg, err)
	}
}

func TestPlanImportReplaceRemovesLocalKeys(t *testing.T) {
	shared := encodeShare([]byte(`{"defaultRegion": "eu-west-3"}`))
	target := writeConfig(t, `{"defaultDbUser": "acme_ro", "defaultRegion": "eu-west-3"}`)

	plan, err := PlanImport(target, nil, shared, true)
	if err != nil {
		t.Fatal(err)
	}
	if c := changeOf(t, &plan, "modules.sql.defaultDbUser"); c.Kind != ChangeRemoved || c.Old != "acme_ro" || c.New != "" || !c.NewDefault {
		t.Fatalf("defaultDbUser = %+v", c)
	}
	if err = ApplyImport(&plan); err != nil {
		t.Fatal(err)
	}
	settings, err := Settings(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := settingOf(t, settings, "modules.sql.defaultDbUser"); s.Source != SourceDefault {
		t.Fatalf("defaultDbUser = %+v, want removed from the file", s)
	}
}

func TestPlanImportOfChosenKeysLeavesTheOthers(t *testing.T) {
	shared := encodeShare([]byte(`{"jumphostTag": "Env=bastion", "defaultRegion": "eu-west-3"}`))
	target := writeConfig(t, `{"jumphostTag": "Usage=jumphost", "defaultDbUser": "acme_ro"}`)

	plan, err := PlanImport(target, nil, shared, false, "aws.defaultRegion")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Name != "aws.defaultRegion" {
		t.Fatalf("changes = %+v, want only defaultRegion", plan.Changes)
	}
	if err = ApplyImport(&plan); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(target, nil)
	if err != nil || cfg.JumphostTag.String() != "Usage=jumphost" || cfg.DefaultRegion != "eu-west-3" {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
}

func TestPlanImportReplaceKeepsTheKeysLeftOut(t *testing.T) {
	shared := encodeShare([]byte(`{"defaultRegion": "eu-west-3"}`))
	target := writeConfig(t, `{"defaultDbUser": "acme_ro", "mcpServerPrefix": "acme"}`)

	plan, err := PlanImport(target, nil, shared, true, "aws.defaultRegion", "mcp.serverPrefix")
	if err != nil {
		t.Fatal(err)
	}
	if err = ApplyImport(&plan); err != nil {
		t.Fatal(err)
	}
	settings, err := Settings(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := settingOf(t, settings, "modules.sql.defaultDbUser"); s.Source != SourceFile || s.Value != "acme_ro" {
		t.Fatalf("defaultDbUser = %+v, want kept", s)
	}
	if s := settingOf(t, settings, "mcp.serverPrefix"); s.Source != SourceDefault {
		t.Fatalf("mcpServerPrefix = %+v, want removed", s)
	}
}

func TestPlanImportWithNothingToChange(t *testing.T) {
	target := writeConfig(t, `{"defaultRegion": "eu-west-3"}`)
	plan, err := PlanImport(target, nil, encodeShare([]byte(`{"defaultRegion": "eu-west-3"}`)), false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.HasChanges() {
		t.Fatalf("changes = %+v, want nothing to change", plan.Changes)
	}
}

func TestPlanImportRefusesAnInvalidLocalFile(t *testing.T) {
	target := writeConfig(t, `{"jumphostTag": "Usage"}`)
	_, err := PlanImport(target, nil, encodeShare([]byte(`{"defaultRegion": "eu-west-3"}`)), false)
	if err == nil || !strings.Contains(err.Error(), target) {
		t.Fatalf("err = %v, want the invalid local file named", err)
	}
	data, readErr := os.ReadFile(target) // #nosec G304 -- test file under t.TempDir.
	if readErr != nil || string(data) != `{"jumphostTag": "Usage"}` {
		t.Fatalf("local file changed: %q (%v)", data, readErr)
	}
}

func TestImportOfALegacyShareMigratesProductionProfiles(t *testing.T) {
	path := writeConfig(t, `{"defaultRegion": "eu-west-3"}`)
	plan, err := PlanImport(path, nil, encodeShare([]byte(`{"productionProfiles": "*prod*"}`)), false)
	if err != nil {
		t.Fatal(err)
	}
	if c := changeOf(t, &plan, "environments"); c.Kind != ChangeAdded || c.New != "prod!=*prod*" {
		t.Fatalf("environments = %+v, want the old string's productionProfiles added as environments", c)
	}
	if err = ApplyImport(&plan); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "productionProfiles") || !strings.Contains(string(raw), `"environments": "prod!=*prod*"`) {
		t.Fatalf("file = %s", raw)
	}
}

func TestExportEnvironmentsOfALegacyFileConvertsProductionProfiles(t *testing.T) {
	path := writeConfig(t, `{"defaultRegion": "eu-west-3", "productionProfiles": "*prod*,*-live"}`)
	shared, err := Export(path, "environments")
	if err != nil {
		t.Fatal(err)
	}
	target := writeConfig(t, `{}`)
	plan, err := PlanImport(target, nil, shared, false)
	if err != nil {
		t.Fatal(err)
	}
	if c := changeOf(t, &plan, "environments"); c.Kind != ChangeAdded || c.New != "prod!=*prod*,*-live" {
		t.Fatalf("environments = %+v", c)
	}
	if len(plan.Changes) != 1 {
		t.Fatalf("changes = %+v, want environments only", plan.Changes)
	}
}

func TestExportLeavesLanguageOut(t *testing.T) {
	shared, err := Export(writeConfig(t, `{"mcpServerPrefix": "acme", "language": "fr"}`))
	if err != nil {
		t.Fatal(err)
	}
	target := writeConfig(t, `{}`)
	plan, err := PlanImport(target, nil, shared, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range plan.Changes {
		if c.Name == "language" {
			t.Fatalf("the shared string carries language: %+v", c)
		}
	}
}

func TestExportOfOnlyLanguageHasNothingToShare(t *testing.T) {
	if _, err := Export(writeConfig(t, `{"language": "fr"}`)); err == nil {
		t.Fatal("Export of a file setting only language succeeded")
	}
	if _, err := Export(writeConfig(t, `{"mode": "cli", "language": "fr"}`), "language"); err == nil ||
		!strings.Contains(err.Error(), "personal") {
		t.Fatalf("Export(language) = %v, want a personal-setting error", err)
	}
}

func TestImportReplaceKeepsTheLocalLanguage(t *testing.T) {
	shared, err := Export(writeConfig(t, `{"mcpServerPrefix": "acme"}`))
	if err != nil {
		t.Fatal(err)
	}
	target := writeConfig(t, `{"language": "fr", "mode": "daemon"}`)
	plan, err := PlanImport(target, nil, shared, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = ApplyImport(&plan); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Language != "fr" || cfg.Mode != ModeCLI {
		t.Fatalf("after import --replace: language %q mode %q; want fr and cli", cfg.Language, cfg.Mode)
	}
	for _, c := range plan.Changes {
		if c.Name == "language" {
			t.Fatalf("the plan lists language: %+v", c)
		}
	}
}

func TestImportIgnoresAShareCarryingLanguage(t *testing.T) {
	shared := encodeShare([]byte(`{"mcpServerPrefix": "acme", "language": "fr"}`))
	target := writeConfig(t, `{}`)
	plan, err := PlanImport(target, nil, shared, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = ApplyImport(&plan); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Language != LanguageAuto {
		t.Fatalf("language = %q, want auto", cfg.Language)
	}
}

func TestImportOfAnAirlock1StringRenamesItsKeys(t *testing.T) {
	shared := sharecode.Encode("airlock1:", []byte(`{"mcpGatewayPort": "8123", "disabledModules": "k8s", "language": "fr"}`))
	target := writeConfig(t, `{}`)
	plan, err := PlanImport(target, nil, shared, false)
	if err != nil {
		t.Fatal(err)
	}
	if c := changeOf(t, &plan, "mcp.gateway.port"); c.Kind != ChangeAdded || c.New != "8123" {
		t.Fatalf("mcp.gateway.port = %+v", c)
	}
	if c := changeOf(t, &plan, "modules.k8s.enabled"); c.Kind != ChangeAdded || c.New != "false" {
		t.Fatalf("modules.k8s.enabled = %+v", c)
	}
	if err = ApplyImport(&plan); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(target) // #nosec G304 -- test file under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"mcp\": {\n    \"gateway\": {\n      \"port\": 8123\n    }\n  },\n  \"modules\": {\n    \"k8s\": {\n      \"enabled\": false\n    }\n  }\n}\n"
	if string(raw) != want {
		t.Fatalf("file = %q, want %q", raw, want)
	}
}
