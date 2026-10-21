package ui

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// firstRunSnapshot are the keys of the wizard at their defaults; locked lists the keys an
// organization locks, at its value.
func firstRunSnapshot(org string, locked map[string]string) SettingsSnapshot {
	language := testSetting("language", "general", KindChoice, "auto")
	language.Options = testOptions("language", "auto", "en", "fr")
	source := testSetting("organizationConfig", "general", KindText, "")
	source.Optional = true
	mode := testSetting("mode", "general", KindChoice, "cli")
	mode.Options = testOptions("mode", "cli", "daemon")
	autoConnect := testSetting("agent.autoConnect", "agent", KindChoice, "none")
	autoConnect.Options = testOptions("agent.autoConnect", "none", "non-prod", "all")
	settings := []SettingView{language, source, mode,
		testSetting("environments", "general", KindEnvironments, "prod!="), autoConnect,
		testSetting("mcp.clients", "mcp", KindClients, "auto")}
	for idx := range settings {
		if value, ok := locked[settings[idx].Name]; ok {
			settings[idx].Value, settings[idx].Locked, settings[idx].FromOrganization = value, true, true
		}
	}
	return SettingsSnapshot{Path: "/home/me/.config/airlock/config.json", Settings: settings, Organization: org}
}

// fakeFirstRun records what the wizard saves; orgs are the snapshots Load returns by source.
type fakeFirstRun struct {
	orgs     map[string]SettingsSnapshot
	language string
	saved    [][2]string
	saves    int
}

func (f *fakeFirstRun) setup() FirstRunSetup {
	return FirstRunSetup{
		Snapshot: firstRunSnapshot("", nil),
		Load: func(source string) (SettingsSnapshot, error) {
			if snapshot, ok := f.orgs[source]; ok {
				return snapshot, nil
			}
			return SettingsSnapshot{}, errors.New("organization config " + source + " not found")
		},
		Validate: func(string, string) error { return nil },
		Save: func(values [][2]string) error {
			f.saves++
			f.saved = values
			return nil
		},
		Profiles:    func() ([]string, error) { return []string{"acme-prod", "acme-dev"}, nil },
		SetLanguage: func(value string) { f.language = value },
	}
}

func newFakeFirstRun() *fakeFirstRun {
	return &fakeFirstRun{orgs: map[string]SettingsSnapshot{"": firstRunSnapshot("", nil)}}
}

// firstRunPress sends each key, then the messages of the wizard's own commands; quit reports a
// tea.Quit.
func firstRunPress(t *testing.T, m firstRunModel, names ...string) (_ firstRunModel, quit bool) {
	t.Helper()
	for _, name := range names {
		model, cmd := m.Update(key(name))
		m = firstRunOf(t, model)
		for cmd != nil {
			msg := runFirstRunCmd(cmd)
			switch msg.(type) {
			case firstRunLoadedMsg, firstRunSavedMsg, firstRunProfilesMsg:
				model, cmd = m.Update(msg)
				m = firstRunOf(t, model)
			case tea.QuitMsg:
				return m, true
			default:
				cmd = nil
			}
		}
	}
	return m, false
}

func firstRunOf(t *testing.T, model tea.Model) firstRunModel {
	t.Helper()
	m, ok := model.(firstRunModel)
	if !ok {
		t.Fatalf("model is a %T", model)
	}
	return m
}

// runFirstRunCmd runs cmd; a command waiting, like a cursor blink, gives nil.
func runFirstRunCmd(cmd tea.Cmd) tea.Msg {
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg
	case <-time.After(100 * time.Millisecond):
		return nil
	}
}

func firstRunPageKey(m firstRunModel) string {
	switch m.page {
	case firstRunSummary:
		return "summary"
	case firstRunDoctor:
		return "doctor"
	}
	return firstRunPages[m.page]
}

