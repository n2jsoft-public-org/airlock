package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/aiclient"
	"github.com/n2jsoft-public-org/airlock/internal/config"
	"github.com/n2jsoft-public-org/airlock/internal/ui"
	"github.com/spf13/pflag"
)

// settingsFlags is a flag set of the root command's setting flags, parsed from args.
func settingsFlags(t *testing.T, args ...string) *pflag.FlagSet {
	t.Helper()
	flags := pflag.NewFlagSet("airlock", pflag.ContinueOnError)
	for _, name := range []string{"jumphost-tag", "region", "db-user", "mcp-server-prefix", "mcp-clients", "mode", "read-only-secret-tag"} {
		flags.String(name, "", "")
	}
	if err := flags.Parse(args); err != nil {
		t.Fatal(err)
	}
	return flags
}

func snapshotByName(t *testing.T, store ui.SettingsStore) (snapshot ui.SettingsSnapshot, byName map[string]ui.SettingView) {
	t.Helper()
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	byName = map[string]ui.SettingView{}
	for i := range snapshot.Settings {
		byName[snapshot.Settings[i].Name] = snapshot.Settings[i]
	}
	return snapshot, byName
}

func TestConfigSettingsStoreSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"defaultDbUser": "app_ro", "environments": "prod!=*prod*,*-live"}`)
	store := newConfigSettingsStore(path, nil, settingsFlags(t, "--jumphost-tag", "Env=bastion"))
	snapshot, views := snapshotByName(t, store)

	names := make([]string, 0, len(snapshot.Settings))
	for _, view := range snapshot.Settings {
		names = append(names, view.Name)
	}
	want := make([]string, 0, len(config.Keys))
	for _, k := range config.Keys {
		want = append(want, k.Name)
	}
	if !slices.Equal(names, want) {
		t.Fatalf("keys = %v, want config.Keys order %v", names, want)
	}
	if v := views["modules.sql.defaultDbUser"]; !v.FromFile || v.Value != "app_ro" || v.Description == "" {
		t.Fatalf("modules.sql.defaultDbUser = %+v", v)
	}
	if v := views["aws.jumphostTag"]; v.FromFile || v.Value != "Usage=jumphost" || v.OverriddenBy != "--jumphost-tag" {
		t.Fatalf("aws.jumphostTag = %+v, want the default overridden by the flag", v)
	}
	if v := views["aws.defaultRegion"]; v.Kind != ui.KindRegion || v.OverriddenBy != "" {
		t.Fatalf("aws.defaultRegion = %+v", v)
	}
	if v := views["mode"]; !slices.Equal(optionValues(&v), []string{"cli", "daemon"}) || !v.NextLaunch {
		t.Fatalf("mode = %+v", v)
	}
	if snapshot.Path != path || snapshot.Environments.String() != "prod!=*prod*,*-live" {
		t.Fatalf("path = %q environments = %q", snapshot.Path, snapshot.Environments.String())
	}
}

func TestConfigSettingsStoreSkipsTheDeprecatedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"productionProfiles": "*prod*"}`)
	snapshot, err := newConfigSettingsStore(path, nil, settingsFlags(t)).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range snapshot.Settings {
		if v.Name == "productionProfiles" {
			t.Fatal("the Settings screen lists the deprecated key, whose edit is refused")
		}
		if v.Name == "environments" && (v.Value != "prod!=*prod*" || !v.FromFile) {
			t.Fatalf("environments = %+v, want the migrated value from the file", v)
		}
	}
	if !snapshot.Environments.Match("team-prod").Production {
		t.Fatal("the badges must follow the migrated productionProfiles")
	}
}

func TestConfigSettingsStoreSetAndUnset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := newConfigSettingsStore(path, nil, settingsFlags(t))
	if err := store.Set("aws.jumphostTag", "Env=bastion"); err != nil {
		t.Fatal(err)
	}
	if _, views := snapshotByName(t, store); views["aws.jumphostTag"].Value != "Env=bastion" || !views["aws.jumphostTag"].FromFile {
		t.Fatalf("aws.jumphostTag = %+v, want the written value", views["aws.jumphostTag"])
	}
	if err := store.Unset("aws.jumphostTag"); err != nil {
		t.Fatal(err)
	}
	if _, views := snapshotByName(t, store); views["aws.jumphostTag"].FromFile {
		t.Fatalf("aws.jumphostTag = %+v, want its default back", views["aws.jumphostTag"])
	}
}

