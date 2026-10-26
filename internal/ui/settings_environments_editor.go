package ui

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/n2jsoft-public-org/airlock/internal/environment"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

// Rows of the environment form before its patterns.
const (
	envRowName = iota
	envRowProduction
	envRowPatterns
)

// envEntry is one environment as the editor holds it.
type envEntry struct {
	name       string
	patterns   []string
	production bool
}

// environmentsEditor edits the environments setting: a list of environments, then a form for each.
// There is always exactly one production environment, and the value is written once, with s.
type environmentsEditor struct {
	validate func(string) error
	// err is why the last change or save was refused.
	err string
	// initial is the value the editor opened on: esc asks before dropping a change of it.
	initial string
	envs    []envEntry
	// profiles are the names of the local AWS profiles, for the matches of the form.
	profiles []string
	input    textinput.Model
	view     SettingView
	// cursor is the row of the list; len(envs) is "+ add an environment".
	cursor int
	// row is the row of the form: name, production, the patterns, then "+ add a pattern".
	row int
	// form shows the environment under the cursor.
	form bool
	// editing gives the keys to input, for the name or the pattern under row.
	editing bool
	// discarding waits for y to drop the changes.
	discarding bool
	// profilesLoaded and profilesFailed tell whether profiles arrived.
	profilesLoaded bool
	profilesFailed bool
}

// newEnvironmentsEditor opens on the effective value; one that does not parse opens on the default.
func newEnvironmentsEditor(view SettingView, validate func(string) error) valueEditor {
	set, err := environment.Parse(view.Value)
	if err != nil {
		set = environment.Default()
	}
	// The normalized value lists production first, then the others in config order.
	var envs []envEntry
	for part := range strings.SplitSeq(set.String(), "; ") {
		name, patterns, _ := strings.Cut(part, "=")
		env := envEntry{name: strings.TrimSuffix(name, "!"), production: strings.HasSuffix(name, "!")}
		if patterns != "" {
			env.patterns = strings.Split(patterns, ",")
		}
		envs = append(envs, env)
	}
	input := textinput.New()
	input.SetStyles(textInputStyles())
	input.Prompt = ""
	input.CharLimit = 128
	e := environmentsEditor{view: view, validate: validate, envs: envs, input: input}
	e.initial = e.Value()
	return e
}

// withProfiles gives the editor the local AWS profile names; err means they could not be listed.
func (e environmentsEditor) withProfiles(names []string, err error) environmentsEditor {
	e.profiles, e.profilesLoaded, e.profilesFailed = names, true, err != nil
	return e
}

func (e environmentsEditor) changed() bool { return e.Value() != e.initial }

func (e environmentsEditor) Update(msg tea.Msg) (valueEditor, editorOutcome, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		var cmd tea.Cmd
		if e.editing {
			e.input, cmd = e.input.Update(msg)
		}
		return e, editorStay, cmd
	}
	if e.discarding {
		e.discarding = false
		if key.String() == "y" {
			return e, editorCancel, nil
		}
		return e, editorStay, nil
	}
	if !e.view.Locked {
		// The editor before this key shares the environments: they are copied before a change.
		envs := make([]envEntry, len(e.envs))
		for idx, env := range e.envs {
			env.patterns = slices.Clone(env.patterns)
			envs[idx] = env
		}
		e.envs = envs
	}
	if e.editing {
		return e.updateField(key)
	}
	e.err = ""
	if e.form {
		return e.updateForm(key)
	}
	return e.updateList(key)
}