func TestFirstRunAsksEveryKeyAndSavesTheChanges(t *testing.T) {
	fake := newFakeFirstRun()
	m := newFirstRunModel(fake.setup(), NewStyles(true))
	steps := []struct{ keys []string }{
		{[]string{"down", "down", "enter"}}, // language: fr
		{[]string{"enter"}},                 // no organization
		{[]string{"down", "enter"}},         // mode: daemon
		{[]string{"s"}},                     // environments: the proposed *prod*
		{[]string{"down", "enter"}},         // agent.autoConnect: non-prod
		{[]string{"enter"}},                 // mcp.clients: auto
	}
	for idx, step := range steps {
		m, _ = firstRunPress(t, m, step.keys...)
		if page := firstRunPageKey(m); idx+1 < len(firstRunPages) && page != firstRunPages[idx+1] {
			t.Fatalf("after %s: page = %s, want %s", firstRunPages[idx], page, firstRunPages[idx+1])
		}
	}
	if firstRunPageKey(m) != "summary" {
		t.Fatalf("page = %s, want the summary", firstRunPageKey(m))
	}
	if fake.language != "fr" {
		t.Fatalf("language switched to %q, want fr", fake.language)
	}
	view := ansi.Strip(m.render())
	for _, want := range []string{"Daemon", "Non-production", "/home/me/.config/airlock/config.json"} {
		if !strings.Contains(view, want) {
			t.Fatalf("summary lacks %q:\n%s", want, view)
		}
	}
	m, _ = firstRunPress(t, m, "enter")
	want := [][2]string{{"language", "fr"}, {"mode", "daemon"}, {"environments", "prod!=*prod*"}, {"agent.autoConnect", "non-prod"}}
	if !reflect.DeepEqual(fake.saved, want) {
		t.Fatalf("saved %v, want %v", fake.saved, want)
	}
	if firstRunPageKey(m) != "doctor" || !m.result.Saved {
		t.Fatalf("page = %s saved = %v, want the doctor question after a save", firstRunPageKey(m), m.result.Saved)
	}
	m, quit := firstRunPress(t, m, "enter")
	if !quit || !m.result.Doctor {
		t.Fatalf("quit = %v doctor = %v, want the doctor asked", quit, m.result.Doctor)
	}
}

func TestFirstRunOrganizationLocksSkipTheirPages(t *testing.T) {
	fake := newFakeFirstRun()
	source := "https://example.com/org.json"
	fake.orgs[source] = firstRunSnapshot(source, map[string]string{"mode": "daemon", "mcp.clients": "claude:user"})
	m := newFirstRunModel(fake.setup(), NewStyles(true))
	m, _ = firstRunPress(t, m, "enter", source, "enter")
	if !m.reviewing {
		t.Fatalf("the organization is not shown after it was read")
	}
	view := ansi.Strip(m.render())
	for _, want := range []string{source, "Mode", "Daemon", "locked"} {
		if !strings.Contains(view, want) {
			t.Fatalf("organization review lacks %q:\n%s", want, view)
		}
	}
	m, _ = firstRunPress(t, m, "enter")
	if firstRunPageKey(m) != "environments" {
		t.Fatalf("page = %s, want environments: the locked mode is not asked", firstRunPageKey(m))
	}
	m, _ = firstRunPress(t, m, "s", "enter")
	if firstRunPageKey(m) != "summary" {
		t.Fatalf("page = %s, want the summary: the locked mcp.clients is not asked", firstRunPageKey(m))
	}
	if view = ansi.Strip(m.render()); !strings.Contains(view, "imposed by the organization") {
		t.Fatalf("summary does not mark the locked keys:\n%s", view)
	}
	firstRunPress(t, m, "enter")
	want := [][2]string{{"organizationConfig", source}, {"environments", "prod!=*prod*"}}
	if !reflect.DeepEqual(fake.saved, want) {
		t.Fatalf("saved %v, want the organization first and no locked key: %v", fake.saved, want)
	}
}

func TestFirstRunOrganizationLockingEverythingLeavesThePersonalKeys(t *testing.T) {
	fake := newFakeFirstRun()
	source := "/etc/acme/org.json"
	fake.orgs[source] = firstRunSnapshot(source, map[string]string{
		"mode": "cli", "environments": "production!=*-prod", "agent.autoConnect": "none", "mcp.clients": "auto",
	})
	m := newFirstRunModel(fake.setup(), NewStyles(true))
	m, _ = firstRunPress(t, m, "enter", source, "enter", "enter")
	if firstRunPageKey(m) != "summary" {
		t.Fatalf("page = %s, want the summary right after the organization", firstRunPageKey(m))
	}
	firstRunPress(t, m, "enter")
	if want := [][2]string{{"organizationConfig", source}}; !reflect.DeepEqual(fake.saved, want) {
		t.Fatalf("saved %v, want %v", fake.saved, want)
	}
}