func TestConfigSettingsStoreRefusesAnInvalidValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"jumphostTag": "Env=bastion"}`)
	store := newConfigSettingsStore(path, nil, settingsFlags(t))
	if err := store.Set("aws.jumphostTag", "bastion"); err == nil {
		t.Fatal("a tag without = must be refused")
	}
	if _, views := snapshotByName(t, store); views["aws.jumphostTag"].Value != "Env=bastion" {
		t.Fatalf("aws.jumphostTag = %+v, want the file untouched", views["aws.jumphostTag"])
	}
}

func TestConfigSettingsStoreNamesAnInvalidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"mode": "sometimes"}`)
	if _, err := newConfigSettingsStore(path, nil, settingsFlags(t)).Snapshot(); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want the file named", err)
	}
}

func TestSettingsStoreOffersTheGatewayChoices(t *testing.T) {
	store := newConfigSettingsStore(filepath.Join(t.TempDir(), "config.json"), nil, pflag.NewFlagSet("t", pflag.ContinueOnError))
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	views := map[string]ui.SettingView{}
	for _, view := range snapshot.Settings {
		views[view.Name] = view
	}
	if got := optionValues(new(views["agent.autoConnect"])); len(got) != 3 || got[0] != "none" {
		t.Fatalf("agent.autoConnect choices = %v", got)
	}
	if !views["mcp.gateway.enabled"].NextLaunch || !views["mcp.gateway.port"].NextLaunch || views["agent.writes"].NextLaunch {
		t.Fatalf("next launch flags = %+v", views)
	}
}

func TestSnapshotOffersTheLanguages(t *testing.T) {
	store := newConfigSettingsStore(filepath.Join(t.TempDir(), "config.json"), nil, settingsFlags(t))
	_, views := snapshotByName(t, store)
	view, ok := views["language"]
	if !ok {
		t.Fatal("no language setting in the snapshot")
	}
	if !slices.Equal(optionValues(&view), []string{"auto", "en", "fr"}) || !view.NextLaunch {
		t.Fatalf("language view = %+v, want choices auto/en/fr applied at next launch", view)
	}
}

