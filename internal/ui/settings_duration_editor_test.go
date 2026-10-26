package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

// pressEditor feeds key names to a value editor and returns it with the last outcome.
func pressEditor(t *testing.T, ed valueEditor, names ...string) (valueEditor, editorOutcome) {
	t.Helper()
	outcome := editorStay
	for _, name := range names {
		ed, outcome, _ = ed.Update(key(name))
		if outcome != editorStay {
			return ed, outcome
		}
	}
	return ed, outcome
}

func durationView(value string) SettingView {
	view := testSetting("agent.idleTimeout", "agent", KindDuration, value)
	for _, preset := range []string{"15m", "30m", "1h", "4h"} {
		view.Options = append(view.Options, SettingOption{Value: preset, Label: preset})
	}
	return view
}

func minuteRule(value string) error {
	if value == "45s" {
		return errors.New("agent.idleTimeout: at least one minute")
	}
	return nil
}

func TestDurationEditorPreset(t *testing.T) {
	ed, outcome := pressEditor(t, newDurationEditor(durationView("15m"), minuteRule), "down", "down", "enter")
	if outcome != editorSave || ed.Value() != "1h" {
		t.Fatalf("outcome = %v value = %q, want 1h saved", outcome, ed.Value())
	}
}

func TestDurationEditorOther(t *testing.T) {
	ed, _ := pressEditor(t, newDurationEditor(durationView("15m"), minuteRule), "up", "enter")
	for _, r := range "45s" {
		ed, _ = pressEditor(t, ed, string(r))
	}
	if view := ansi.Strip(ed.View(NewStyles(true), 60, 20)); !strings.Contains(view, "at least one minute") {
		t.Fatalf("45s shows no error:\n%s", view)
	}
	if _, outcome := pressEditor(t, ed, "enter"); outcome != editorStay {
		t.Fatalf("enter on 45s: outcome = %v, want no save", outcome)
	}
	ed, _ = pressEditor(t, ed, "ctrl+u", "2", "h")
	if got, outcome := pressEditor(t, ed, "enter"); outcome != editorSave || got.Value() != "2h" {
		t.Fatalf("outcome = %v value = %q, want 2h saved", outcome, got.Value())
	}
	// esc in the field goes back to the list, esc on the list cancels.
	ed, outcome := pressEditor(t, ed, "esc")
	if outcome != editorStay {
		t.Fatalf("esc in the field: outcome = %v, want back to the list", outcome)
	}
	if _, outcome = pressEditor(t, ed, "esc"); outcome != editorCancel {
		t.Fatalf("esc on the list: outcome = %v, want cancel", outcome)
	}
}

func TestDurationEditorStartsOnCurrent(t *testing.T) {
	if ed, _ := pressEditor(t, newDurationEditor(durationView("30m"), minuteRule)); ed.Value() != "30m" {
		t.Fatalf("value = %q, want 30m", ed.Value())
	}
	ed := newDurationEditor(durationView("2h"), minuteRule)
	if ed.Value() != "2h" {
		t.Fatalf("value = %q, want 2h held by other…", ed.Value())
	}
	if view := ansi.Strip(ed.View(NewStyles(true), 60, 20)); !strings.Contains(view, i18n.T("ui.duration_editor.other")) {
		t.Fatalf("view lacks other…:\n%s", view)
	}
}

func TestDurationEditorLockedNeverSaves(t *testing.T) {
	view := durationView("15m")
	view.Locked = true
	if _, outcome := pressEditor(t, newDurationEditor(view, minuteRule), "enter"); outcome != editorStay {
		t.Fatalf("outcome = %v, want no save when locked", outcome)
	}
}

func TestDurationEditorEscThenEnterStillValidates(t *testing.T) {
	ed, _ := pressEditor(t, newDurationEditor(durationView("15m"), minuteRule), "up", "enter")
	ed = typeInto(t, ed, "45s")
	// esc back to the list, enter reopens the field, enter again must not save 45s.
	if _, outcome := pressEditor(t, ed, "esc", "enter", "enter"); outcome != editorStay {
		t.Fatalf("outcome = %v, want 45s refused", outcome)
	}
}
