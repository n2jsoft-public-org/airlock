package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/n2jsoft-public-org/airlock/internal/environment"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
)

var errTestProfiles = errors.New("no AWS config")

// environmentsFixture is the value of the environments goldens.
const environmentsFixture = "prod!=*-prod*;staging=*-staging*,*-stg"

// profilesRunner lists the AWS profiles names.
type profilesRunner struct {
	historyRunner
	names []string
}

func (r profilesRunner) ListAWSProfileSummaries(context.Context) ([]jumphost.AWSProfileSummary, error) {
	profiles := make([]jumphost.AWSProfileSummary, 0, len(r.names))
	for _, name := range r.names {
		profiles = append(profiles, jumphost.AWSProfileSummary{Name: name})
	}
	return profiles, nil
}

func environmentsView(value string) SettingView {
	return testSetting("environments", "general", KindEnvironments, value)
}

func newTestEnvironmentsEditor(value string) valueEditor {
	return newEnvironmentsEditor(environmentsView(value), acceptAnyValue)
}

// editorLine is the first line of the rendered editor containing text.
func editorLine(ed valueEditor, text string) string {
	return lineWith(ansi.Strip(ed.View(NewStyles(true), 57, 20)), text)
}

// lineWith is the first line of view containing text.
func lineWith(view, text string) string {
	for line := range strings.SplitSeq(view, "\n") {
		if strings.Contains(line, text) {
			return line
		}
	}
	return ""
}

func TestEnvironmentsEditorRoundTrips(t *testing.T) {
	for _, value := range []string{environmentsFixture, "", "staging=*-stg"} {
		set, err := environment.Parse(value)
		if err != nil {
			t.Fatal(err)
		}
		if got := newTestEnvironmentsEditor(value).Value(); got != set.String() {
			t.Errorf("%q: Value = %q, want %q", value, got, set.String())
		}
	}
	if got := settingSummary(environmentsView(environmentsFixture)); got != "prod, staging" {
		t.Fatalf("summary = %q, want prod, staging", got)
	}
}

func TestEnvironmentsEditorAddAndSave(t *testing.T) {
	ed, _ := pressEditor(t, newTestEnvironmentsEditor(environmentsFixture), "a")
	ed = typeInto(t, ed, "qa")
	ed, _ = pressEditor(t, ed, "enter", "a")
	ed = typeInto(t, ed, "*-qa")
	ed, _ = pressEditor(t, ed, "enter", "esc")
	ed, outcome := pressEditor(t, ed, "s")
	if outcome != editorSave || !strings.HasSuffix(ed.Value(), "; qa=*-qa") {
		t.Fatalf("outcome = %v value = %q, want qa=*-qa saved last", outcome, ed.Value())
	}
}

func TestEnvironmentsEditorMovesProduction(t *testing.T) {
	ed, _ := pressEditor(t, newTestEnvironmentsEditor(environmentsFixture), "down", "enter", "down", "space")
	if got := ed.Value(); !strings.HasPrefix(got, "staging!=*-staging*,*-stg") || !strings.Contains(got, "; prod=*-prod*") {
		t.Fatalf("value = %q, want staging production and prod not", got)
	}
	// Space again leaves it production: there is always one.
	if ed, _ = pressEditor(t, ed, "space"); !strings.HasPrefix(ed.Value(), "staging!=") {
		t.Fatalf("value = %q, want staging still production", ed.Value())
	}
	// prod names production only: the set is refused until the old one is renamed.
	ed, outcome := pressEditor(t, ed, "esc", "s")
	if outcome != editorStay || editorLine(ed, "rename it") == "" {
		t.Fatalf("outcome = %v, want the reserved name refused:\n%s", outcome, ansi.Strip(ed.View(NewStyles(true), 57, 20)))
	}
	ed, _ = pressEditor(t, ed, "up", "enter", "enter")
	for range len("prod") {
		ed, _ = pressEditor(t, ed, "backspace")
	}
	ed = typeInto(t, ed, "live")
	if ed, outcome = pressEditor(t, ed, "enter", "esc", "s"); outcome != editorSave || ed.Value() != "staging!=*-staging*,*-stg; live=*-prod*" {
		t.Fatalf("outcome = %v value = %q, want the renamed set saved", outcome, ed.Value())
	}
}

