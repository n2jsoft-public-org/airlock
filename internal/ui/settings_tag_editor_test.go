package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func typeInto(t *testing.T, ed valueEditor, text string) valueEditor {
	t.Helper()
	for _, r := range text {
		ed, _ = pressEditor(t, ed, string(r))
	}
	return ed
}

func tagView(name, value string, optional bool) SettingView {
	view := testSetting(name, "aws", KindTag, value)
	view.Optional = optional
	return view
}

func TestTagEditorJoinsFields(t *testing.T) {
	ed := typeInto(t, newTagEditor(tagView("aws.jumphostTag", "", false), acceptAnyValue), "Usage")
	ed, _ = pressEditor(t, ed, "tab")
	ed = typeInto(t, ed, "jumphost")
	ed, outcome := pressEditor(t, ed, "enter")
	if outcome != editorSave || ed.Value() != "Usage=jumphost" {
		t.Fatalf("outcome = %v value = %q, want Usage=jumphost saved", outcome, ed.Value())
	}
}

func TestTagEditorOptionalEmpty(t *testing.T) {
	ed, outcome := pressEditor(t, newTagEditor(tagView("modules.sql.readOnlySecretTag", "", true), acceptAnyValue), "enter")
	if outcome != editorSave || ed.Value() != "" {
		t.Fatalf("outcome = %v value = %q, want the empty value saved", outcome, ed.Value())
	}
}

func TestTagEditorRequiredEmptyRefused(t *testing.T) {
	ed, outcome := pressEditor(t, newTagEditor(tagView("aws.jumphostTag", "", false), acceptAnyValue), "enter")
	if outcome != editorStay {
		t.Fatalf("outcome = %v, want no save", outcome)
	}
	if view := ansi.Strip(ed.View(NewStyles(true), 60, 20)); !strings.Contains(view, "Key") || !strings.Contains(view, "needs a tag") {
		t.Fatalf("no error under the fields:\n%s", view)
	}
	// One field only is refused too, and the store's rule has the last word.
	ed = typeInto(t, ed, "Usage")
	if _, outcome = pressEditor(t, ed, "enter"); outcome != editorStay {
		t.Fatalf("key only: outcome = %v, want no save", outcome)
	}
}

func TestTagEditorLoadsCurrent(t *testing.T) {
	ed := newTagEditor(tagView("aws.jumphostTag", "Role = bastion", false), acceptAnyValue).(tagEditor) //nolint:errcheck // newTagEditor returns a tagEditor
	if ed.key.Value() != "Role" || ed.value.Value() != "bastion" || ed.Value() != "Role=bastion" {
		t.Fatalf("fields = %q, %q", ed.key.Value(), ed.value.Value())
	}
}

func TestTagEditorLockedNeverSaves(t *testing.T) {
	view := tagView("aws.jumphostTag", "Usage=jumphost", false)
	view.Locked = true
	if _, outcome := pressEditor(t, newTagEditor(view, acceptAnyValue), "enter"); outcome != editorStay {
		t.Fatalf("outcome = %v, want no save when locked", outcome)
	}
}

func acceptAnyValue(string) error { return nil }

func TestTagEditorIncompletePairOnlyOnEnter(t *testing.T) {
	ed := typeInto(t, newTagEditor(tagView("modules.sql.readOnlySecretTag", "", true), acceptAnyValue), "Usage")
	if view := ansi.Strip(ed.View(NewStyles(true), 60, 20)); strings.Contains(view, "needs a key") {
		t.Fatalf("error shown while typing:\n%s", view)
	}
	ed, outcome := pressEditor(t, ed, "enter")
	if outcome != editorStay {
		t.Fatalf("outcome = %v, want no save", outcome)
	}
	if view := ansi.Strip(ed.View(NewStyles(true), 60, 20)); !strings.Contains(view, "needs a key") {
		t.Fatalf("no error after enter:\n%s", view)
	}
}

func TestTagEditorValidatorRefusalBlocksSave(t *testing.T) {
	refuse := func(string) error { return errors.New("refused by the store") }
	ed := typeInto(t, newTagEditor(tagView("aws.jumphostTag", "", false), refuse), "Usage")
	ed, _ = pressEditor(t, ed, "tab")
	ed = typeInto(t, ed, "jumphost")
	ed, outcome := pressEditor(t, ed, "enter")
	if outcome != editorStay {
		t.Fatalf("outcome = %v, want no save", outcome)
	}
	if view := ansi.Strip(ed.View(NewStyles(true), 60, 20)); !strings.Contains(view, "refused by the store") {
		t.Fatalf("no refusal shown:\n%s", view)
	}
}

func TestTagEditorKeyWithEqualsRefused(t *testing.T) {
	ed := typeInto(t, newTagEditor(tagView("aws.jumphostTag", "", false), acceptAnyValue), "a=b")
	ed, _ = pressEditor(t, ed, "tab")
	ed = typeInto(t, ed, "c")
	if _, outcome := pressEditor(t, ed, "enter"); outcome != editorStay {
		t.Fatalf("outcome = %v, want a=b refused", outcome)
	}
}
