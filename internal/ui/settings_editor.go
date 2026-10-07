package ui

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/n2jsoft/airlock/internal/environment"
)

// SettingsStore reads and writes the settings of config.json for the Settings screen.
type SettingsStore interface {
	Snapshot() (SettingsSnapshot, error)
	// Set validates value and writes it for key; the error names what is wrong.
	Set(key, value string) error
	// Unset removes key from the file, so that its default applies again.
	Unset(key string) error
}

// SettingsSnapshot is the content of the Settings screen.
type SettingsSnapshot struct {
	// Path is the file, shown under the rows.
	Path string
	// Settings are every key, in the order of the file.
	Settings []SettingView
	// Environments is the effective environments setting, for the badges.
	Environments environment.Set
}

// SettingView is one key of the Settings screen.
type SettingView struct {
	Name        string
	Description string
	// Value is the effective value; "" means none.
	Value string
	// OverriddenBy is the flag of this run winning over the file, e.g. "--jumphost-tag".
	OverriddenBy string
	// Choices, when not empty, are the values enter cycles through.
	Choices []string
	// FromFile reports that the file sets the key; otherwise its default applies.
	FromFile bool
	// Region makes enter open the region picker.
	Region bool
	// NextLaunch reports that a change applies at the next launch of airlock only.
	NextLaunch bool
}

// settingsOutcome is what the user asked of the Settings screen.
type settingsOutcome int

const (
	settingsStay settingsOutcome = iota
	// settingsBack leaves the screen.
	settingsBack
	// settingsSave writes requestValue for requestKey.
	settingsSave
	// settingsReset removes requestKey from the file.
	settingsReset
	// settingsPickRegion chooses requestKey in the region picker.
	settingsPickRegion
)

const (
	// settingsNameWidth fits the longest key, "readOnlySecretTag".
	settingsNameWidth = 20
	// settingsValueWidth is the value column; longer values are cut with the row.
	settingsValueWidth = 24
	// settingsNextLaunch is shown under the description of a NextLaunch key.
	settingsNextLaunch = "Applies at the next launch of airlock."
)

// settingsEditor lists the keys of config.json and edits the selected one.
type settingsEditor struct {
	// err is why the snapshot could not be read: the screen is then read-only.
	err error
	// fieldErr is why the last write was refused, shown under the selected row.
	fieldErr string
	// requestKey and requestValue are the key and value of the last outcome.
	requestKey   string
	requestValue string
	snapshot     SettingsSnapshot
	input        textinput.Model
	cursor       int
	editing      bool
	// confirming waits for y to reset the selected key.
	confirming bool
}

func newSettingsEditor() settingsEditor {
	input := textinput.New()
	input.SetStyles(textInputStyles())
	input.Prompt = ""
	input.CharLimit = 512
	// The field scrolls inside the value column; the cursor takes its last cell.
	input.SetWidth(settingsValueWidth - 1)
	return settingsEditor{input: input}
}

// loaded shows snapshot, keeping the selected key; err makes the screen read-only.
func (e settingsEditor) loaded(snapshot SettingsSnapshot, err error) settingsEditor {
	previous, _ := e.selected()
	e.snapshot, e.err, e.cursor = snapshot, err, 0
	for idx := range snapshot.Settings {
		if snapshot.Settings[idx].Name == previous.Name {
			e.cursor = idx
		}
	}
	return e
}

// selected returns the key under the cursor; ok is false on an unreadable or empty snapshot.
func (e settingsEditor) selected() (SettingView, bool) {
	if e.err != nil || e.cursor < 0 || e.cursor >= len(e.snapshot.Settings) {
		return SettingView{}, false
	}
	return e.snapshot.Settings[e.cursor], true
}

// typing reports whether printable keys belong to the editor, so q does not quit.
func (e settingsEditor) typing() bool {
	return e.editing || e.confirming
}

// closeEdit ends the edit: after a save, or when it is canceled.
func (e settingsEditor) closeEdit() settingsEditor {
	e.editing, e.fieldErr = false, ""
	e.input.Blur()
	return e
}

// refused shows why the last write failed; an edit stays open with what was typed.
func (e settingsEditor) refused(err error) settingsEditor {
	e.fieldErr = err.Error()
	return e
}

// Update handles one message and reports what the user asked, if anything.
func (e settingsEditor) Update(msg tea.Msg) (settingsEditor, settingsOutcome, tea.Cmd) {
	key, isKey := msg.(tea.KeyPressMsg)
	if e.editing {
		return e.updateEdit(msg)
	}
	if !isKey {
		return e, settingsStay, nil
	}
	if e.confirming {
		e.confirming = false
		if key.String() == "y" {
			return e, settingsReset, nil
		}
		return e, settingsStay, nil
	}
	if key.String() == "esc" {
		return e, settingsBack, nil
	}
	setting, ok := e.selected()
	if !ok {
		return e, settingsStay, nil
	}
	e.fieldErr = ""
	count := len(e.snapshot.Settings)
	switch key.String() {
	case "up", "k":
		e.cursor = (e.cursor - 1 + count) % count
	case "down", "j":
		e.cursor = (e.cursor + 1) % count
	case "d":
		// A key the file does not set has nothing to reset.
		if setting.FromFile {
			e.confirming, e.requestKey = true, setting.Name
		}
	case "enter":
		return e.change(setting)
	}
	return e, settingsStay, nil
}