func TestEnvironmentsEditorRefusesBadNames(t *testing.T) {
	ed, _ := pressEditor(t, newTestEnvironmentsEditor(environmentsFixture), "a")
	ed = typeInto(t, ed, "staging")
	ed, outcome := pressEditor(t, ed, "enter")
	if outcome != editorStay || editorLine(ed, "already") == "" {
		t.Fatalf("outcome = %v, want the duplicate refused", outcome)
	}
	ed, _ = pressEditor(t, ed, "esc", "enter")
	ed = typeInto(t, ed, "9lives")
	if ed, _ = pressEditor(t, ed, "enter"); editorLine(ed, "lower-case") == "" {
		t.Fatalf("no invalid name error:\n%s", ansi.Strip(ed.View(NewStyles(true), 57, 20)))
	}
	// Leaving the form with no name drops the new environment: nothing changed, nothing is saved.
	ed, outcome = pressEditor(t, ed, "esc", "esc", "s")
	if outcome == editorSave {
		t.Fatalf("value = %q saved, want no save", ed.Value())
	}
	ed, _ = pressEditor(t, newTestEnvironmentsEditor(environmentsFixture), "down", "enter", "down", "down", "enter")
	ed = typeInto(t, ed, "[")
	if ed, _ = pressEditor(t, ed, "enter"); editorLine(ed, "Invalid pattern") == "" {
		t.Fatalf("no invalid pattern error:\n%s", ansi.Strip(ed.View(NewStyles(true), 57, 20)))
	}
}

func TestEnvironmentsEditorMatches(t *testing.T) {
	ed := newTestEnvironmentsEditor(environmentsFixture).(environmentsEditor) //nolint:errcheck // newEnvironmentsEditor returns an environmentsEditor
	opened, _ := pressEditor(t, ed, "down", "enter")
	if line := editorLine(opened, "Matches"); !strings.Contains(line, "loading") {
		t.Fatalf("matches line = %q, want loading", line)
	}
	loaded, _ := pressEditor(t, ed.withProfiles([]string{"acme-prod", "acme-staging", "sandbox"}, nil), "down", "enter")
	if line := editorLine(loaded, "Matches"); !strings.Contains(line, "acme-staging") || strings.Contains(line, "acme-prod") {
		t.Fatalf("matches line = %q, want acme-staging only", line)
	}
	many := make([]string, 0, 30)
	for idx := range 30 {
		many = append(many, fmt.Sprintf("team-%s-staging-%c", strings.Repeat("x", idx%5), 'a'+idx%26))
	}
	cut, _ := pressEditor(t, ed.withProfiles(many, nil), "down", "enter")
	if line := editorLine(cut, "Matches"); !strings.Contains(line, "more") || ansi.StringWidth(line) > 57 {
		t.Fatalf("matches line = %q, want cut with … N more within 57 columns", line)
	}
	failed, _ := pressEditor(t, ed.withProfiles(nil, errTestProfiles), "down", "enter")
	if line := editorLine(failed, "Matches"); !strings.Contains(line, "unavailable") {
		t.Fatalf("matches line = %q, want unavailable", line)
	}
}

func TestEnvironmentsEditorDiscardPrompt(t *testing.T) {
	if _, outcome := pressEditor(t, newTestEnvironmentsEditor(environmentsFixture), "esc"); outcome != editorCancel {
		t.Fatalf("unchanged esc: outcome = %v, want cancel", outcome)
	}
	ed, _ := pressEditor(t, newTestEnvironmentsEditor(environmentsFixture), "a")
	ed = typeInto(t, ed, "qa")
	ed, outcome := pressEditor(t, ed, "enter", "esc", "esc")
	if outcome != editorStay || editorLine(ed, "Discard") == "" {
		t.Fatalf("outcome = %v, want the discard prompt", outcome)
	}
	if ed, outcome = pressEditor(t, ed, "n"); outcome != editorStay || !strings.HasSuffix(ed.Value(), "; qa=") {
		t.Fatalf("n: outcome = %v value = %q, want the changes kept", outcome, ed.Value())
	}
	if _, outcome = pressEditor(t, ed, "esc", "y"); outcome != editorCancel {
		t.Fatalf("y: outcome = %v, want cancel", outcome)
	}
}

// Review Focus 5: an environments value the organization gives but does not lock opens editable
// on the effective value, and saving writes a local value.
func TestEnvironmentsEditorStartsFromTheOrganizationValue(t *testing.T) {
	store := newFakeSettingsStore()
	for idx := range store.snapshot.Settings {
		if store.snapshot.Settings[idx].Name == "environments" {
			store.snapshot.Settings[idx].Value, store.snapshot.Settings[idx].FromOrganization = environmentsFixture, true
		}
	}
	m := pressWizard(t, settingsWizard(t, store), key(","))
	m = pressWizard(t, wizardKeys(t, m, "right", "down", "down", "down"), key("enter"))
	if _, ok := m.settingsEditor.open.(environmentsEditor); !ok {
		t.Fatalf("open editor = %T, want the environments editor", m.settingsEditor.open)
	}
	m = wizardKeys(t, m, "a", "q", "a", "enter", "esc")
	m = pressWizard(t, m, key("s"))
	want := "set environments=prod!=*-prod*; staging=*-staging*,*-stg; qa="
	if !slices.Contains(store.calls, want) {
		t.Fatalf("calls = %q, want %q", store.calls, want)
	}
}

