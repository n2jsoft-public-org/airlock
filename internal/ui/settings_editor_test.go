package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/n2jsoft/airlock/internal/environment"
)

// testSettings is a snapshot with one key of each kind, in this order: a default text key, a
// file key, a region, a key overridden by a flag, a list of patterns and a choice.
func testSettings() SettingsSnapshot {
	return SettingsSnapshot{
		Path: "/home/me/.config/airlock/config.json",
		Settings: []SettingView{
			{Name: "jumphostTag", Description: "Tag (Key=Value) of the running SSM jumphost EC2 instance", Value: "Usage=jumphost"},
			{Name: "defaultDbUser", Description: "Database user signing in with AWS IAM", Value: "app_ro", FromFile: true},
			{Name: "defaultRegion", Description: "Region used after --region", Region: true},
			{Name: "mcpServerPrefix", Description: "Start of the MCP server names", Value: "airlock", OverriddenBy: "--mcp-server-prefix"},
			{Name: "productionProfiles", Description: "Patterns of the production profiles"},
			{Name: "mode", Description: "cli or daemon", Value: "cli", Choices: []string{"cli", "daemon"}, NextLaunch: true},
		},
		Environments: environment.FromProductionProfiles([]string{"*prod*"}),
	}
}

func loadedEditor() settingsEditor {
	return newSettingsEditor().loaded(testSettings(), nil)
}

func editorPress(t *testing.T, e settingsEditor, msgs ...tea.Msg) (settingsEditor, settingsOutcome) {
	t.Helper()
	outcome := settingsStay
	for _, msg := range msgs {
		e, outcome, _ = e.Update(msg)
	}
	return e, outcome
}

// editorDown moves the selection count rows down.
func editorDown(t *testing.T, e settingsEditor, count int) settingsEditor {
	t.Helper()
	for range count {
		e, _ = editorPress(t, e, key("j"))
	}
	return e
}

func editorView(e settingsEditor) string {
	return ansi.Strip(e.View(NewStyles(true), 120))
}

func TestSettingsEditorEditsADefaultKeyFromEmpty(t *testing.T) {
	e, outcome := editorPress(t, loadedEditor(), key("enter"))
	if !e.editing || outcome != settingsStay || e.input.Value() != "" || e.input.Placeholder != "Usage=jumphost" {
		t.Fatalf("editing = %v outcome = %v value = %q placeholder = %q", e.editing, outcome, e.input.Value(), e.input.Placeholder)
	}
	e, outcome = editorPress(t, e, key(" Env=bastion "), key("enter"))
	if outcome != settingsSave || e.requestKey != "jumphostTag" || e.requestValue != "Env=bastion" {
		t.Fatalf("outcome = %v request = %q=%q, want a trimmed save", outcome, e.requestKey, e.requestValue)
	}
}

func TestSettingsEditorUnchangedDefaultKeyIsNotSaved(t *testing.T) {
	e, outcome := editorPress(t, loadedEditor(), key("enter"), key("enter"))
	if e.editing || outcome != settingsStay {
		t.Fatalf("editing = %v outcome = %v, want the edit closed without a save", e.editing, outcome)
	}
}

func TestSettingsEditorClearedFileKeySavesEmpty(t *testing.T) {
	e, outcome := editorPress(t, editorDown(t, loadedEditor(), 1), key("enter"), key("ctrl+u"), key("enter"))
	if outcome != settingsSave || e.requestKey != "defaultDbUser" || e.requestValue != "" {
		t.Fatalf("outcome = %v request = %q=%q, want the empty value saved", outcome, e.requestKey, e.requestValue)
	}
}

func TestSettingsEditorPrefillsTheFileValue(t *testing.T) {
	e, _ := editorPress(t, editorDown(t, loadedEditor(), 1), key("enter"))
	if e.input.Value() != "app_ro" {
		t.Fatalf("value = %q, want the file's app_ro", e.input.Value())
	}
}

func TestSettingsEditorTypesCommas(t *testing.T) {
	e, outcome := editorPress(t, editorDown(t, loadedEditor(), 4), key("enter"), key("*prod*,*-live"), key("enter"))
	if outcome != settingsSave || e.requestValue != "*prod*,*-live" {
		t.Fatalf("outcome = %v value = %q, want the comma typed", outcome, e.requestValue)
	}
}

func TestSettingsEditorEscCancelsTheEdit(t *testing.T) {
	e, outcome := editorPress(t, loadedEditor(), key("enter"), key("x"), key("esc"))
	if e.editing || outcome != settingsStay {
		t.Fatalf("editing = %v outcome = %v, want the edit closed without a save", e.editing, outcome)
	}
}

func TestSettingsEditorRefusedValueKeepsTheField(t *testing.T) {
	e, _ := editorPress(t, loadedEditor(), key("enter"), key("bastion"), key("enter"))
	e = e.refused(errors.New("invalid tag \"bastion\": want Key=Value"))
	if !e.editing || e.input.Value() != "bastion" {
		t.Fatalf("editing = %v value = %q, want the typed value kept", e.editing, e.input.Value())
	}
	if view := editorView(e); !strings.Contains(view, "want Key=Value") {
		t.Fatalf("view lacks the refusal:\n%s", view)
	}
}

func TestSettingsEditorReloadKeepsTheSelectedKey(t *testing.T) {
	e := editorDown(t, loadedEditor(), 3).closeEdit().loaded(testSettings(), nil)
	if s, ok := e.selected(); !ok || s.Name != "mcpServerPrefix" {
		t.Fatalf("selected = %q, want mcpServerPrefix", s.Name)
	}
}

