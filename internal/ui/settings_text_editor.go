package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/n2jsoft/airlock/internal/i18n"
)

// textEditor edits a setting in a text field, checked with validate as you type: enter does not
// save a value it refuses.
type textEditor struct {
	validate func(string) error
	// err is why validate refuses the typed value.
	err   string
	view  SettingView
	input textinput.Model
}

// newTextEditor opens on the effective value; an Optional setting may be saved empty.
func newTextEditor(view SettingView, validate func(string) error) valueEditor {
	input := textinput.New()
	input.SetStyles(textInputStyles())
	input.Prompt = ""
	input.CharLimit = 512
	input.SetValue(view.Value)
	input.CursorEnd()
	if !view.Locked {
		input.Focus()
	}
	return textEditor{view: view, input: input, validate: validate}
}

func (t textEditor) Update(msg tea.Msg) (valueEditor, editorOutcome, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			return t, editorCancel, nil
		case "enter":
			if t.view.Locked || t.err != "" {
				return t, editorStay, nil
			}
			return t, editorSave, nil
		}
	}
	if t.view.Locked {
		return t, editorStay, nil
	}
	before := t.input.Value()
	var cmd tea.Cmd
	t.input, cmd = t.input.Update(msg)
	// Only a change is checked again: the cursor blinks and moves leave the value as it was.
	if t.input.Value() != before {
		t.err = ""
		if err := t.validate(t.Value()); err != nil {
			t.err = i18n.Localize(err)
		}
	}
	return t, editorStay, cmd
}

func (t textEditor) View(styles Styles, width, _ int) string {
	// The field scrolls inside the pane; the marker and the cursor take three cells.
	t.input.SetWidth(max(width-3, 1))
	lines := []string{truncate(styles.Text.Bold(true).Render(t.view.Label), width), "", styles.CardMarker.Render("▌ ") + inputView(t.input)}
	if t.err != "" {
		lines = append(lines, indentedLines(styles.Danger, t.err, width)...)
	}
	lines = append(lines, organizationOverrideLines(styles, t.view, width)...)
	// The description comes last: a short pane cuts it, never the field.
	lines = append(lines, "")
	for _, line := range wrapText(t.view.Description, max(width, 1)) {
		lines = append(lines, styles.Muted.Render(line))
	}
	return strings.Join(lines, "\n")
}

func (t textEditor) Value() string { return strings.TrimSpace(t.input.Value()) }

func (t textEditor) Keys() [][2]string {
	if t.view.Locked {
		return [][2]string{{"esc", i18n.T("ui.hint.back")}}
	}
	return [][2]string{{"enter", i18n.T("ui.hint.save")}, {"esc", i18n.T("ui.hint.cancel")}}
}
