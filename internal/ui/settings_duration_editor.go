package ui

import (
	"cmp"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

// durationEditor offers the preset durations of a setting, then "other…" for a typed one.
type durationEditor struct {
	validate func(string) error
	// err is why validate refuses the typed value.
	err   string
	input textinput.Model
	view  SettingView
	// cursor indexes the presets; len(Options) is "other…".
	cursor int
	// typing is true while the "other…" field has the keys.
	typing bool
}

// newDurationEditor opens on the current value when it is a preset, else on "other…" holding it.
func newDurationEditor(view SettingView, validate func(string) error) valueEditor {
	input := textinput.New()
	input.SetStyles(textInputStyles())
	input.Prompt = ""
	input.CharLimit = 32
	cursor := slices.IndexFunc(view.Options, func(option SettingOption) bool { return option.Value == view.Value })
	if cursor < 0 {
		cursor = len(view.Options)
		input.SetValue(view.Value)
		input.CursorEnd()
	}
	return durationEditor{view: view, validate: validate, cursor: cursor, input: input}
}

func (d durationEditor) onOther() bool { return d.cursor >= len(d.view.Options) }

func (d durationEditor) Update(msg tea.Msg) (valueEditor, editorOutcome, tea.Cmd) {
	if d.typing {
		return d.updateField(msg)
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return d, editorStay, nil
	}
	count := len(d.view.Options) + 1
	switch key.String() {
	case "up", "k":
		d.cursor = (d.cursor - 1 + count) % count
	case "down", "j":
		d.cursor = (d.cursor + 1) % count
	case "esc":
		return d, editorCancel, nil
	case "enter":
		switch {
		case d.view.Locked:
		case d.onOther():
			d.typing = true
			cmd := d.input.Focus()
			return d, editorStay, cmd
		default:
			return d, editorSave, nil
		}
	}
	return d, editorStay, nil
}

func (d durationEditor) updateField(msg tea.Msg) (valueEditor, editorOutcome, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			d.typing, d.err = false, ""
			d.input.Blur()
			return d, editorStay, nil
		case "enter":
			// The text may have been typed before an esc cleared its error: check it again.
			d.err = ""
			if err := d.validate(d.Value()); err != nil {
				d.err = i18n.Localize(err)
			}
			if d.err != "" || d.Value() == "" {
				return d, editorStay, nil
			}
			return d, editorSave, nil
		}
	}
	before := d.input.Value()
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	// Only a change is checked again: the cursor blinks and moves leave the value as it was.
	if d.input.Value() != before {
		d.err = ""
		if err := d.validate(d.Value()); err != nil {
			d.err = i18n.Localize(err)
		}
	}
	return d, editorStay, cmd
}

func (d durationEditor) View(styles Styles, width, _ int) string {
	lines := []string{truncate(styles.Text.Bold(true).Render(d.view.Label), width), ""}
	for idx, option := range d.view.Options {
		label := cmp.Or(option.Label, option.Value)
		lines = append(lines, truncate(d.row(styles, idx == d.cursor, label), width))
	}
	other := i18n.T("ui.duration_editor.other")
	if d.onOther() && (d.typing || d.input.Value() != "") {
		d.input.SetWidth(max(width-lipgloss.Width(other)-6, 1))
		lines = append(lines, truncate(d.row(styles, true, other+" ")+inputView(d.input), width))
	} else {
		lines = append(lines, truncate(d.row(styles, d.onOther(), other), width))
	}
	if d.err != "" {
		lines = append(lines, indentedLines(styles.Danger, d.err, width)...)
	}
	lines = append(lines, "")
	for _, line := range wrapText(d.view.Description, max(width, 1)) {
		lines = append(lines, styles.Muted.Render(line))
	}
	return strings.Join(lines, "\n")
}

func (d durationEditor) row(styles Styles, selected bool, label string) string {
	if selected {
		return styles.CardMarker.Render("▌ ") + styles.Text.Bold(true).Render(label)
	}
	return "  " + styles.Text.Render(label)
}

func (d durationEditor) Value() string {
	if d.onOther() {
		return strings.TrimSpace(d.input.Value())
	}
	return d.view.Options[d.cursor].Value
}

func (d durationEditor) Keys() [][2]string {
	switch {
	case d.view.Locked:
		return [][2]string{{"↑↓", i18n.T("ui.hint.move")}, {"esc", i18n.T("ui.hint.back")}}
	case d.typing:
		return [][2]string{{"enter", i18n.T("ui.hint.save")}, {"esc", i18n.T("ui.hint.back")}}
	}
	return [][2]string{{"↑↓", i18n.T("ui.hint.move")}, {"enter", i18n.T("ui.hint.pick")}, {"esc", i18n.T("ui.hint.cancel")}}
}