// change starts the change of setting: a region picker, the next choice, or a text edit.
func (e settingsEditor) change(setting SettingView) (settingsEditor, settingsOutcome, tea.Cmd) {
	e.requestKey = setting.Name
	switch {
	case setting.Region:
		return e, settingsPickRegion, nil
	case len(setting.Choices) > 0:
		e.requestValue = nextChoice(setting.Choices, setting.Value)
		return e, settingsSave, nil
	}
	e.editing = true
	// A default is offered as the placeholder: the field holds what the file says.
	e.input.Placeholder = setting.Value
	e.input.SetValue("")
	if setting.FromFile {
		e.input.SetValue(setting.Value)
	}
	e.input.CursorEnd()
	return e, settingsStay, e.input.Focus()
}

// updateEdit handles the text field: enter saves the trimmed value, esc cancels. Enter on an empty
// field of a default key changes nothing, so it only closes the edit; a file key saved empty is
// cleared, as the store decides.
func (e settingsEditor) updateEdit(msg tea.Msg) (settingsEditor, settingsOutcome, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			return e.closeEdit(), settingsStay, nil
		case "enter":
			e.requestValue = strings.TrimSpace(e.input.Value())
			if setting, _ := e.selected(); !setting.FromFile && e.requestValue == "" {
				return e.closeEdit(), settingsStay, nil
			}
			return e, settingsSave, nil
		}
	}
	var cmd tea.Cmd
	e.input, cmd = e.input.Update(msg)
	return e, settingsStay, cmd
}

// nextChoice is the choice after current, the first one when current is none of them.
func nextChoice(choices []string, current string) string {
	return choices[(slices.Index(choices, current)+1)%len(choices)]
}

// View renders the rows, then the description of the selected key and the file path.
func (e settingsEditor) View(styles Styles, width int) string {
	textWidth := min(width, resourceCardWidth)
	if e.err != nil {
		return strings.Join([]string{
			styles.Danger.Width(textWidth).Render(e.err.Error()),
			"",
			styles.Muted.Width(textWidth).Render("Fix the file, then come back."),
			styles.Muted.Width(textWidth).Render("`airlock config show` names the faulty key."),
			styles.Muted.Width(textWidth).Render("`airlock config init --force` rewrites it with the defaults, keeping a .bak."),
		}, "\n")
	}
	lines := make([]string, 0, len(e.snapshot.Settings)+6)
	for idx := range e.snapshot.Settings {
		lines = append(lines, e.row(styles, idx, width))
		if idx == e.cursor && e.fieldErr != "" {
			lines = append(lines, truncate("  "+styles.Danger.Render(e.fieldErr), width))
		}
	}
	lines = append(lines, styles.Rule.Render(strings.Repeat("─", textWidth)))
	if setting, ok := e.selected(); ok {
		lines = append(lines, styles.Muted.Width(textWidth).Render(setting.Description))
		if setting.NextLaunch {
			lines = append(lines, truncate(styles.Warning.Render(settingsNextLaunch), width))
		}
		if e.confirming {
			lines = append(lines, truncate(styles.Warning.Render("Reset "+setting.Name+" to its default? y/n"), width))
		}
	}
	lines = append(lines, truncate(styles.Faint.Render(e.snapshot.Path), width))
	return strings.Join(lines, "\n")
}

// row renders the key at idx: name, value (or the field while editing it), source and flag.
func (e settingsEditor) row(styles Styles, idx, width int) string {
	setting := e.snapshot.Settings[idx]
	marker, name := "  ", styles.Text.Render(padRight(setting.Name, settingsNameWidth))
	if idx == e.cursor {
		marker = styles.CardMarker.Render("▌ ")
		name = styles.Text.Bold(true).Render(padRight(setting.Name, settingsNameWidth))
	}
	var value string
	switch {
	case idx == e.cursor && e.editing:
		value = inputView(e.input)
	case setting.Value == "":
		value = styles.Muted.Render("none")
	default:
		value = styles.Text.Render(truncate(setting.Value, settingsValueWidth))
	}
	source := "default"
	if setting.FromFile {
		source = "file"
	}
	line := marker + name + padRight(value, settingsValueWidth) + "  " + styles.Faint.Render(source)
	if setting.OverriddenBy != "" {
		line += "  " + styles.Warning.Render("⚑ "+setting.OverriddenBy+" for this run")
	}
	return truncate(line, width)
}

// padRight pads value with spaces to width columns.
func padRight(value string, width int) string {
	return value + strings.Repeat(" ", max(width-lipgloss.Width(value), 0))
}
