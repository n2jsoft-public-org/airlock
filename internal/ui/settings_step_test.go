package ui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// fakeSettingsStore keeps a snapshot in memory and records the writes.
type fakeSettingsStore struct {
	setErr   error
	calls    []string
	snapshot SettingsSnapshot
}

func newFakeSettingsStore() *fakeSettingsStore {
	return &fakeSettingsStore{snapshot: testSettings()}
}

func (s *fakeSettingsStore) Snapshot() (SettingsSnapshot, error) {
	return SettingsSnapshot{
		Path:               s.snapshot.Path,
		Settings:           slices.Clone(s.snapshot.Settings),
		ProductionProfiles: s.snapshot.ProductionProfiles,
	}, nil
}

func (s *fakeSettingsStore) Set(key, value string) error {
	s.calls = append(s.calls, "set "+key+"="+value)
	if s.setErr != nil {
		return s.setErr
	}
	for idx := range s.snapshot.Settings {
		if s.snapshot.Settings[idx].Name == key {
			s.snapshot.Settings[idx].Value, s.snapshot.Settings[idx].FromFile = value, true
		}
	}
	return nil
}

func (s *fakeSettingsStore) Unset(key string) error {
	s.calls = append(s.calls, "unset "+key)
	for idx := range s.snapshot.Settings {
		if s.snapshot.Settings[idx].Name == key {
			s.snapshot.Settings[idx].Value, s.snapshot.Settings[idx].FromFile = "", false
		}
	}
	return nil
}

// settingsWizard opens a wizard with a history and store on its start screen.
func settingsWizard(t *testing.T, store SettingsStore) jumphostWizardModel {
	t.Helper()
	m := newJumphostWizardModel(context.Background(), newHistoryRunner(savedArchive), jumphost.Options{})
	m.settings = store
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	return runChain(t, m, m.loadSavedCmd(), 1)
}

// pressWizard sends msg, then runs the commands it leads to, one level deep.
func pressWizard(t *testing.T, m jumphostWizardModel, msg tea.Msg) jumphostWizardModel {
	t.Helper()
	m, cmd := wizardUpdate(t, m, msg)
	return runChain(t, m, cmd, 1)
}

var escKey = key("esc")