// updateList handles the list of environments.
func (e environmentsEditor) updateList(key tea.KeyPressMsg) (valueEditor, editorOutcome, tea.Cmd) {
	count := len(e.envs) + 1
	switch key.String() {
	case "up", "k":
		e.cursor = (e.cursor - 1 + count) % count
	case "down", "j":
		e.cursor = (e.cursor + 1) % count
	case "esc":
		if e.changed() && !e.view.Locked {
			e.discarding = true
			return e, editorStay, nil
		}
		return e, editorCancel, nil
	case "enter":
		if e.cursor < len(e.envs) {
			e.form, e.row = true, envRowName
			return e, editorStay, nil
		}
		if !e.view.Locked {
			return e.addEnvironment()
		}
	case "a":
		if !e.view.Locked {
			return e.addEnvironment()
		}
	case "x":
		switch {
		case e.view.Locked || e.cursor >= len(e.envs):
		case e.envs[e.cursor].production:
			e.err = i18n.T("ui.environments_editor.keep_production")
		default:
			e.envs = slices.Delete(e.envs, e.cursor, e.cursor+1)
		}
	case "s":
		switch {
		case e.view.Locked:
		case !e.changed():
			// Nothing to write: saving the value it opened on only closes the editor.
			return e, editorCancel, nil
		default:
			if e.err = e.checkSet(); e.err == "" {
				return e, editorSave, nil
			}
		}
	}
	return e, editorStay, nil
}

// addEnvironment adds an environment last and edits its name.
func (e environmentsEditor) addEnvironment() (valueEditor, editorOutcome, tea.Cmd) {
	e.envs = append(e.envs, envEntry{})
	e.cursor, e.form, e.row = len(e.envs)-1, true, envRowName
	return e.edit("")
}

// updateForm handles the form of the environment under the cursor.
func (e environmentsEditor) updateForm(key tea.KeyPressMsg) (valueEditor, editorOutcome, tea.Cmd) {
	env := &e.envs[e.cursor]
	count := envRowPatterns + len(env.patterns) + 1
	pattern := e.row - envRowPatterns
	switch key.String() {
	case "up", "k":
		e.row = (e.row - 1 + count) % count
	case "down", "j":
		e.row = (e.row + 1) % count
	case "esc":
		return e.leaveForm()
	}
	if e.view.Locked {
		return e, editorStay, nil
	}
	switch key.String() {
	case "enter":
		switch {
		case e.row == envRowName:
			return e.edit(env.name)
		case e.row == envRowProduction:
			e.makeProduction()
		case pattern < len(env.patterns):
			return e.edit(env.patterns[pattern])
		default:
			return e.addPattern()
		}
	case "space":
		if e.row == envRowProduction {
			e.makeProduction()
		}
	case "a":
		return e.addPattern()
	case "x":
		if pattern >= 0 && pattern < len(env.patterns) {
			env.patterns = slices.Delete(env.patterns, pattern, pattern+1)
		}
	}
	return e, editorStay, nil
}

// leaveForm goes back to the list: an environment left without a name is dropped, and a name
// refused keeps the form open.
func (e environmentsEditor) leaveForm() (valueEditor, editorOutcome, tea.Cmd) {
	env := e.envs[e.cursor]
	switch {
	case e.view.Locked:
	case env.name == "" && !env.production:
		e.envs = slices.Delete(e.envs, e.cursor, e.cursor+1)
	default:
		if e.err = e.nameError(env.name); e.err != "" {
			return e, editorStay, nil
		}
	}
	e.form = false
	return e, editorStay, nil
}

// makeProduction moves production to the environment under the cursor; it never removes it.
func (e *environmentsEditor) makeProduction() {
	for idx := range e.envs {
		e.envs[idx].production = idx == e.cursor
	}
}

// addPattern adds a pattern last and edits it.
func (e environmentsEditor) addPattern() (valueEditor, editorOutcome, tea.Cmd) {
	env := &e.envs[e.cursor]
	env.patterns = append(env.patterns, "")
	e.row = envRowPatterns + len(env.patterns) - 1
	return e.edit("")
}

// edit gives the keys to the field of row, holding value.
func (e environmentsEditor) edit(value string) (valueEditor, editorOutcome, tea.Cmd) {
	e.editing = true
	e.input.SetValue(value)
	e.input.CursorEnd()
	cmd := e.input.Focus()
	return e, editorStay, cmd
}