func TestSettingsEditorResetAsksFirst(t *testing.T) {
	e, _ := editorPress(t, editorDown(t, loadedEditor(), 1), key("d"))
	if !e.confirming || !strings.Contains(editorView(e), "Reset defaultDbUser to its default? y/n") {
		t.Fatalf("confirming = %v, want the question:\n%s", e.confirming, editorView(e))
	}
	kept, outcome := editorPress(t, e, key("n"))
	if kept.confirming || outcome != settingsStay {
		t.Fatalf("n: confirming = %v outcome = %v, want nothing", kept.confirming, outcome)
	}
	e, outcome = editorPress(t, e, key("y"))
	if outcome != settingsReset || e.requestKey != "defaultDbUser" {
		t.Fatalf("y: outcome = %v key = %q, want the reset", outcome, e.requestKey)
	}
}

func TestSettingsEditorResetIgnoresADefaultKey(t *testing.T) {
	if e, _ := editorPress(t, loadedEditor(), key("d")); e.confirming {
		t.Fatal("a key the file does not set has nothing to reset")
	}
}

func TestSettingsEditorChoiceCycles(t *testing.T) {
	e, outcome := editorPress(t, editorDown(t, loadedEditor(), 5), key("enter"))
	if outcome != settingsSave || e.requestKey != "mode" || e.requestValue != "daemon" || e.editing {
		t.Fatalf("outcome = %v request = %q=%q editing = %v, want daemon saved", outcome, e.requestKey, e.requestValue, e.editing)
	}
}

func TestSettingsEditorChoiceWithoutValueTakesTheFirst(t *testing.T) {
	snapshot := testSettings()
	snapshot.Settings[5].Value = ""
	e := editorDown(t, newSettingsEditor().loaded(snapshot, nil), 5)
	if e, _ = editorPress(t, e, key("enter")); e.requestValue != "cli" {
		t.Fatalf("value = %q, want the first choice", e.requestValue)
	}
}

func TestSettingsEditorRegionOpensThePicker(t *testing.T) {
	e, outcome := editorPress(t, editorDown(t, loadedEditor(), 2), key("enter"))
	if outcome != settingsPickRegion || e.requestKey != "defaultRegion" || e.editing {
		t.Fatalf("outcome = %v key = %q editing = %v, want the region picker", outcome, e.requestKey, e.editing)
	}
}

func TestSettingsEditorEscGoesBack(t *testing.T) {
	if _, outcome := editorPress(t, loadedEditor(), key("esc")); outcome != settingsBack {
		t.Fatalf("outcome = %v, want back", outcome)
	}
}

func TestSettingsEditorView(t *testing.T) {
	view := editorView(loadedEditor())
	for _, want := range []string{
		"jumphostTag", "Usage=jumphost", "default", "app_ro", "file", "none",
		"⚑ --mcp-server-prefix for this run",
		"Tag (Key=Value) of the running SSM jumphost EC2 instance",
		"/home/me/.config/airlock/config.json",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "next launch") {
		t.Fatalf("the next launch notice belongs to mode only:\n%s", view)
	}
	if view = editorView(editorDown(t, loadedEditor(), 5)); !strings.Contains(view, "Applies at the next launch of airlock.") {
		t.Fatalf("mode lacks the next launch notice:\n%s", view)
	}
}

func TestSettingsEditorFitsANarrowBody(t *testing.T) {
	snapshot := testSettings()
	snapshot.Settings[0].Value = "claude:user,vscode,codex,cursor,gemini:user"
	snapshot.Settings[0].Description = strings.Repeat("a long description ", 12)
	view := newSettingsEditor().loaded(snapshot, nil).View(NewStyles(true), 60)
	for _, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > 60 {
			t.Fatalf("line of %d columns in a 60-column body: %q", width, ansi.Strip(line))
		}
	}
}

func TestSettingsEditorLongValueScrollsInItsColumn(t *testing.T) {
	typed := "BEGIN" + strings.Repeat("-", 45) + "0123456789"
	e, _ := editorPress(t, loadedEditor(), key("enter"), key(typed))
	var row string
	for _, line := range strings.Split(e.View(NewStyles(true), 120), "\n") {
		if strings.Contains(line, "jumphostTag") {
			row = line
		}
	}
	plain := ansi.Strip(row)
	if lipgloss.Width(row) > 120 || !strings.Contains(plain, "0123456789") || strings.Contains(plain, "BEGIN") {
		t.Fatalf("editing row = %q, want the end of the value scrolled into its column", plain)
	}
	if !strings.Contains(plain, "default") {
		t.Fatalf("editing row = %q, want the source column kept", plain)
	}
}

func TestSettingsEditorUnreadableFileIsReadOnly(t *testing.T) {
	e := newSettingsEditor().loaded(SettingsSnapshot{}, errors.New("invalid config /x/config.json: unknown mode"))
	view := editorView(e)
	for _, want := range []string{"invalid config /x/config.json: unknown mode", "airlock config show", "airlock config init --force"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
	if entered, outcome := editorPress(t, e, key("enter")); entered.editing || outcome != settingsStay {
		t.Fatalf("enter: editing = %v outcome = %v, want nothing", entered.editing, outcome)
	}
	if _, outcome := editorPress(t, e, key("esc")); outcome != settingsBack {
		t.Fatalf("esc: outcome = %v, want back", outcome)
	}
}
