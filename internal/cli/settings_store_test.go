package cli

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/config"
	"github.com/n2jsoft/airlock/internal/ui"
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
	for _, view := range snapshot.Settings {
		byName[view.Name] = view
	}
	return snapshot, byName
}

func TestConfigSettingsStoreSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"defaultDbUser": "app_ro", "environments": "prod!=*prod*,*-live"}`)
	store := newConfigSettingsStore(path, settingsFlags(t, "--jumphost-tag", "Env=bastion"))
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
	if v := views["defaultDbUser"]; !v.FromFile || v.Value != "app_ro" || v.Description == "" {
		t.Fatalf("defaultDbUser = %+v", v)
	}
	if v := views["jumphostTag"]; v.FromFile || v.Value != "Usage=jumphost" || v.OverriddenBy != "--jumphost-tag" {
		t.Fatalf("jumphostTag = %+v, want the default overridden by the flag", v)
	}
	if v := views["defaultRegion"]; !v.Region || v.OverriddenBy != "" {
		t.Fatalf("defaultRegion = %+v", v)
	}
	if v := views["mode"]; !slices.Equal(v.Choices, []string{"cli", "daemon"}) || !v.NextLaunch {
		t.Fatalf("mode = %+v", v)
	}
	if snapshot.Path != path || snapshot.Environments.String() != "prod!=*prod*,*-live" {
		t.Fatalf("path = %q environments = %q", snapshot.Path, snapshot.Environments.String())
	}
}

func TestConfigSettingsStoreSkipsTheDeprecatedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"productionProfiles": "*prod*"}`)
	snapshot, err := newConfigSettingsStore(path, settingsFlags(t)).Snapshot()
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
	store := newConfigSettingsStore(path, settingsFlags(t))
	if err := store.Set("jumphostTag", "Env=bastion"); err != nil {
		t.Fatal(err)
	}
	if _, views := snapshotByName(t, store); views["jumphostTag"].Value != "Env=bastion" || !views["jumphostTag"].FromFile {
		t.Fatalf("jumphostTag = %+v, want the written value", views["jumphostTag"])
	}
	if err := store.Unset("jumphostTag"); err != nil {
		t.Fatal(err)
	}
	if _, views := snapshotByName(t, store); views["jumphostTag"].FromFile {
		t.Fatalf("jumphostTag = %+v, want its default back", views["jumphostTag"])
	}
}

func TestConfigSettingsStoreRefusesAnInvalidValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"jumphostTag": "Env=bastion"}`)
	store := newConfigSettingsStore(path, settingsFlags(t))
	if err := store.Set("jumphostTag", "bastion"); err == nil {
		t.Fatal("a tag without = must be refused")
	}
	if _, views := snapshotByName(t, store); views["jumphostTag"].Value != "Env=bastion" {
		t.Fatalf("jumphostTag = %+v, want the file untouched", views["jumphostTag"])
	}
}

func TestConfigSettingsStoreNamesAnInvalidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, `{"mode": "sometimes"}`)
	if _, err := newConfigSettingsStore(path, settingsFlags(t)).Snapshot(); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want the file named", err)
	}
}

func TestSettingsStoreOffersTheGatewayChoices(t *testing.T) {
	store := newConfigSettingsStore(filepath.Join(t.TempDir(), "config.json"), pflag.NewFlagSet("t", pflag.ContinueOnError))
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	views := map[string]ui.SettingView{}
	for _, view := range snapshot.Settings {
		views[view.Name] = view
	}
	if got := views["agentAutoConnect"].Choices; len(got) != 3 || got[0] != "none" {
		t.Fatalf("agentAutoConnect choices = %v", got)
	}
	if !views["mcpGateway"].NextLaunch || !views["mcpGatewayPort"].NextLaunch || views["agentWrites"].NextLaunch {
		t.Fatalf("next launch flags = %+v", views)
	}
}

func TestSnapshotOffersTheLanguages(t *testing.T) {
	store := newConfigSettingsStore(filepath.Join(t.TempDir(), "config.json"), settingsFlags(t))
	_, views := snapshotByName(t, store)
	view, ok := views["language"]
	if !ok {
		t.Fatal("no language setting in the snapshot")
	}
	if !slices.Equal(view.Choices, []string{"auto", "en", "fr"}) || !view.NextLaunch {
		t.Fatalf("language view = %+v, want choices auto/en/fr applied at next launch", view)
	}
}