// updateField handles the field being typed: enter keeps a value it accepts, esc drops the edit.
func (e environmentsEditor) updateField(key tea.KeyPressMsg) (valueEditor, editorOutcome, tea.Cmd) {
	env := &e.envs[e.cursor]
	pattern := e.row - envRowPatterns
	switch key.String() {
	case "esc":
		e.editing, e.err = false, ""
		e.input.Blur()
		// A pattern added and left empty goes away.
		if pattern >= 0 && env.patterns[pattern] == "" {
			env.patterns = slices.Delete(env.patterns, pattern, pattern+1)
			e.row = min(e.row, envRowPatterns+len(env.patterns))
		}
		return e, editorStay, nil
	case "enter":
		value := e.fieldValue()
		if e.err = e.fieldError(value); e.err != "" {
			return e, editorStay, nil
		}
		e.editing = false
		e.input.Blur()
		switch {
		case e.row == envRowName:
			env.name = value
		case value == "":
			env.patterns = slices.Delete(env.patterns, pattern, pattern+1)
			e.row = min(e.row, envRowPatterns+len(env.patterns))
		default:
			env.patterns[pattern] = value
		}
		return e, editorStay, nil
	}
	before := e.input.Value()
	var cmd tea.Cmd
	e.input, cmd = e.input.Update(key)
	// Only a change is checked again: the cursor moves leave the value as it was.
	if e.input.Value() != before {
		e.err = e.fieldError(e.fieldValue())
	}
	return e, editorStay, cmd
}

// fieldValue is the typed value: a name is lower case, as the setting reads it.
func (e environmentsEditor) fieldValue() string {
	value := strings.TrimSpace(e.input.Value())
	if e.row == envRowName {
		return strings.ToLower(value)
	}
	return value
}

// fieldError is why the field under row refuses value, empty when it accepts it.
func (e environmentsEditor) fieldError(value string) string {
	if e.row == envRowName {
		return e.nameError(value)
	}
	if strings.ContainsAny(value, ",;") {
		return i18n.T("ui.environments_editor.bad_pattern")
	}
	if _, err := path.Match(value, ""); err != nil {
		return i18n.T("ui.environments_editor.bad_pattern")
	}
	return ""
}

// nameError is why name cannot name the environment under the cursor, empty when it can.
func (e environmentsEditor) nameError(name string) string {
	if environment.ValidName(name) != nil {
		return i18n.T("ui.environments_editor.bad_name")
	}
	for idx, env := range e.envs {
		if idx != e.cursor && env.name == name {
			return i18n.Format("ui.environments_editor.duplicate", name)
		}
	}
	return ""
}

// checkSet is why the edited set cannot be saved, empty when it can.
func (e environmentsEditor) checkSet() string {
	for _, env := range e.envs {
		if !env.production && env.name == environment.ProductionAlias {
			return i18n.Format("ui.environments_editor.reserved", environment.ProductionAlias)
		}
	}
	if _, err := environment.Parse(e.raw()); err != nil {
		return i18n.Localize(err)
	}
	if err := e.validate(e.Value()); err != nil {
		return i18n.Localize(err)
	}
	return ""
}

// raw is the edited set as the setting writes it, production first.
func (e environmentsEditor) raw() string {
	return e.joined(func(idx int) string { return e.envs[idx].name })
}

// joined writes the environments as the setting does, production first, each named by name.
func (e environmentsEditor) joined(name func(idx int) string) string {
	parts := make([]string, 0, len(e.envs))
	for idx, env := range e.envs {
		if env.production {
			parts = slices.Insert(parts, 0, name(idx)+"!="+strings.Join(env.patterns, ","))
		} else {
			parts = append(parts, name(idx)+"="+strings.Join(env.patterns, ","))
		}
	}
	return strings.Join(parts, "; ")
}

// Value is the edited set as Set.String writes it; one that does not parse yet is shown as typed.
func (e environmentsEditor) Value() string {
	if set, err := environment.Parse(e.raw()); err == nil {
		return set.String()
	}
	return e.raw()
}

// matches are the profiles the environment at idx receives on the edited set.
func (e environmentsEditor) matches(idx int) []string {
	// Placeholder names keep Set.Match working while a name is being typed: e<i> is envs[i].
	set, err := environment.Parse(e.joined(func(idx int) string { return fmt.Sprintf("e%d", idx) }))
	if err != nil {
		return nil
	}
	var names []string
	for _, profile := range e.profiles {
		if set.Match(profile).Name == fmt.Sprintf("e%d", idx) {
			names = append(names, profile)
		}
	}
	return names
}

