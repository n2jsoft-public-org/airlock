package ui

import (
	"cmp"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/n2jsoft-public-org/airlock/internal/aiclient"
	"github.com/n2jsoft-public-org/airlock/internal/environment"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

// SettingsStore reads and writes the settings of config.json for the Settings screen.
type SettingsStore interface {
	Snapshot() (SettingsSnapshot, error)
	// Set validates value and writes it for key; the error names what is wrong.
	Set(key, value string) error
	// Unset removes key from the file, so that its default applies again.
	Unset(key string) error
	// Validate reports why value is not valid for key, without writing anything.
	Validate(key, value string) error
}

// SettingsSnapshot is the content of the Settings screen.
type SettingsSnapshot struct {
	// Path is the file, shown under the rows.
	Path string
	// Settings are every key, in the order of the file.
	Settings []SettingView
	// Environments is the effective environments setting, for the badges.
	Environments environment.Set
	// Organization is the source of the organization config, shown in the header; "" when none.
	Organization string
	// DetectedClients are the AI clients found on this machine, for the mcp.clients editor.
	DetectedClients []aiclient.Client
}

// SettingKind tells how a setting is edited.
type SettingKind int

const (
	// KindText is a free text field.
	KindText SettingKind = iota
	KindToggle
	KindChoice
	KindRegion
	KindDuration
	KindTag
	KindPort
	KindClients
	KindEnvironments
)

// SettingOption is one value offered for a setting.
type SettingOption struct {
	Value string
	Label string
	Help  string
}

// SettingView is one key of the Settings screen.
type SettingView struct {
	Name        string
	Description string
	// Value is the effective value; "" means none.
	Value string
	// OverriddenBy is the flag of this run winning over the file, e.g. "--jumphost-tag".
	OverriddenBy string
	// Section groups the key in the editor: the key without its last segment, "general" at top level.
	Section string
	// Label is the short name shown for the key.
	Label string
	// Options are the values offered for a choice or a duration.
	Options []SettingOption
	Kind    SettingKind
	// Optional keys may be left empty: clearing one sets it to "".
	Optional bool
	// FromFile reports that the file sets the key; otherwise its default applies.
	FromFile bool
	// NextLaunch reports that a change applies at the next launch of airlock only.
	NextLaunch bool
	// FromOrganization reports that the organization config gives the value.
	FromOrganization bool
	// Locked keys take the organization's value: they cannot be changed here.
	Locked bool
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
	// settingsSectionsWidth is the left pane, which fits "▌   DynamoDB (off)".
	settingsSectionsWidth = 20
	// settingsLabelWidth caps the label column of the right pane; longer labels are cut.
	settingsLabelWidth = 22
	// settingsValueWidth caps the value column; a narrower pane cuts values with "…".
	settingsValueWidth = 24
	// settingsNextLaunch (a catalog key) is shown under the selected row of a NextLaunch key.
	settingsNextLaunch = "ui.settings.next_launch"
)

// SettingSections lists the sections of the Settings screen in the order of its left pane; a
// section not listed follows them, titled with its id.
var SettingSections = []string{
	"general", "aws", "mcp", "agent", "daemon", "modules.sql", "modules.k8s", "modules.cache", "modules.dynamodb", "modules.s3",
	"modules.logs",
}

// settingsSection is one entry of the left pane: its id and its settings, as indices in the snapshot.
type settingsSection struct {
	id   string
	rows []int
}

// settingsSections groups settings by section, known sections first in their order, then the
// others in the order they come; a key without section belongs to general.
func settingsSections(settings []SettingView) []settingsSection {
	var sections []settingsSection
	for idx := range settings {
		id := cmp.Or(settings[idx].Section, "general")
		pos := slices.IndexFunc(sections, func(section settingsSection) bool { return section.id == id })
		if pos < 0 {
			pos, sections = len(sections), append(sections, settingsSection{id: id})
		}
		sections[pos].rows = append(sections[pos].rows, idx)
	}
	rank := func(id string) int {
		if pos := slices.Index(SettingSections, id); pos >= 0 {
			return pos
		}
		return len(SettingSections)
	}
	slices.SortStableFunc(sections, func(a, b settingsSection) int { return cmp.Compare(rank(a.id), rank(b.id)) })
	return sections
}

// settingsSectionTitle is the name of a section in the left pane; an unknown one shows its id.
func settingsSectionTitle(id string) string {
	if slices.Contains(SettingSections, id) {
		return i18n.T("ui.settings.section." + id)
	}
	return id
}

// settingSummary is the value of view in plain words: on/off, the label of a choice, the AI
// clients, the environments, none.
func settingSummary(view SettingView) string {
	switch {
	case view.Kind == KindClients:
		return clientsSummary(view.Value)
	case view.Kind == KindEnvironments:
		return environmentsSummary(view.Value)
	case view.Value == "":
		return i18n.T("ui.none")
	case view.Kind == KindToggle && view.Value == "true":
		return i18n.T("ui.settings.on")
	case view.Kind == KindToggle && view.Value == "false":
		return i18n.T("ui.settings.off")
	case view.Kind == KindChoice:
		for _, option := range view.Options {
			if option.Value == view.Value && option.Label != "" {
				return option.Label
			}
		}
	}
	return view.Value
}

// settingsFocus is the pane taking the keys.
type settingsFocus int

const (
	focusSections settingsFocus = iota
	focusSettings
)

// settingsEditor shows the sections of config.json on the left, the settings of the selected one on
// the right, and edits them.
type settingsEditor struct {
	// err is why the snapshot could not be read: the screen is then read-only.
	err error
	// open is the editor of the selected setting, nil when none is open.
	open valueEditor
	// validate checks a value for a key without writing it (SettingsStore.Validate).
	validate func(key, value string) error
	// fieldErr is why the last write was refused, shown under the selected row or the editor.
	fieldErr string
	// requestKey and requestValue are the key and value of the last outcome.
	requestKey   string
	requestValue string
	snapshot     SettingsSnapshot
	sections     []settingsSection
	focus        settingsFocus
	// section is the selected section, cursor the selected row in it.
	section int
	cursor  int
	// confirming waits for y to reset the selected key.
	confirming bool
}

func newSettingsEditor(validate func(key, value string) error) settingsEditor {
	return settingsEditor{validate: validate}
}

// loaded shows snapshot, keeping the selected key, or else the selected section; err makes the
// screen read-only.
func (e settingsEditor) loaded(snapshot SettingsSnapshot, err error) settingsEditor {
	previous, _ := e.selected()
	previousSection := ""
	if e.section < len(e.sections) {
		previousSection = e.sections[e.section].id
	}
	e.snapshot, e.err, e.section, e.cursor, e.sections = snapshot, err, 0, 0, nil
	if err != nil {
		return e
	}
	e.sections = settingsSections(snapshot.Settings)
	for pos, section := range e.sections {
		if section.id == previousSection {
			e.section = pos
		}
		for row, idx := range section.rows {
			if snapshot.Settings[idx].Name == previous.Name {
				e.section, e.cursor = pos, row
			}
		}
	}
	return e
}

// selected returns the setting under the cursor; ok is false on an unreadable or empty snapshot.
func (e settingsEditor) selected() (SettingView, bool) {
	if e.err != nil || e.section >= len(e.sections) || e.cursor >= len(e.sections[e.section].rows) {
		return SettingView{}, false
	}
	return e.snapshot.Settings[e.sections[e.section].rows[e.cursor]], true
}

// typing reports whether printable keys belong to the editor, so q does not quit.
func (e settingsEditor) typing() bool {
	return e.open != nil || e.confirming
}

// closeEdit closes the open editor: after a save, or when it is canceled.
func (e settingsEditor) closeEdit() settingsEditor {
	e.open, e.fieldErr = nil, ""
	return e
}

// refused shows why the last write failed; an open editor stays open with its value.
func (e settingsEditor) refused(err error) settingsEditor {
	e.fieldErr = i18n.Localize(err)
	return e
}

// Update handles one message and reports what the user asked, if anything.
func (e settingsEditor) Update(msg tea.Msg) (settingsEditor, settingsOutcome, tea.Cmd) {
	if e.open != nil {
		return e.updateOpen(msg)
	}
	key, isKey := msg.(tea.KeyPressMsg)
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
	if _, ok := e.selected(); !ok {
		if key.String() == "esc" {
			return e, settingsBack, nil
		}
		return e, settingsStay, nil
	}
	e.fieldErr = ""
	if e.focus == focusSections {
		return e.updateSections(key)
	}
	return e.updateRows(key)
}

// updateSections handles the left pane: moving there previews the settings of the section.
func (e settingsEditor) updateSections(key tea.KeyPressMsg) (settingsEditor, settingsOutcome, tea.Cmd) {
	count := len(e.sections)
	switch key.String() {
	case "esc":
		return e, settingsBack, nil
	case "up", "k":
		e.section, e.cursor = (e.section-1+count)%count, 0
	case "down", "j":
		e.section, e.cursor = (e.section+1)%count, 0
	case "right", "l", "tab", "enter":
		e.focus = focusSettings
	}
	return e, settingsStay, nil
}

// updateRows handles the right pane: a toggle flips in place, enter opens the editor of the row.
func (e settingsEditor) updateRows(key tea.KeyPressMsg) (settingsEditor, settingsOutcome, tea.Cmd) {
	setting, _ := e.selected()
	count := len(e.sections[e.section].rows)
	switch key.String() {
	case "esc", "left", "h", "tab":
		e.focus = focusSections
	case "up", "k":
		e.cursor = (e.cursor - 1 + count) % count
	case "down", "j":
		e.cursor = (e.cursor + 1) % count
	case "d":
		// A key the file does not set has nothing to reset; a locked one is the organization's.
		if setting.FromFile && !setting.Locked {
			e.confirming, e.requestKey = true, setting.Name
		}
	case "space":
		if setting.Kind == KindToggle {
			return e.change(setting)
		}
	case "enter":
		return e.change(setting)
	}
	return e, settingsStay, nil
}

// change flips a toggle, opens the region picker, or opens the editor of setting; a locked
// setting opens its editor read-only, and neither flips nor picks.
func (e settingsEditor) change(setting SettingView) (settingsEditor, settingsOutcome, tea.Cmd) {
	switch {
	case setting.Locked && (setting.Kind == KindToggle || setting.Kind == KindRegion):
		return e, settingsStay, nil
	case setting.Kind == KindToggle:
		e.requestKey, e.requestValue = setting.Name, nextChoice([]string{"false", "true"}, setting.Value)
		return e, settingsSave, nil
	case setting.Kind == KindRegion:
		e.requestKey = setting.Name
		return e, settingsPickRegion, nil
	case setting.Kind == KindChoice && len(setting.Options) > 0:
		e.open = newChoiceEditor(setting)
	case setting.Kind == KindDuration:
		e.open = newDurationEditor(setting, e.validateKey(setting.Name))
	case setting.Kind == KindTag:
		e.open = newTagEditor(setting, e.validateKey(setting.Name))
	case setting.Kind == KindClients:
		e.open = newClientsEditor(setting, e.snapshot.DetectedClients)
	case setting.Kind == KindEnvironments:
		e.open = newEnvironmentsEditor(setting, e.validateKey(setting.Name))
	default:
		e.open = newTextEditor(setting, e.validateKey(setting.Name))
	}
	return e, settingsStay, textinput.Blink
}

// validateKey is the store's rule for the value of key.
func (e settingsEditor) validateKey(key string) func(string) error {
	validate := e.validate
	return func(value string) error { return validate(key, value) }
}

// updateOpen hands msg to the open editor; a save of the unchanged value only closes it.
func (e settingsEditor) updateOpen(msg tea.Msg) (settingsEditor, settingsOutcome, tea.Cmd) {
	editor, outcome, cmd := e.open.Update(msg)
	e.open = editor
	if _, isKey := msg.(tea.KeyPressMsg); isKey {
		e.fieldErr = ""
	}
	switch outcome {
	case editorCancel:
		return e.closeEdit(), settingsStay, nil
	case editorSave:
		setting, _ := e.selected()
		if editor.Value() == setting.Value {
			return e.closeEdit(), settingsStay, nil
		}
		e.requestKey, e.requestValue = setting.Name, editor.Value()
		return e, settingsSave, nil
	default:
		return e, settingsStay, cmd
	}
}

// nextChoice is the choice after current, the first one when current is none of them.
func nextChoice(choices []string, current string) string {
	return choices[(slices.Index(choices, current)+1)%len(choices)]
}

// View renders the two panes in width x height cells, then the file path.
func (e settingsEditor) View(styles Styles, width, height int) string {
	if e.err != nil {
		textWidth := min(width, resourceCardWidth)
		return strings.Join([]string{
			styles.Danger.Width(textWidth).Render(i18n.Localize(e.err)),
			"",
			styles.Muted.Width(textWidth).Render(i18n.T("ui.settings_editor.fix_file_then_come")),
			styles.Muted.Width(textWidth).Render(i18n.T("ui.settings_editor.airlock_config_show_names")),
			styles.Muted.Width(textWidth).Render(i18n.T("ui.settings_editor.airlock_config_init_force")),
		}, "\n")
	}
	leftWidth := min(settingsSectionsWidth, width/3)
	rightWidth := max(width-leftWidth-3, 1)
	paneHeight := max(height-1, 1)
	left, right := e.sectionLines(styles), e.rightLines(styles, rightWidth, paneHeight)
	lines := make([]string, 0, paneHeight+1)
	for idx := range paneHeight {
		var l, r string
		if idx < len(left) {
			l = truncate(left[idx], leftWidth)
		}
		if idx < len(right) {
			r = truncate(right[idx], rightWidth)
		}
		lines = append(lines, padRight(l, leftWidth)+styles.Rule.Render(" │ ")+r)
	}
	lines = append(lines, truncate(styles.Faint.Render(e.snapshot.Path), width))
	return strings.Join(lines, "\n")
}

// sectionLines are the lines of the left pane: modules gather under a label that is not selectable,
// and a module that is off is dimmed.
func (e settingsEditor) sectionLines(styles Styles) []string {
	lines := make([]string, 0, len(e.sections)+1)
	grouped := false
	for pos, section := range e.sections {
		indent, name, style := "", settingsSectionTitle(section.id), styles.Text
		if strings.HasPrefix(section.id, "modules.") {
			if !grouped {
				lines, grouped = append(lines, "  "+styles.Muted.Render(i18n.T("ui.settings.section.modules"))), true
			}
			indent = "  "
		}
		if e.moduleOff(section) {
			name, style = name+" "+i18n.T("ui.settings.module_off"), styles.Faint
		}
		marker := "  "
		if pos == e.section {
			marker = styles.CardMarker.Render("▌ ")
			if e.focus == focusSections {
				style = style.Bold(true)
			}
		}
		lines = append(lines, marker+indent+style.Render(name))
	}
	return lines
}

// moduleOff reports that section is a module whose enabled setting is false.
func (e settingsEditor) moduleOff(section settingsSection) bool {
	for _, idx := range section.rows {
		if setting := e.snapshot.Settings[idx]; setting.Name == section.id+".enabled" && setting.Value == "false" {
			return true
		}
	}
	return false
}

// rightLines are the lines of the right pane: the open editor, or the rows of the selected section
// scrolled to keep the selected one in view.
func (e settingsEditor) rightLines(styles Styles, width, height int) []string {
	setting, ok := e.selected()
	if !ok {
		return nil
	}
	if e.open != nil {
		extras := e.errorLines(styles, width)
		if setting.Locked {
			extras = append(extras, e.lockedLines(styles, width)...)
		}
		lines := strings.Split(e.open.View(styles, width, max(height-len(extras), 1)), "\n")
		return append(lines[:min(len(lines), max(height-len(extras), 0))], extras...)
	}
	section := e.sections[e.section]
	labelWidth := 0
	for _, idx := range section.rows {
		labelWidth = max(labelWidth, lipgloss.Width(settingLabel(e.snapshot.Settings[idx])))
	}
	labelWidth = min(labelWidth, settingsLabelWidth)
	lines := []string{styles.Text.Bold(true).Render(settingsSectionTitle(section.id)), ""}
	first, last := 0, 0
	for row, idx := range section.rows {
		view := e.snapshot.Settings[idx]
		selected := e.focus == focusSettings && row == e.cursor
		if selected {
			first = len(lines)
		}
		lines = append(lines, settingRow(styles, view, selected, labelWidth, width), "  "+styles.Muted.Render(view.Description))
		if selected {
			lines = append(lines, e.selectedLines(styles, view, width)...)
			last = len(lines)
		}
	}
	offset := min(max(last-height, 0), first)
	return lines[offset:min(len(lines), offset+height)]
}

// settingLabel is the short name of view, its key when it has none.
func settingLabel(view SettingView) string { return cmp.Or(view.Label, view.Name) }

// settingRow renders view: label, value in plain words, source, and the flag of this run.
func settingRow(styles Styles, view SettingView, selected bool, labelWidth, width int) string {
	marker, label := "  ", styles.Text
	if selected {
		marker, label = styles.CardMarker.Render("▌ "), styles.Text.Bold(true)
	}
	source := "  " + styles.Faint.Render(i18n.T("ui.settings.source_default"))
	switch {
	case view.Locked:
		source = "  " + styles.Warning.Render(i18n.T("ui.settings.source_locked"))
	case view.FromOrganization:
		source = "  " + styles.Faint.Render(i18n.T("ui.settings.source_organization"))
	case view.FromFile:
		source = "  " + styles.Faint.Render(i18n.T("ui.hint.file"))
	}
	flag := ""
	if view.OverriddenBy != "" {
		flag = "  " + styles.Warning.Render("⚑ "+view.OverriddenBy)
	}
	// The values line up; a row with a flag cuts its value shorter so that the flag shows, and in
	// a narrow pane drops its source, which the dotted key and the footer's d still tell.
	valueWidth := min(max(width-labelWidth-18, 8), settingsValueWidth)
	if flag != "" {
		room := width - labelWidth - 4 - lipgloss.Width(flag)
		if room-lipgloss.Width(source) < 8 {
			source = ""
		}
		valueWidth = max(min(valueWidth, room-lipgloss.Width(source)), 1)
	}
	value := styles.Text.Render(truncate(settingSummary(view), valueWidth))
	if view.Value == "" {
		value = styles.Muted.Render(truncate(settingSummary(view), valueWidth))
	}
	return marker + label.Render(padRight(truncate(settingLabel(view), labelWidth), labelWidth)) + "  " + padRight(value, valueWidth) + source + flag
}

// selectedLines are shown under the selected row: its key, for `airlock config set`, then what
// applies to it.
func (e settingsEditor) selectedLines(styles Styles, view SettingView, width int) []string {
	lines := []string{"  " + styles.Faint.Render(view.Name)}
	if view.OverriddenBy != "" {
		lines = append(lines, "  "+styles.Warning.Render(i18n.Format("ui.settings.overridden", view.OverriddenBy)))
	}
	if view.Locked {
		lines = append(lines, e.lockedLines(styles, width)...)
	}
	if view.NextLaunch {
		lines = append(lines, "  "+styles.Warning.Render(i18n.T(settingsNextLaunch)))
	}
	lines = append(lines, e.errorLines(styles, width)...)
	if e.confirming {
		lines = append(lines, "  "+styles.Warning.Render(i18n.Format("ui.settings.reset_confirm", view.Name)))
	}
	return lines
}

// lockedLines tell that the organization imposes the selected setting.
func (e settingsEditor) lockedLines(styles Styles, width int) []string {
	return indentedLines(styles.Warning, i18n.Format("ui.settings.locked_help", e.snapshot.Organization), width)
}

// errorLines tell why the last write was refused, if it was.
func (e settingsEditor) errorLines(styles Styles, width int) []string {
	if e.fieldErr == "" {
		return nil
	}
	return indentedLines(styles.Danger, e.fieldErr, width)
}

// indentedLines wraps text in style, two columns in, within width.
func indentedLines(style lipgloss.Style, text string, width int) []string {
	wrapped := wrapText(text, max(width-2, 1))
	for idx, line := range wrapped {
		wrapped[idx] = "  " + style.Render(line)
	}
	return wrapped
}

// padRight pads value with spaces to width columns.
func padRight(value string, width int) string {
	return value + strings.Repeat(" ", max(width-lipgloss.Width(value), 0))
}