func TestSettingsStoreSnapshotMarksLockedAndOrganization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"aws": {"jumphostTag": "Env=local"}}`)
	org := testOrganization(t, `{"settings":{"aws":{"jumphostTag":"Role=bastion"},"mode":"daemon"},"locked":["aws.jumphostTag"]}`)
	store := newConfigSettingsStore(path, org, settingsFlags(t))
	snapshot, views := snapshotByName(t, store)
	if snapshot.Organization != "org.json" {
		t.Fatalf("organization = %q, want the source", snapshot.Organization)
	}
	if v := views["aws.jumphostTag"]; !v.Locked || !v.FromOrganization || v.FromFile || v.Value != "Role=bastion" {
		t.Fatalf("aws.jumphostTag = %+v, want the locked organization value", v)
	}
	if v := views["mode"]; v.Locked || !v.FromOrganization || v.Value != "daemon" {
		t.Fatalf("mode = %+v, want the unlocked organization value", v)
	}
	if v := views["aws.defaultRegion"]; v.Locked || v.FromOrganization {
		t.Fatalf("aws.defaultRegion = %+v, want a default", v)
	}
	if err := store.Set("aws.jumphostTag", "Env=mine"); !errors.Is(err, config.ErrLocked) {
		t.Fatalf("Set a locked key: err = %v, want ErrLocked", err)
	}
	if err := store.Unset("aws.jumphostTag"); !errors.Is(err, config.ErrLocked) {
		t.Fatalf("Unset a locked key: err = %v, want ErrLocked", err)
	}
	if snapshot, _ = snapshotByName(t, newConfigSettingsStore(path, nil, settingsFlags(t))); snapshot.Organization != "" {
		t.Fatalf("organization without one = %q", snapshot.Organization)
	}
}

func TestSettingsStoreRefusesOrganizationConfigWhenManaged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	orgFile := writeTempFile(t, "org.json", testOrgBody)
	useManagedFile(t, `{"organizationConfig": "`+orgFile+`"}`)
	store := newConfigSettingsStore(path, nil, settingsFlags(t))
	store.managed = true
	managed := orgEnvironment().managedPath
	if err := store.Set("organizationConfig", orgFile); err == nil || !strings.Contains(err.Error(), managed) {
		t.Fatalf("Set: err = %v, want the managed file named", err)
	}
	if err := store.Unset("organizationConfig"); err == nil || !strings.Contains(err.Error(), managed) {
		t.Fatalf("Unset: err = %v, want the managed file named", err)
	}
	if err := store.Validate("organizationConfig", orgFile); err == nil || !strings.Contains(err.Error(), managed) {
		t.Fatalf("Validate: err = %v, want the managed file named", err)
	}
	if err := store.Set("mode", "daemon"); err != nil {
		t.Fatalf("Set another key: %v", err)
	}
}

func optionValues(view *ui.SettingView) []string {
	values := make([]string, 0, len(view.Options))
	for _, option := range view.Options {
		values = append(values, option.Value)
	}
	return values
}

func TestSnapshotDescribesEveryKey(t *testing.T) {
	store := newConfigSettingsStore(filepath.Join(t.TempDir(), "config.json"), nil, settingsFlags(t))
	snapshot, views := snapshotByName(t, store)
	if len(snapshot.Settings) != len(config.Keys) {
		t.Fatalf("%d views, want one per key (%d)", len(snapshot.Settings), len(config.Keys))
	}
	for _, view := range snapshot.Settings {
		if view.Label == "" || view.Label == "config.label."+view.Name || view.Section == "" {
			t.Errorf("%s: label %q section %q, want both set", view.Name, view.Label, view.Section)
		}
	}
	for _, view := range snapshot.Settings {
		if !slices.Contains(ui.SettingSections, view.Section) {
			t.Errorf("%s: section %q is none of the Settings screen's", view.Name, view.Section)
		}
	}
	if v := views["mcp.gateway.port"]; v.Section != "mcp" {
		t.Fatalf("mcp.gateway.port: section %q, want mcp", v.Section)
	}
	if v := views["mcp.clients"]; v.Kind != ui.KindClients || v.Section != "mcp" {
		t.Fatalf("mcp.clients = kind %v section %q", v.Kind, v.Section)
	}
	if v := views["modules.sql.readOnlySecretTag"]; v.Kind != ui.KindTag || !v.Optional || v.Section != "modules.sql" {
		t.Fatalf("modules.sql.readOnlySecretTag = %+v", v)
	}
	if v := views["agent.idleTimeout"]; v.Kind != ui.KindDuration || !slices.Equal(optionValues(&v), []string{"15m", "30m", "1h", "4h"}) {
		t.Fatalf("agent.idleTimeout = %+v", v)
	}
	if v := views["language"]; v.Kind != ui.KindChoice || v.Section != "general" || v.Options[1].Label == "" {
		t.Fatalf("language = %+v", v)
	}
	if v := views["agent.writes"]; v.Kind != ui.KindToggle || len(v.Options) != 0 {
		t.Fatalf("agent.writes = %+v, want a toggle without options", v)
	}
}

func TestSettingsStoreValidate(t *testing.T) {
	store := newConfigSettingsStore(filepath.Join(t.TempDir(), "config.json"), nil, settingsFlags(t))
	if err := store.Validate("mcp.gateway.port", "7822"); err != nil {
		t.Fatal(err)
	}
	if err := store.Validate("mcp.gateway.port", "70000"); err == nil {
		t.Fatal("port 70000 accepted")
	}
}

func TestSnapshotDetectedClients(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".cursor"), 0o750); err != nil {
		t.Fatal(err)
	}
	store := newConfigSettingsStore(filepath.Join(t.TempDir(), "config.json"), nil, settingsFlags(t))
	store.projectDir = dir
	snapshot, _ := snapshotByName(t, store)
	if want := []aiclient.Client{aiclient.Cursor}; !slices.Equal(snapshot.DetectedClients, want) {
		t.Fatalf("DetectedClients = %v, want %v", snapshot.DetectedClients, want)
	}

	store.projectDir = ""
	if snapshot, _ = snapshotByName(t, store); snapshot.DetectedClients != nil {
		t.Fatalf("unknown directory: DetectedClients = %v, want nil", snapshot.DetectedClients)
	}
}