func (e environmentsEditor) View(styles Styles, width, height int) string {
	var lines []string
	var focus int
	if e.form {
		lines, focus = e.formLines(styles, width)
	} else {
		lines, focus = e.listLines(styles, width)
	}
	last := focus
	if e.err != "" {
		// The error goes right under the focused line, so scrolling to the focus shows it.
		errLines := indentedLines(styles.Danger, e.err, width)
		lines = slices.Insert(lines, focus+1, errLines...)
		last += len(errLines)
	}
	tail := []string{"", truncate(styles.Muted.Render(i18n.T("ui.environments_editor.value"))+"  "+styles.Text.Render(e.Value()), width)}
	if e.discarding {
		tail = append(tail, indentedLines(styles.Warning, i18n.T("ui.environments_editor.discard"), width)...)
	}
	// The Value line and the discard prompt always show: the lines above them fit in what is left,
	// scrolled up when the focus is past it.
	room := max(height-len(tail), 1)
	if last >= room {
		lines = lines[last+1-room:]
	}
	return strings.Join(append(lines[:min(len(lines), room)], tail...), "\n")
}

// listLines are the environments, one per line, and the line under the cursor.
func (e environmentsEditor) listLines(styles Styles, width int) (lines []string, focus int) {
	lines = []string{
		truncate(styles.Text.Bold(true).Render(e.view.Label)+"  "+styles.Muted.Render(i18n.T("ui.environments_editor.subtitle")), width),
		"",
	}
	nameWidth, productionWidth := 0, lipgloss.Width(i18n.T("ui.environments_editor.production_marker"))
	for _, env := range e.envs {
		nameWidth = max(nameWidth, lipgloss.Width(env.name))
	}
	for idx, env := range e.envs {
		production := ""
		if env.production {
			production = styles.Danger.Render(i18n.T("ui.environments_editor.production_marker"))
		}
		line := e.marker(styles, idx == e.cursor) + e.label(styles, idx == e.cursor, padRight(env.name, nameWidth)) + "  " +
			padRight(production, productionWidth) + "  " + styles.Muted.Render(strings.Join(env.patterns, ", "))
		lines = append(lines, truncate(line, width))
	}
	add := e.cursor == len(e.envs)
	lines = append(lines, truncate(e.marker(styles, add)+e.label(styles, add, i18n.T("ui.environments_editor.add")), width))
	return lines, 2 + e.cursor
}

// formLines are the fields of the environment under the cursor, and the line of the focused one.
func (e environmentsEditor) formLines(styles Styles, width int) (lines []string, focus int) {
	env := e.envs[e.cursor]
	labels := []string{
		i18n.T("ui.environments_editor.name"), i18n.T("ui.environments_editor.production"),
		i18n.T("ui.environments_editor.patterns"), i18n.T("ui.environments_editor.matches"),
	}
	labelWidth := 0
	for _, label := range labels {
		labelWidth = max(labelWidth, lipgloss.Width(label))
	}
	labelWidth += 3
	field := func(row int, label, value string) string {
		if e.editing && e.row == row {
			e.input.SetWidth(max(width-labelWidth-5, 1))
			value = inputView(e.input)
		}
		return truncate(e.marker(styles, e.row == row)+styles.Muted.Render(padRight(label, labelWidth))+e.label(styles, e.row == row, value), width)
	}
	title := e.view.Label + " › " + env.name
	box := "[ ]"
	if env.production {
		box = "[x]"
	}
	production := field(envRowProduction, labels[1], box)
	if !env.production {
		production += "   " + styles.Muted.Render(i18n.Format("ui.environments_editor.production_help", e.productionName()))
	}
	lines = []string{
		truncate(styles.Text.Bold(true).Render(title), width), "",
		field(envRowName, labels[0], env.name),
		truncate(production, width), "",
		truncate("  "+styles.Muted.Render(padRight(labels[2], labelWidth)+i18n.T("ui.environments_editor.patterns_help")), width),
	}
	for idx, pattern := range env.patterns {
		lines = append(lines, field(envRowPatterns+idx, "", pattern))
	}
	lines = append(lines, field(envRowPatterns+len(env.patterns), "", i18n.T("ui.environments_editor.add_pattern")), "")
	room := max(width-2-labelWidth, 1)
	lines = append(lines, truncate("  "+styles.Muted.Render(padRight(labels[3], labelWidth))+e.matchesText(styles, room), width))
	// Before the patterns: title, blank, name, production, blank, header.
	focus = 2 + e.row
	if e.row >= envRowPatterns {
		focus += 2
	}
	return lines, focus
}