func TestEnvironmentsEditorLocked(t *testing.T) {
	view := environmentsView(environmentsFixture)
	view.Locked = true
	before := newEnvironmentsEditor(view, acceptAnyValue).Value()
	ed, outcome := pressEditor(t, newEnvironmentsEditor(view, acceptAnyValue), "a", "x", "s", "down", "enter", "enter", "space", "a", "x")
	if outcome != editorStay || ed.Value() != before {
		t.Fatalf("outcome = %v value = %q, want no change and no save", outcome, ed.Value())
	}
	// The form opened read-only; esc goes back to the list, then leaves without asking.
	if editorLine(ed, "Matches") == "" {
		t.Fatalf("form not open:\n%s", ansi.Strip(ed.View(NewStyles(true), 57, 20)))
	}
	if _, outcome = pressEditor(t, ed, "esc", "esc"); outcome != editorCancel {
		t.Fatalf("esc: outcome = %v, want cancel", outcome)
	}
}

// The wizard loads the AWS profiles when the editor opens; without discovery the editor says so.
func TestEnvironmentsEditorLoadsProfiles(t *testing.T) {
	open := func(m jumphostWizardModel) jumphostWizardModel {
		m = pressWizard(t, m, key(","))
		return pressWizard(t, wizardKeys(t, m, "right", "down", "down", "down"), key("enter"))
	}
	// The history runner lists dev and prod; the test setting is prod!=*prod*.
	m := wizardKeys(t, open(settingsWizard(t, newFakeSettingsStore())), "enter")
	if line := lineWith(wizardView(m), "Matches"); !strings.Contains(line, "prod") || strings.Contains(line, "dev") {
		t.Fatalf("matches line = %q, want prod only:\n%s", line, wizardView(m))
	}
	noDiscovery := settingsWizard(t, newFakeSettingsStore())
	noDiscovery.runner = nil
	if view := wizardView(wizardKeys(t, open(noDiscovery), "enter")); !strings.Contains(view, "unavailable") {
		t.Fatalf("no unavailable line:\n%s", view)
	}
	// Profiles arriving once the editor closed are ignored.
	closed := wizardKeys(t, open(settingsWizard(t, newFakeSettingsStore())), "esc")
	if closed, _ = wizardUpdate(t, closed, settingsProfilesMsg{names: []string{"x"}}); closed.settingsEditor.open != nil {
		t.Fatalf("open editor = %T after the profiles of a closed one", closed.settingsEditor.open)
	}
}

// paneView is what a pane of height lines shows of the editor: the pane cuts what does not fit.
func paneView(ed valueEditor, height int) string {
	lines := strings.Split(ansi.Strip(ed.View(NewStyles(true), 57, height)), "\n")
	return strings.Join(lines[:min(len(lines), height)], "\n")
}

// manyPatterns is an environment whose form is taller than the pane.
func manyPatterns() string {
	patterns := make([]string, 12)
	for idx := range patterns {
		patterns[idx] = "pat" + string(rune('a'+idx))
	}
	return "prod!=" + strings.Join(patterns, ",")
}

func TestEnvironmentsEditorFormKeepsTheFocusedPatternVisible(t *testing.T) {
	ed, _ := pressEditor(t, newTestEnvironmentsEditor(manyPatterns()), "enter")
	for range 2 + 11 {
		ed, _ = pressEditor(t, ed, "down")
	}
	if view := paneView(ed, 10); !strings.HasPrefix(lineWith(view, "patl"), "▌") {
		t.Fatalf("focused pattern is not shown in 10 lines:\n%s", view)
	}
}

func TestEnvironmentsEditorErrorStaysVisibleWhenTheFormOverflows(t *testing.T) {
	ed, _ := pressEditor(t, newTestEnvironmentsEditor(manyPatterns()), "enter")
	for range 2 + 11 {
		ed, _ = pressEditor(t, ed, "down")
	}
	ed, _ = pressEditor(t, ed, "enter")
	ed = typeInto(t, ed, "[")
	if view := paneView(ed, 10); !strings.Contains(view, "Invalid pattern") {
		t.Fatalf("pattern error is not shown in 10 lines:\n%s", view)
	}
}

func TestEnvironmentsEditorDiscardPromptStaysVisibleWithManyEnvironments(t *testing.T) {
	envs := []string{"prod!=*prod*"}
	for idx := range 12 {
		envs = append(envs, fmt.Sprintf("env%d=*env%d*", idx, idx))
	}
	for _, moves := range []int{0, 13} {
		ed, _ := pressEditor(t, newTestEnvironmentsEditor(strings.Join(envs, "; ")), "a")
		ed = typeInto(t, ed, "qa")
		ed, _ = pressEditor(t, ed, "enter", "esc")
		for range moves {
			ed, _ = pressEditor(t, ed, "up")
		}
		ed, _ = pressEditor(t, ed, "esc")
		view := paneView(ed, 12)
		if !strings.Contains(view, "Discard") || !strings.Contains(view, "Value") || !strings.Contains(view, "▌") {
			t.Fatalf("moves = %d: the focus, the Value line or the discard prompt is not shown in 12 lines:\n%s", moves, view)
		}
	}
}
