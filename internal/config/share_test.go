package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if !strings.HasPrefix(shared, "airlock1:") || strings.ContainsAny(shared, " \n{}\"") {
		t.Fatalf("shared = %q, want one opaque airlock1: string", shared)
	}

	target := filepath.Join(t.TempDir(), "airlock", "config.json")
	plan, err := PlanImport(target, shared, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 2 || !plan.HasChanges() {
		t.Fatalf("changes = %+v, want the two exported keys", plan.Changes)
	}
	if c := changeOf(t, &plan, "mcpServerPrefix"); c.Kind != ChangeAdded || c.Old != "airlock" || !c.OldDefault || c.New != "acme" || c.NewDefault {
		t.Fatalf("mcpServerPrefix change = %+v", c)
	}
	if err = ApplyImport(&plan); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(target)
	if err != nil || cfg.MCPServerPrefix != "acme" || cfg.DefaultRegion != "eu-west-3" {
		t.Fatalf("imported cfg = %+v, %v", cfg, err)
	}
}

func TestExportKeepsTheNamedKeys(t *testing.T) {
	source := writeConfig(t, `{"mcpServerPrefix": "acme", "defaultRegion": "eu-west-3", "mode": "daemon"}`)
	shared, err := Export(source, "defaultRegion", "mode")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanImport(filepath.Join(t.TempDir(), "config.json"), shared, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 2 {
		t.Fatalf("changes = %+v, want defaultRegion and mode only", plan.Changes)
	}
	if changeOf(t, &plan, "defaultRegion").New != "eu-west-3" || changeOf(t, &plan, "mode").New != "daemon" {
		t.Fatalf("changes = %+v", plan.Changes)
	}
}

func TestExportRefusesKeysNotInTheFile(t *testing.T) {
	source := writeConfig(t, `{"mcpServerPrefix": "acme"}`)
	if _, err := Export(source, "defaultRegion"); err == nil || !strings.Contains(err.Error(), "does not set defaultRegion") {
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
		"wrong prefix": "airlock2:" + strings.TrimPrefix(shared, "airlock1:"),
		"not base64":   "airlock1:!!!",
		"empty":        "airlock1:",
		"not a string": "hello",
	} {
		if _, err := PlanImport(target, s, false); !errors.Is(err, ErrInvalidShare) {
			t.Fatalf("%s: err = %v, want ErrInvalidShare", name, err)
		}
	}
	if _, err := PlanImport(target, "  "+shared+"\n", false); err != nil {
		t.Fatalf("surrounding spaces from a paste must be accepted: %v", err)
	}
}

func TestImportRejectsUnknownKeysAndInvalidValues(t *testing.T) {
	target := filepath.Join(t.TempDir(), "config.json")
	_, err := PlanImport(target, encodeShare([]byte(`{"futureKey": "x"}`)), false)
	if err == nil || !strings.Contains(err.Error(), "futureKey") || !strings.Contains(err.Error(), "update airlock") {
		t.Fatalf("unknown key err = %v", err)
	}
	_, err = PlanImport(target, encodeShare([]byte(`{"jumphostTag": "Usage"}`)), false)
	if err == nil || !strings.Contains(err.Error(), "jumphostTag") {
		t.Fatalf("invalid value err = %v", err)
	}
}

func TestPlanImportMergesByDefault(t *testing.T) {
	shared := encodeShare([]byte(`{"jumphostTag": "Env=bastion", "mcpServerPrefix": "acme", "defaultRegion": "eu-west-3"}`))
	target := writeConfig(t, `{"jumphostTag": "Usage=jumphost", "defaultDbUser": "acme_ro", "mcpServerPrefix": "acme"}`)

	plan, err := PlanImport(target, shared, false)
	if err != nil {
		t.Fatal(err)
	}
	if c := changeOf(t, &plan, "jumphostTag"); c.Kind != ChangeUpdated || c.Old != "Usage=jumphost" || c.OldDefault || c.New != "Env=bastion" {
		t.Fatalf("jumphostTag = %+v", c)
	}
	if c := changeOf(t, &plan, "mcpServerPrefix"); c.Kind != ChangeUnchanged {
		t.Fatalf("mcpServerPrefix = %+v", c)
	}
	if c := changeOf(t, &plan, "defaultRegion"); c.Kind != ChangeAdded || c.Old != "" || !c.OldDefault || c.New != "eu-west-3" {
		t.Fatalf("defaultRegion = %+v", c)
	}
	for _, c := range plan.Changes {
		if c.Name == "defaultDbUser" {
			t.Fatalf("a merge must not mention local keys absent from the string: %+v", c)
		}
	}

	if err = ApplyImport(&plan); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(target)
	if err != nil || cfg.DefaultDBUser.String() != "acme_ro" || cfg.JumphostTag.String() != "Env=bastion" || cfg.DefaultRegion != "eu-west-3" {
		t.Fatalf("merged cfg = %+v, %v", cfg, err)
	}
}

func TestPlanImportReplaceRemovesLocalKeys(t *testing.T) {
	shared := encodeShare([]byte(`{"defaultRegion": "eu-west-3"}`))
	target := writeConfig(t, `{"defaultDbUser": "acme_ro", "defaultRegion": "eu-west-3"}`)

	plan, err := PlanImport(target, shared, true)
	if err != nil {
		t.Fatal(err)
	}
	if c := changeOf(t, &plan, "defaultDbUser"); c.Kind != ChangeRemoved || c.Old != "acme_ro" || c.New != "" || !c.NewDefault {
		t.Fatalf("defaultDbUser = %+v", c)
	}
	if err = ApplyImport(&plan); err != nil {
		t.Fatal(err)
	}
	settings, err := Settings(target)
	if err != nil {
		t.Fatal(err)
	}
	if s := settingOf(t, settings, "defaultDbUser"); s.Source != SourceDefault {
		t.Fatalf("defaultDbUser = %+v, want removed from the file", s)
	}
}

func TestPlanImportWithNothingToChange(t *testing.T) {
	target := writeConfig(t, `{"defaultRegion": "eu-west-3"}`)
	plan, err := PlanImport(target, encodeShare([]byte(`{"defaultRegion": "eu-west-3"}`)), false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.HasChanges() {
		t.Fatalf("changes = %+v, want nothing to change", plan.Changes)
	}
}

func TestPlanImportRefusesAnInvalidLocalFile(t *testing.T) {
	target := writeConfig(t, `{"jumphostTag": "Usage"}`)
	_, err := PlanImport(target, encodeShare([]byte(`{"defaultRegion": "eu-west-3"}`)), false)
	if err == nil || !strings.Contains(err.Error(), target) {
		t.Fatalf("err = %v, want the invalid local file named", err)
	}
	data, readErr := os.ReadFile(target) // #nosec G304 -- test file under t.TempDir.
	if readErr != nil || string(data) != `{"jumphostTag": "Usage"}` {
		t.Fatalf("local file changed: %q (%v)", data, readErr)
	}
}