// matchesText lists the profiles of the environment under the cursor in room columns, as many as
// fit then "… N more"; while the profiles load, or when they cannot, it says so.
func (e environmentsEditor) matchesText(styles Styles, room int) string {
	switch {
	case !e.profilesLoaded:
		return styles.Faint.Render(i18n.T("ui.environments_editor.loading_profiles"))
	case e.profilesFailed:
		return styles.Warning.Render(i18n.T("ui.environments_editor.profiles_unavailable"))
	}
	names := e.matches(e.cursor)
	if len(names) == 0 {
		return styles.Faint.Render(i18n.T("ui.none"))
	}
	for shown := len(names); shown > 0; shown-- {
		text := strings.Join(names[:shown], ", ")
		if shown < len(names) {
			text += " " + i18n.Format("ui.environments_editor.more", len(names)-shown)
		}
		if lipgloss.Width(text) <= room {
			return styles.Text.Render(text)
		}
	}
	return styles.Text.Render(i18n.Format("ui.environments_editor.more", len(names)))
}

// productionName is the name of the production environment.
func (e environmentsEditor) productionName() string {
	for _, env := range e.envs {
		if env.production {
			return env.name
		}
	}
	return environment.ProductionAlias
}

func (e environmentsEditor) marker(styles Styles, focused bool) string {
	if focused {
		return styles.CardMarker.Render("▌ ")
	}
	return "  "
}

func (e environmentsEditor) label(styles Styles, focused bool, text string) string {
	if focused {
		return styles.Text.Bold(true).Render(text)
	}
	return styles.Text.Render(text)
}

func (e environmentsEditor) Keys() [][2]string {
	switch {
	case e.discarding:
		return [][2]string{{"y", i18n.T("ui.hint.discard")}, {i18n.T("ui.hint.any_key"), i18n.T("ui.hint.keep")}}
	case e.editing:
		return [][2]string{{"enter", i18n.T("ui.hint.done")}, {"esc", i18n.T("ui.hint.cancel")}}
	case e.view.Locked && e.form:
		return [][2]string{{"↑↓", i18n.T("ui.hint.move")}, {"esc", i18n.T("ui.hint.back")}}
	case e.view.Locked:
		return [][2]string{{"↑↓", i18n.T("ui.hint.move")}, {"enter", i18n.T("ui.hint.view")}, {"esc", i18n.T("ui.hint.back")}}
	case e.form:
		// The arrows are left out to fit 80 columns.
		return [][2]string{
			{"enter", i18n.T("ui.hint.edit")}, {"space", i18n.T("ui.hint.toggle")}, {"a", i18n.T("ui.hint.add")},
			{"x", i18n.T("ui.hint.delete")}, {"esc", i18n.T("ui.hint.back")},
		}
	}
	// Save comes first: the keys after it give way first in a narrow footer.
	return [][2]string{
		{"s", i18n.T("ui.hint.save")}, {"enter", i18n.T("ui.hint.edit")}, {"a", i18n.T("ui.hint.add")},
		{"x", i18n.T("ui.hint.delete")}, {"esc", i18n.T("ui.hint.cancel")},
	}
}

// environmentsSummary names the environments, production first.
func environmentsSummary(value string) string {
	set, err := environment.Parse(value)
	if err != nil {
		return value
	}
	return strings.Join(set.Names(), ", ")
}
