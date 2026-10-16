package ui

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft/airlock/internal/i18n"
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
		Path:            s.snapshot.Path,
		Settings:        slices.Clone(s.snapshot.Settings),
		Environments:    s.snapshot.Environments,
		Organization:    s.snapshot.Organization,
		DetectedClients: s.snapshot.DetectedClients,
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

// Validate refuses a gateway port past 65535, like config.Validate.
func (s *fakeSettingsStore) Validate(key, value string) error {
	if n, err := strconv.Atoi(value); key == "mcp.gateway.port" && err == nil && n > 65535 {
		return errors.New("invalid port " + value + " (valid: 1024-65535)")
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

// wizardKeys presses the named keys, without following the commands they lead to.
func wizardKeys(t *testing.T, m jumphostWizardModel, names ...string) jumphostWizardModel {
	t.Helper()
	for _, name := range names {
		m, _ = wizardUpdate(t, m, key(name))
	}
	return m
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
	for _, want := range []string{"Settings", "General", "Language", "Automatic", "/home/me/.config/airlock/config.json"} {
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
	m := wizardKeys(t, pressWizard(t, settingsWizard(t, store), key(",")), "down", "down", "right", "enter", "ctrl+u", "acme")
	m = pressWizard(t, m, key("enter"))
	if !slices.Equal(store.calls, []string{"set mcp.serverPrefix=acme"}) {
		t.Fatalf("calls = %v", store.calls)
	}
	if m.loading || m.settingsEditor.open != nil {
		t.Fatalf("loading = %v open = %v, want the editor closed", m.loading, m.settingsEditor.open != nil)
	}
	if view := wizardView(m); !strings.Contains(view, "acme") {
		t.Fatalf("view lacks the saved value:\n%s", view)
	}
}

func TestWizardSettingsRefusedValueKeepsTheField(t *testing.T) {
	store := newFakeSettingsStore()
	store.setErr = errors.New("invalid tag \"bastion\": want Key=Value")
	m := wizardKeys(t, pressWizard(t, settingsWizard(t, store), key(",")), "down", "down", "right", "enter", "ctrl+u", "bastion")
	m = pressWizard(t, m, key("enter"))
	if open := m.settingsEditor.open; open == nil || open.Value() != "bastion" || m.loading {
		t.Fatalf("open = %v loading = %v, want the editor kept with bastion", open != nil, m.loading)
	}
	if view := wizardView(m); !strings.Contains(view, "want Key=Value") {
		t.Fatalf("view lacks the refusal:\n%s", view)
	}
}

func TestWizardSettingsQIsTextWhileEditing(t *testing.T) {
	m := wizardKeys(t, pressWizard(t, settingsWizard(t, newFakeSettingsStore()), key(",")), "down", "down", "right", "enter", "ctrl+u")
	m, cmd := wizardUpdate(t, m, key("q"))
	if open := m.settingsEditor.open; m.canceled || m.step != jumphostStepSettings || open == nil || open.Value() != "q" {
		t.Fatalf("canceled = %v step = %v, want q typed", m.canceled, m.step)
	}
	if cmd != nil {
		if _, done := runWithTimeout(cmd).(jumphostWizardDoneMsg); done {
			t.Fatal("q finished the wizard while editing")
		}
	}
}

func TestWizardSettingsResetUnsets(t *testing.T) {
	store := newFakeSettingsStore()
	m := wizardKeys(t, pressWizard(t, settingsWizard(t, store), key(",")), "right", "down", "d")
	_ = pressWizard(t, m, key("y"))
	if !slices.Equal(store.calls, []string{"unset organizationConfig"}) {
		t.Fatalf("calls = %v", store.calls)
	}
}

func TestWizardSettingsRegionPickerSavesTheRegion(t *testing.T) {
	store := newFakeSettingsStore()
	m := wizardKeys(t, pressWizard(t, settingsWizard(t, store), key(",")), "down", "right", "enter")
	if !m.pickingRegion || !m.regionForSettings {
		t.Fatalf("picking = %v for settings = %v, want the region picker", m.pickingRegion, m.regionForSettings)
	}
	m, _ = wizardUpdate(t, m, key("paris"))
	m = pressWizard(t, m, key("enter"))
	if m.pickingRegion || m.regionForSettings || !slices.Equal(store.calls, []string{"set aws.defaultRegion=eu-west-3"}) {
		t.Fatalf("picking = %v calls = %v, want eu-west-3 saved", m.pickingRegion, store.calls)
	}
	if m.options.AwsRegion != "" {
		t.Fatalf("region of the connection = %q, want it untouched", m.options.AwsRegion)
	}
}

func TestWizardSettingsRegionPickerKeepsAnUnchangedRegion(t *testing.T) {
	store := newFakeSettingsStore()
	for idx := range store.snapshot.Settings {
		if store.snapshot.Settings[idx].Name == "aws.defaultRegion" {
			store.snapshot.Settings[idx].Value = "eu-west-3"
		}
	}
	m := wizardKeys(t, pressWizard(t, settingsWizard(t, store), key(",")), "down", "right", "enter")
	m = pressWizard(t, m, key("enter"))
	if m.pickingRegion || m.regionForSettings || m.loading || m.step != jumphostStepSettings || len(store.calls) != 0 {
		t.Fatalf("picking = %v loading = %v step = %v calls = %v, want the picker closed without a write",
			m.pickingRegion, m.loading, m.step, store.calls)
	}
}

func TestWizardSettingsUnchangedChoiceWritesNothing(t *testing.T) {
	store := newFakeSettingsStore()
	for idx := range store.snapshot.Settings {
		if store.snapshot.Settings[idx].Name == "mode" {
			store.snapshot.Settings[idx].Value, store.snapshot.Settings[idx].FromOrganization = "daemon", true
		}
	}
	m := wizardKeys(t, pressWizard(t, settingsWizard(t, store), key(",")), "right", "down", "down", "enter")
	if _, ok := m.settingsEditor.open.(choiceEditor); !ok {
		t.Fatalf("open = %T, want the choice editor of mode", m.settingsEditor.open)
	}
	m = pressWizard(t, m, key("enter"))
	if m.settingsEditor.open != nil || m.loading || len(store.calls) != 0 {
		t.Fatalf("open = %v loading = %v calls = %v, want the editor closed without a write",
			m.settingsEditor.open != nil, m.loading, store.calls)
	}
}

func TestWizardSettingsFollowProductionProfiles(t *testing.T) {
	m := pressWizard(t, settingsWizard(t, newFakeSettingsStore()), key(","))
	if !m.environments.Match("acme-prod").Production {
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
	if m := dashboardSettings(t, newFakeSettingsStore()); !m.environments.Match("acme-prod").Production {
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
	m := newTestWizard(t, regionRunner{historyRunner: newHistoryRunner(), region: new("eu-west-1")}, jumphost.Options{AwsRegion: "us-east-1"})
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
	m, cmd := wizardUpdate(t, m, settingsLoadedMsg{snapshot: SettingsSnapshot{Environments: mustEnvironments(t, "prod!=*live*")}})
	if cmd != nil || m.step != jumphostStepStart || m.environments.Match("acme-live").Production || !m.environments.Match("acme-prod").Production {
		t.Fatalf("step = %v cmd = %v, want the late snapshot ignored", m.step, cmd != nil)
	}
	m, _ = wizardUpdate(t, m, settingRefusedMsg{err: errors.New("late")})
	if m.settingsEditor.fieldErr != "" {
		t.Fatalf("fieldErr = %q, want the late refusal ignored", m.settingsEditor.fieldErr)
	}
}

func TestFrenchConfirmHintsHaveNoEnglishPhrase(t *testing.T) {
	i18n.SetLanguage("fr")
	t.Cleanup(func() { i18n.SetLanguage("en") })
	m := wizardKeys(t, pressWizard(t, settingsWizard(t, newFakeSettingsStore()), key(",")), "right", "down", "d")
	if view := ansi.Strip(m.render()); strings.Contains(view, "any key") || !strings.Contains(view, "autre touche") {
		t.Fatalf("the reset confirmation hints are not French:\n%s", view)
	}
}

func TestWizardSettingsToggleWritesAtOnce(t *testing.T) {
	store := newFakeSettingsStore()
	m := wizardKeys(t, pressWizard(t, settingsWizard(t, store), key(",")), "down", "down", "down", "right", "down")
	if m = pressWizard(t, m, key("space")); !slices.Equal(store.calls, []string{"set agent.writes=true"}) || m.loading {
		t.Fatalf("calls = %v loading = %v, want agent.writes flipped", store.calls, m.loading)
	}
}

func TestWizardSettingsValidatesWithTheStore(t *testing.T) {
	store := newFakeSettingsStore()
	m := wizardKeys(t, pressWizard(t, settingsWizard(t, store), key(",")), "down", "down", "right", "down", "down", "down", "enter", "ctrl+u", "70000")
	if view := wizardView(m); !strings.Contains(view, "invalid port 70000") {
		t.Fatalf("view lacks the store's validation error:\n%s", view)
	}
	if m = pressWizard(t, m, key("enter")); len(store.calls) != 0 || m.settingsEditor.open == nil {
		t.Fatalf("calls = %v, want nothing written for an invalid value", store.calls)
	}
}
