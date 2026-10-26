package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/n2jsoft-public-org/airlock/internal/awstag"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

// tagEditor edits an AWS tag in a Key field and a Value field, joined as Key=Value.
type tagEditor struct {
	validate func(string) error
	// err is why the typed tag is refused.
	err        string
	key, value textinput.Model
	view       SettingView
	valueFocus bool
}

// newTagEditor opens on the effective tag; one that does not parse is shown whole in the Key field.
func newTagEditor(view SettingView, validate func(string) error) valueEditor {
	tag, err := awstag.Parse(view.Value)
	if err != nil {
		tag = awstag.Tag{Key: view.Value}
	}
	newField := func(value string) textinput.Model {
		input := textinput.New()
		input.SetStyles(textInputStyles())
		input.Prompt = ""
		input.CharLimit = 256
		input.SetValue(value)
		input.CursorEnd()
		return input
	}
	t := tagEditor{view: view, validate: validate, key: newField(tag.Key), value: newField(tag.Value)}
	if !view.Locked {
		t.key.Focus()
	}
	return t
}

// check is why the typed tag cannot be saved, empty when it can.
func (t tagEditor) check() string {
	key, value := strings.TrimSpace(t.key.Value()), strings.TrimSpace(t.value.Value())
	switch {
	case key == "" && value == "" && t.view.Optional:
		return ""
	case key == "" && value == "":
		return i18n.T("ui.tag_editor.required")
	case key == "" || value == "":
		return i18n.T("ui.tag_editor.need_both")
	case strings.Contains(key, "="):
		// The tag would read back with the key cut at the first "=".
		return i18n.T("ui.tag_editor.no_equals")
	}
	if err := t.validate(t.Value()); err != nil {
		return i18n.Localize(err)
	}
	return ""
}

func (t tagEditor) Update(msg tea.Msg) (valueEditor, editorOutcome, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			return t, editorCancel, nil
		case "enter":
			if t.view.Locked {
				return t, editorStay, nil
			}
			if t.err = t.check(); t.err != "" {
				return t, editorStay, nil
			}
			return t, editorSave, nil
		case "tab", "shift+tab", "up", "down":
			t.valueFocus = !t.valueFocus
			if t.valueFocus {
				t.key.Blur()
			} else {
				t.value.Blur()
			}
			if t.view.Locked {
				return t, editorStay, nil
			}
			cmd := t.field().Focus()
			return t, editorStay, cmd
		}
	}
	if t.view.Locked {
		return t, editorStay, nil
	}
	before := t.Value()
	var cmd tea.Cmd
	if t.valueFocus {
		t.value, cmd = t.value.Update(msg)
	} else {
		t.key, cmd = t.key.Update(msg)
	}
	// Only a change is checked again: the cursor blinks and moves leave the tag as it was.
	// The pair is only judged once both fields are filled: enter reports an incomplete one.
	if t.Value() != before {
		t.err = ""
		if strings.TrimSpace(t.key.Value()) != "" && strings.TrimSpace(t.value.Value()) != "" {
			t.err = t.check()
		}
	}
	return t, editorStay, cmd
}

// field is the input having the keys.
func (t *tagEditor) field() *textinput.Model {
	if t.valueFocus {
		return &t.value
	}
	return &t.key
}

func (t tagEditor) View(styles Styles, width, _ int) string {
	lines := []string{truncate(styles.Text.Bold(true).Render(t.view.Label), width), ""}
	labels := [2]string{i18n.T("ui.tag_editor.key"), i18n.T("ui.tag_editor.value")}
	inputs := [2]*textinput.Model{&t.key, &t.value}
	for idx, input := range inputs {
		marker := "  "
		if (idx == 1) == t.valueFocus {
			marker = styles.CardMarker.Render("▌ ")
		}
		input.SetWidth(max(width-3, 1))
		lines = append(lines, truncate(styles.Muted.Render(labels[idx]), width), marker+inputView(*input))
	}
	if t.err != "" {
		lines = append(lines, indentedLines(styles.Danger, t.err, width)...)
	}
	lines = append(lines, organizationOverrideLines(styles, t.view, width)...)
	// The description comes last: a short pane cuts it, never the fields.
	lines = append(lines, "")
	for _, line := range wrapText(t.view.Description, max(width, 1)) {
		lines = append(lines, styles.Muted.Render(line))
	}
	return strings.Join(lines, "\n")
}

// Value is Key=Value, or empty when both fields are.
func (t tagEditor) Value() string {
	key, value := strings.TrimSpace(t.key.Value()), strings.TrimSpace(t.value.Value())
	if key == "" && value == "" {
		return ""
	}
	return key + "=" + value
}

func (t tagEditor) Keys() [][2]string {
	if t.view.Locked {
		return [][2]string{{"tab", i18n.T("ui.hint.next_field")}, {"esc", i18n.T("ui.hint.back")}}
	}
	return [][2]string{{"tab", i18n.T("ui.hint.next_field")}, {"enter", i18n.T("ui.hint.save")}, {"esc", i18n.T("ui.hint.cancel")}}
}