func TestWizardStartScreenOpensSettings(t *testing.T) {
	m := settingsWizard(t, newFakeSettingsStore())
	if view := wizardView(m); !strings.Contains(view, "⚙ Settings") {
		t.Fatalf("start screen lacks the Settings row:\n%s", view)
	}
	m = pressWizard(t, m, key(","))
	if m.step != jumphostStepSettings || m.loading {
		t.Fatalf("step = %v loading = %v, want the Settings screen", m.step, m.loading)
	}
	view := wizardView(m)
	for _, want := range []string{"Settings", "jumphostTag", "Usage=jumphost", "/home/me/.config/airlock/config.json"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
}

func TestWizardSettingsActionRowOpensSettings(t *testing.T) {
	m := settingsWizard(t, newFakeSettingsStore())
	// Past the last entry: New connection, Search a target, then Settings.
	for range 3 {
		m, _ = wizardUpdate(t, m, key("j"))
	}
	if m = pressWizard(t, m, key("enter")); m.step != jumphostStepSettings {
		t.Fatalf("step = %v, want the Settings screen", m.step)
	}
}

func TestWizardWithoutSettingsStoreHidesSettings(t *testing.T) {
	m := settingsWizard(t, nil)
	if view := wizardView(m); strings.Contains(view, "Settings") {
		t.Fatalf("the Settings row shows without a store:\n%s", view)
	}
	if m = pressWizard(t, m, key(",")); m.step != jumphostStepStart || m.recent.typing() {
		t.Fatalf("step = %v filtering = %v, want , to do nothing", m.step, m.recent.typing())
	}
}

func TestWizardCommaFiltersWhileFilteringRecent(t *testing.T) {
	m := settingsWizard(t, newFakeSettingsStore())
	m, _ = wizardUpdate(t, m, key("/"))
	m, _ = wizardUpdate(t, m, key(","))
	if m.step != jumphostStepStart || m.recent.list.FilterValue() != "," {
		t.Fatalf("step = %v filter = %q, want , typed in the filter", m.step, m.recent.list.FilterValue())
	}
}

func TestWizardSettingsSaveWritesAndReloads(t *testing.T) {
	store := newFakeSettingsStore()
	m := pressWizard(t, settingsWizard(t, store), key(","))
	m, _ = wizardUpdate(t, m, key("enter"))
	m, _ = wizardUpdate(t, m, key("Env=bastion"))
	m = pressWizard(t, m, key("enter"))
	if !slices.Equal(store.calls, []string{"set jumphostTag=Env=bastion"}) {
		t.Fatalf("calls = %v", store.calls)
	}
	if m.loading || m.settingsEditor.editing {
		t.Fatalf("loading = %v editing = %v, want the edit closed", m.loading, m.settingsEditor.editing)
	}
	if view := wizardView(m); !strings.Contains(view, "Env=bastion") {
		t.Fatalf("view lacks the saved value:\n%s", view)
	}
}

func TestWizardSettingsRefusedValueKeepsTheField(t *testing.T) {
	store := newFakeSettingsStore()
	store.setErr = errors.New("invalid tag \"bastion\": want Key=Value")
	m := pressWizard(t, settingsWizard(t, store), key(","))
	m, _ = wizardUpdate(t, m, key("enter"))
	m, _ = wizardUpdate(t, m, key("bastion"))
	m = pressWizard(t, m, key("enter"))
	if !m.settingsEditor.editing || m.settingsEditor.input.Value() != "bastion" || m.loading {
		t.Fatalf("editing = %v value = %q loading = %v", m.settingsEditor.editing, m.settingsEditor.input.Value(), m.loading)
	}
	if view := wizardView(m); !strings.Contains(view, "want Key=Value") {
		t.Fatalf("view lacks the refusal:\n%s", view)
	}
}

func TestWizardSettingsQIsTextWhileEditing(t *testing.T) {
	m := pressWizard(t, settingsWizard(t, newFakeSettingsStore()), key(","))
	m, _ = wizardUpdate(t, m, key("enter"))
	m, cmd := wizardUpdate(t, m, key("q"))
	if m.canceled || m.step != jumphostStepSettings || m.settingsEditor.input.Value() != "q" {
		t.Fatalf("canceled = %v step = %v value = %q, want q typed", m.canceled, m.step, m.settingsEditor.input.Value())
	}
	if cmd != nil {
		if _, done := runWithTimeout(cmd).(jumphostWizardDoneMsg); done {
			t.Fatal("q finished the wizard while editing")
		}
	}
}

func TestWizardSettingsResetUnsets(t *testing.T) {
	store := newFakeSettingsStore()
	m := pressWizard(t, settingsWizard(t, store), key(","))
	m, _ = wizardUpdate(t, m, key("j"))
	m, _ = wizardUpdate(t, m, key("d"))
	_ = pressWizard(t, m, key("y"))
	if !slices.Equal(store.calls, []string{"unset defaultDbUser"}) {
		t.Fatalf("calls = %v", store.calls)
	}
}

func TestWizardSettingsRegionPickerSavesTheRegion(t *testing.T) {
	store := newFakeSettingsStore()
	m := pressWizard(t, settingsWizard(t, store), key(","))
	m, _ = wizardUpdate(t, m, key("j"))
	m, _ = wizardUpdate(t, m, key("j"))
	m, _ = wizardUpdate(t, m, key("enter"))
	if !m.pickingRegion || !m.regionForSettings {
		t.Fatalf("picking = %v for settings = %v, want the region picker", m.pickingRegion, m.regionForSettings)
	}
	m, _ = wizardUpdate(t, m, key("paris"))
	m = pressWizard(t, m, key("enter"))
	if m.pickingRegion || m.regionForSettings || !slices.Equal(store.calls, []string{"set defaultRegion=eu-west-3"}) {
		t.Fatalf("picking = %v calls = %v, want eu-west-3 saved", m.pickingRegion, store.calls)
	}
	if m.options.AwsRegion != "" {
		t.Fatalf("region of the connection = %q, want it untouched", m.options.AwsRegion)
	}
}

func TestWizardSettingsFollowProductionProfiles(t *testing.T) {
	m := pressWizard(t, settingsWizard(t, newFakeSettingsStore()), key(","))
	if !m.production.matches("acme-prod") {
		t.Fatal("the PROD badges do not follow the snapshot's productionProfiles")
	}
}

func TestWizardSettingsEscReturnsToStart(t *testing.T) {
	m := pressWizard(t, settingsWizard(t, newFakeSettingsStore()), key(","))
	m = pressWizard(t, m, escKey)
	if m.step != jumphostStepStart || m.loading {
		t.Fatalf("step = %v loading = %v, want the start screen reloaded", m.step, m.loading)
	}
}

func TestWizardTypeStepOpensSettings(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})
	m.settings = newFakeSettingsStore()
	if m = pressWizard(t, m, key(",")); m.step != jumphostStepSettings {
		t.Fatalf("step = %v, want the Settings screen", m.step)
	}
	if m = pressWizard(t, m, escKey); m.step != jumphostStepType {
		t.Fatalf("step = %v, want the Type step back", m.step)
	}
}