func TestFirstRunManagedOrganizationIsShownNotAsked(t *testing.T) {
	fake := newFakeFirstRun()
	setup := fake.setup()
	setup.Managed = "/Library/Application Support/airlock/managed.json"
	setup.Snapshot = firstRunSnapshot(setup.Managed, map[string]string{"mode": "daemon"})
	fake.orgs[""] = setup.Snapshot
	m := newFirstRunModel(setup, NewStyles(true))
	m, _ = firstRunPress(t, m, "enter")
	if firstRunPageKey(m) != "organizationConfig" || m.editor != nil {
		t.Fatalf("page = %s, want the managed organization shown without a field", firstRunPageKey(m))
	}
	if view := ansi.Strip(m.render()); !strings.Contains(view, setup.Managed) {
		t.Fatalf("the managed organization is not named:\n%s", view)
	}
	m, _ = firstRunPress(t, m, "enter")
	if firstRunPageKey(m) != "environments" {
		t.Fatalf("page = %s, want environments past the locked mode", firstRunPageKey(m))
	}
	m, _ = firstRunPress(t, m, "s", "enter", "enter", "enter")
	if want := [][2]string{{"environments", "prod!=*prod*"}}; !reflect.DeepEqual(fake.saved, want) {
		t.Fatalf("saved %v, want %v: organizationConfig is not the user's", fake.saved, want)
	}
}

func TestFirstRunUnreadableOrganizationStaysOnItsPage(t *testing.T) {
	fake := newFakeFirstRun()
	m := newFirstRunModel(fake.setup(), NewStyles(true))
	m, _ = firstRunPress(t, m, "enter", "https://example.com/missing.json", "enter")
	if firstRunPageKey(m) != "organizationConfig" || !strings.Contains(m.err, "not found") {
		t.Fatalf("page = %s err = %q, want the organization page with the error", firstRunPageKey(m), m.err)
	}
}

func TestFirstRunSkipWritesTheFile(t *testing.T) {
	fake := newFakeFirstRun()
	m := newFirstRunModel(fake.setup(), NewStyles(true))
	m, _ = firstRunPress(t, m, "esc")
	if !m.skipping {
		t.Fatalf("esc on the first page does not offer to skip")
	}
	m, _ = firstRunPress(t, m, "n")
	if m.skipping || firstRunPageKey(m) != "language" {
		t.Fatalf("n does not return to the language")
	}
	m, quit := firstRunPress(t, m, "esc", "y")
	if !quit || !m.result.Skipped || !m.result.Saved || fake.saves != 1 || fake.saved != nil {
		t.Fatalf("quit = %v result = %+v saves = %d saved = %v, want an empty file written", quit, m.result, fake.saves, fake.saved)
	}
}

func TestFirstRunEscGoesBackAPage(t *testing.T) {
	fake := newFakeFirstRun()
	m := newFirstRunModel(fake.setup(), NewStyles(true))
	m, _ = firstRunPress(t, m, "enter", "enter")
	if firstRunPageKey(m) != "mode" {
		t.Fatalf("page = %s, want mode", firstRunPageKey(m))
	}
	if m, _ = firstRunPress(t, m, "esc"); firstRunPageKey(m) != "organizationConfig" {
		t.Fatalf("page = %s, want esc back to the organization", firstRunPageKey(m))
	}
}

func TestFirstRunQuitSavesNothing(t *testing.T) {
	fake := newFakeFirstRun()
	m := newFirstRunModel(fake.setup(), NewStyles(true))
	m, quit := firstRunPress(t, m, "ctrl+c")
	if !quit || m.result.Saved || fake.saves != 0 {
		t.Fatalf("quit = %v saved = %v saves = %d, want a quit without a write", quit, m.result.Saved, fake.saves)
	}
}