// feedDashboard runs cmd and gives its messages to m, batches included, without following the
// commands they lead to.
func feedDashboard(t *testing.T, m ssmModel, cmd tea.Cmd) ssmModel {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch msg := runWithTimeout(cmd).(type) {
	case tea.BatchMsg:
		for _, inner := range msg {
			m = feedDashboard(t, m, inner)
		}
	case nil, loadingTickMsg:
		// Spinner ticks only animate.
	default:
		m, _ = update(t, m, msg)
	}
	return m
}

// dashboardSettings opens the Settings screen from a dashboard running one session.
func dashboardSettings(t *testing.T, store SettingsStore) ssmModel {
	t.Helper()
	m, _ := newSessionsModel(t, "db")
	m.settings = store
	m, _ = update(t, m, key(","))
	m, cmd := update(t, m, jumphostSavedLoadedMsg{})
	return feedDashboard(t, m, cmd)
}

func TestDashboardCommaOpensSettings(t *testing.T) {
	m := dashboardSettings(t, newFakeSettingsStore())
	if m.mode != ssmModeWizard || m.wizard.step != jumphostStepSettings || m.wizard.loading {
		t.Fatalf("mode = %v step = %v loading = %v, want the Settings screen", m.mode, m.wizard.step, m.wizard.loading)
	}
	m, cmd := update(t, m, escKey)
	if m = feedDashboard(t, m, cmd); m.mode != ssmModeSessions {
		t.Fatalf("mode = %v, want esc back on the sessions", m.mode)
	}
}

func TestDashboardFollowsTheProductionProfilesOfSettings(t *testing.T) {
	if m := dashboardSettings(t, newFakeSettingsStore()); !m.production.matches("acme-prod") {
		t.Fatal("the session cards do not follow the snapshot's productionProfiles")
	}
}

func TestDashboardWithoutSettingsIgnoresComma(t *testing.T) {
	m, _ := newSessionsModel(t, "db")
	if m, _ = update(t, m, key(",")); m.mode != ssmModeSessions {
		t.Fatalf("mode = %v, want , to do nothing without a store", m.mode)
	}
}

func TestDashboardHelpListsSettings(t *testing.T) {
	m, _ := newSessionsModel(t, "db")
	has := func(m ssmModel) bool {
		for _, group := range m.helpGroups() {
			for _, k := range group.keys {
				if k[0] == "," {
					return true
				}
			}
		}
		return false
	}
	if has(m) {
		t.Fatal("the help lists , without a store")
	}
	m.settings = newFakeSettingsStore()
	if !has(m) {
		t.Fatal("the help lacks ,")
	}
}

func TestDashboardPassesSettingsToWizard(t *testing.T) {
	store := newFakeSettingsStore()
	m, _ := newSessionsModel(t)
	m.settings = store
	if m = m.showWizard(jumphost.Options{}); m.wizard.settings != store {
		t.Fatal("the wizard did not get the dashboard's settings")
	}
}

// regionRunner resolves the default region from region, which a test changes on the way.
type regionRunner struct {
	historyRunner
	region *string
}

func (r regionRunner) ResolveRegion(context.Context, string) string { return *r.region }

func TestWizardSettingsNewDefaultRegionAppliesOnTheTypeStep(t *testing.T) {
	region := "eu-west-1"
	m := newTestWizard(t, regionRunner{historyRunner: newHistoryRunner(), region: &region}, jumphost.Options{})
	m.settings = newFakeSettingsStore()
	m = runCmd(t, m, m.Init())
	if got := m.currentRegion(); got != "eu-west-1" {
		t.Fatalf("region = %q, want eu-west-1 before the change", got)
	}
	m = pressWizard(t, m, key(","))
	region = "eu-west-2"
	m, cmd := wizardUpdate(t, m, escKey)
	if m = runChain(t, m, cmd, 1); m.step != jumphostStepType || m.currentRegion() != "eu-west-2" {
		t.Fatalf("step = %v region = %q, want the Type step with the new default eu-west-2", m.step, m.currentRegion())
	}
}

func TestWizardSettingsNewDefaultRegionAppliesOnTheStartScreen(t *testing.T) {
	region := "eu-west-1"
	runner := regionRunner{historyRunner: newHistoryRunner(savedArchive), region: &region}
	m := newJumphostWizardModel(context.Background(), runner, jumphost.Options{AwsProfile: "dev"})
	m.settings = newFakeSettingsStore()
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = runChain(t, m, m.Init(), 1)
	if m.step != jumphostStepStart || m.currentRegion() != "eu-west-1" {
		t.Fatalf("step = %v region = %q, want the start screen in eu-west-1", m.step, m.currentRegion())
	}
	m = pressWizard(t, m, key(","))
	region = "eu-west-2"
	m, cmd := wizardUpdate(t, m, escKey)
	if m = runChain(t, m, cmd, 2); m.step != jumphostStepStart || m.currentRegion() != "eu-west-2" {
		t.Fatalf("step = %v region = %q, want the start screen with the new default eu-west-2", m.step, m.currentRegion())
	}
}

func TestWizardSettingsKeepTheChosenRegion(t *testing.T) {
	region := "eu-west-1"
	m := newTestWizard(t, regionRunner{historyRunner: newHistoryRunner(), region: &region}, jumphost.Options{AwsRegion: "us-east-1"})
	m.settings = newFakeSettingsStore()
	m = pressWizard(t, m, key(","))
	m, cmd := wizardUpdate(t, m, escKey)
	if m = runChain(t, m, cmd, 1); m.currentRegion() != "us-east-1" {
		t.Fatalf("region = %q, want the chosen us-east-1", m.currentRegion())
	}
}

func TestWizardSettingsLateSnapshotIsIgnoredAfterLeaving(t *testing.T) {
	m := pressWizard(t, settingsWizard(t, newFakeSettingsStore()), key(","))
	m = pressWizard(t, m, escKey)
	m, cmd := wizardUpdate(t, m, settingsLoadedMsg{snapshot: SettingsSnapshot{ProductionProfiles: []string{"*live*"}}})
	if cmd != nil || m.step != jumphostStepStart || m.production.matches("acme-live") || !m.production.matches("acme-prod") {
		t.Fatalf("step = %v cmd = %v, want the late snapshot ignored", m.step, cmd != nil)
	}
	m, _ = wizardUpdate(t, m, settingRefusedMsg{err: errors.New("late")})
	if m.settingsEditor.fieldErr != "" {
		t.Fatalf("fieldErr = %q, want the late refusal ignored", m.settingsEditor.fieldErr)
	}
}
