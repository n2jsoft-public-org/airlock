package ui

import (
	"errors"
	"io"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/n2jsoft-public-org/airlock/internal/environment"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

// firstRunPages are the keys the first-run wizard asks, in order; a locked one is not asked.
var firstRunPages = []string{"language", "organizationConfig", "mode", "environments", "agent.autoConnect", "mcp.clients"}

// Pages after the keys.
var (
	firstRunSummary = len(firstRunPages)
	firstRunDoctor  = len(firstRunPages) + 1
)

// firstRunProduction is the environments proposed when nothing sets them: with the default, no
// profile is production.
const firstRunProduction = "prod!=*prod*"

// FirstRunSetup is what the first-run wizard reads and writes: internal/cli gives it.
type FirstRunSetup struct {
	// Load returns the settings over the organization config at source, "" for none; a managed
	// organization ignores source. The error tells why the organization cannot be read.
	Load func(source string) (SettingsSnapshot, error)
	// Validate is the store's rule for the value of key.
	Validate func(key, value string) error
	// Save writes values, in order, to config.json, which it creates even when values is empty.
	Save func(values [][2]string) error
	// Profiles lists the local AWS profiles, for the environments editor.
	Profiles func() ([]string, error)
	// SetLanguage switches airlock to a value of the language setting.
	SetLanguage func(value string)
	// Managed is the organization config a managed file imposes, "" when none: it is not asked.
	Managed string
	// Snapshot is the settings before any answer.
	Snapshot SettingsSnapshot
}

// FirstRunResult is how the first-run wizard ended; the zero value is a quit (ctrl+c).
type FirstRunResult struct {
	// Saved: config.json was written, with the answers or, Skipped, empty.
	Saved, Skipped bool
	// Doctor asks for airlock doctor.
	Doctor bool
}

type firstRunLoadedMsg struct {
	err      error
	source   string
	snapshot SettingsSnapshot
}

type firstRunSavedMsg struct{ err error }

type firstRunProfilesMsg struct {
	err   error
	names []string
}

// firstRunModel asks the keys of firstRunPages with the editors of the Settings screen, then
// shows a summary, saves, and offers airlock doctor.
type firstRunModel struct {
	styles Styles
	setup  FirstRunSetup
	editor valueEditor
	// answers are the values chosen, by key.
	answers map[string]string
	// source is the organization config the snapshot is loaded over.
	source   string
	err      string
	snapshot SettingsSnapshot
	page     int
	width    int
	height   int
	result   FirstRunResult
	// reviewing shows what the organization config sets, before the next page.
	reviewing bool
	skipping  bool
	loading   bool
}

func newFirstRunModel(setup FirstRunSetup, styles Styles) firstRunModel {
	m := firstRunModel{setup: setup, snapshot: setup.Snapshot, answers: map[string]string{}, styles: styles}
	m.source = m.setting("organizationConfig").Value
	return m.enter(0)
}

func (m firstRunModel) Init() tea.Cmd { return nil }

// setting is the view of key in the snapshot.
func (m firstRunModel) setting(key string) SettingView {
	for idx := range m.snapshot.Settings {
		if m.snapshot.Settings[idx].Name == key {
			return m.snapshot.Settings[idx]
		}
	}
	return SettingView{Name: key}
}

// view is the view of the key of page with its answer, or the value proposed for it.
func (m firstRunModel) view(page int) SettingView {
	key := firstRunPages[page]
	view := m.setting(key)
	switch answer, ok := m.answers[key]; {
	case ok && !view.Locked:
		view.Value = answer
	case key == "environments" && !view.FromFile && !view.FromOrganization && view.Value == environment.Default().String():
		view.Value = firstRunProduction
	}
	return view
}

// asked reports whether the wizard opens page: a locked key is not asked. The organization a
// managed file imposes is shown on its page instead.
func (m firstRunModel) asked(page int) bool {
	return !m.view(page).Locked
}

// enter opens page, or the next asked one in direction step.
func (m firstRunModel) enter(page int) firstRunModel {
	return m.move(page, 1)
}

func (m firstRunModel) move(page, step int) firstRunModel {
	for page >= 0 && page < firstRunSummary && !m.asked(page) {
		page += step
	}
	if page < 0 {
		m.skipping = true
		return m
	}
	m.page, m.err, m.reviewing, m.editor = page, "", false, nil
	if page < firstRunSummary && (firstRunPages[page] != "organizationConfig" || m.setup.Managed == "") {
		m.editor = m.editorFor(m.view(page))
	}
	if page == firstRunDoctor {
		m.editor = newChoiceEditor(SettingView{Options: []SettingOption{
			{Value: "yes", Label: i18n.T("ui.first_run.doctor_yes"), Help: i18n.T("ui.first_run.doctor_yes.help")},
			{Value: "no", Label: i18n.T("ui.first_run.doctor_no"), Help: i18n.T("ui.first_run.doctor_no.help")},
		}})
	}
	return m
}

func (m firstRunModel) editorFor(view SettingView) valueEditor {
	validate := func(value string) error { return m.setup.Validate(view.Name, value) }
	switch view.Kind {
	case KindChoice:
		return newChoiceEditor(view)
	case KindEnvironments:
		return newEnvironmentsEditor(view, validate)
	case KindClients:
		return newClientsEditor(view, m.snapshot.DetectedClients)
	default:
		return newTextEditor(view, validate)
	}
}

func (m firstRunModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case firstRunLoadedMsg:
		return m.loaded(msg)
	case firstRunSavedMsg:
		m.loading = false
		if msg.err != nil {
			m.err = i18n.Localize(msg.err)
			return m, nil
		}
		if m.skipping {
			m.result = FirstRunResult{Saved: true, Skipped: true}
			return m, tea.Quit
		}
		m.result.Saved = true
		return m.enter(firstRunDoctor), nil
	case firstRunProfilesMsg:
		if editor, ok := m.editor.(environmentsEditor); ok {
			m.editor = editor.withProfiles(msg.names, msg.err)
		}
		return m, nil
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.result = FirstRunResult{}
			return m, tea.Quit
		}
		if m.loading {
			return m, nil
		}
		return m.key(msg)
	}
	if m.editor != nil {
		var cmd tea.Cmd
		m.editor, _, cmd = m.editor.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m firstRunModel) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case m.skipping:
		if msg.String() != "y" {
			m.skipping = false
			return m, nil
		}
		m.loading = true
		return m, m.save(nil)
	case m.page == firstRunSummary:
		switch msg.String() {
		case "enter":
			m.loading, m.err = true, ""
			return m, m.save(m.changes())
		case "esc":
			return m.move(firstRunSummary-1, -1), nil
		}
		return m, nil
	case m.editor == nil || m.reviewing:
		// The organization a managed file imposes, or the one just read: enter goes on.
		switch msg.String() {
		case "enter":
			return m.next()
		case "esc":
			if m.reviewing {
				m.reviewing = false
				return m, nil
			}
			return m.move(m.page-1, -1), nil
		}
		return m, nil
	}
	editor, outcome, cmd := m.editor.Update(msg)
	m.editor = editor
	switch outcome {
	case editorCancel:
		// The environments editor closes on s without a change: here it accepts the value proposed.
		if _, ok := editor.(environmentsEditor); ok && msg.String() == "s" {
			return m.saved(editor.Value())
		}
		if m.page == firstRunDoctor {
			m.result.Doctor = false
			return m, tea.Quit
		}
		return m.move(m.page-1, -1), nil
	case editorSave:
		return m.saved(editor.Value())
	default:
		return m, cmd
	}
}

// saved records value for the page; the language switches airlock and the organization is read,
// both reloading the snapshot before the next page.
func (m firstRunModel) saved(value string) (tea.Model, tea.Cmd) {
	if m.page == firstRunDoctor {
		m.result.Doctor = value == "yes"
		return m, tea.Quit
	}
	key := firstRunPages[m.page]
	m.answers[key] = value
	switch key {
	case "language":
		m.setup.SetLanguage(value)
		return m.load(m.source)
	case "organizationConfig":
		return m.load(value)
	}
	return m.next()
}

// next opens the page after this one, profiles loading for the environments editor.
func (m firstRunModel) next() (tea.Model, tea.Cmd) {
	m = m.enter(m.page + 1)
	if _, ok := m.editor.(environmentsEditor); ok {
		profiles := m.setup.Profiles
		return m, func() tea.Msg {
			names, err := profiles()
			return firstRunProfilesMsg{names: names, err: err}
		}
	}
	return m, nil
}

func (m firstRunModel) load(source string) (tea.Model, tea.Cmd) {
	m.loading, m.err = true, ""
	load := m.setup.Load
	return m, func() tea.Msg {
		snapshot, err := load(source)
		return firstRunLoadedMsg{snapshot: snapshot, err: err, source: source}
	}
}

// loaded shows the snapshot over source; an organization that cannot be read stays on its page.
func (m firstRunModel) loaded(msg firstRunLoadedMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	if msg.err != nil {
		m.err = i18n.Localize(msg.err)
		return m, nil
	}
	m.snapshot, m.source = msg.snapshot, msg.source
	if firstRunPages[m.page] == "organizationConfig" && msg.source != "" {
		m.reviewing = true
		return m, nil
	}
	return m.next()
}

func (m firstRunModel) save(values [][2]string) tea.Cmd {
	save := m.setup.Save
	return func() tea.Msg { return firstRunSavedMsg{err: save(values)} }
}

// changes are the answers that differ from the value that applies, the organization first: its
// locks then apply to the keys after it.
func (m firstRunModel) changes() [][2]string {
	var values [][2]string
	for page, key := range firstRunPages {
		answer, ok := m.answers[key]
		view := m.setting(key)
		if !ok && key == "environments" {
			answer, ok = m.view(page).Value, true
		}
		if ok && !view.Locked && answer != view.Value {
			values = append(values, [2]string{key, answer})
		}
	}
	return values
}

func (m firstRunModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m firstRunModel) render() string {
	bodyWidth, bodyHeight := m.styles.frameBodySize(m.width, m.height, m.err, nil)
	frame := wizardFrame{title: "airlock", tagline: i18n.T("ui.first_run.tagline"), errorMsg: m.err}
	var lines []string
	switch m.page {
	case firstRunSummary:
		frame.heading = i18n.T("ui.first_run.heading.summary")
		lines = m.summaryLines(bodyWidth)
		frame.keys = [][2]string{{"enter", i18n.T("ui.hint.save")}, {"esc", i18n.T("ui.hint.back")}}
	case firstRunDoctor:
		frame.heading = i18n.T("ui.first_run.heading.doctor")
		lines = append(indentedLines(m.styles.Success, i18n.Format("ui.first_run.saved", m.snapshot.Path), bodyWidth), "")
		lines = append(lines, m.editor.View(m.styles, bodyWidth, bodyHeight))
		frame.keys = [][2]string{{"↑↓", i18n.T("ui.hint.move")}, {"enter", i18n.T("ui.hint.pick")}}
	default:
		key := firstRunPages[m.page]
		frame.heading = i18n.T("ui.first_run.heading." + key)
		frame.hint = i18n.Format("ui.first_run.step", m.page+1, firstRunSummary)
		switch {
		case m.editor == nil || m.reviewing:
			lines = append(lines, m.organizationLines(bodyWidth)...)
			frame.keys = [][2]string{{"enter", i18n.T("ui.first_run.hint.continue")}, {"esc", i18n.T("ui.hint.back")}}
		default:
			lines = append(indentedLines(m.styles.Muted, i18n.T("ui.first_run.help."+key), bodyWidth), "")
			lines = append(lines, m.editor.View(m.styles, bodyWidth, bodyHeight-len(lines)))
			frame.keys = m.editorKeys()
		}
	}
	switch {
	case m.loading:
		lines = append(lines, "", m.styles.Muted.Render(i18n.T("ui.first_run.loading")))
	case m.skipping:
		lines = append(lines, "", m.styles.Warning.Render(i18n.T("ui.first_run.skip_confirm")))
	}
	frame.keys = append(frame.keys, [2]string{"ctrl+c", i18n.T("ui.hint.quit")})
	frame.body = strings.Join(lines, "\n")
	return m.styles.renderWizardFrame(frame, m.width, m.height)
}

// editorKeys are the keys of the open editor, esc going back a page, or skipping on the first.
func (m firstRunModel) editorKeys() [][2]string {
	keys := m.editor.Keys()
	for idx, k := range keys {
		if k[0] == "esc" {
			keys[idx][1] = i18n.T("ui.hint.back")
			if !m.anyAskedBefore() {
				keys[idx][1] = i18n.T("ui.first_run.hint.skip")
			}
		}
	}
	return keys
}

func (m firstRunModel) anyAskedBefore() bool {
	for page := range m.page {
		if m.asked(page) {
			return true
		}
	}
	return false
}

// organizationLines tell which organization config applies, what it sets and what it locks.
func (m firstRunModel) organizationLines(width int) []string {
	source := m.setup.Managed
	if source == "" {
		source = m.snapshot.Organization
	}
	lines := indentedLines(m.styles.Text, i18n.Format("ui.first_run.organization", source), width)
	var set []string
	for idx := range m.snapshot.Settings {
		if view := m.snapshot.Settings[idx]; view.FromOrganization {
			line := "  " + padRight(settingLabel(view), settingsLabelWidth) + " " + settingSummary(view)
			if view.Locked {
				line += "  " + m.styles.Warning.Render(i18n.T("ui.first_run.locked"))
			}
			set = append(set, truncate(line, width))
		}
	}
	if len(set) == 0 {
		return append(lines, "", m.styles.Muted.Render("  "+i18n.T("ui.first_run.organization_sets_nothing")))
	}
	return append(append(lines, "", m.styles.Muted.Render("  "+i18n.T("ui.first_run.organization_sets"))), set...)
}

// summaryLines list every key of the wizard with its value, the locked ones as imposed.
func (m firstRunModel) summaryLines(width int) []string {
	var lines []string
	for page := range firstRunPages {
		view := m.view(page)
		value := settingSummary(view)
		if view.Name == "organizationConfig" {
			value = m.setup.Managed
			if value == "" {
				value = m.snapshot.Organization
			}
			if value == "" {
				value = i18n.T("ui.none")
			}
		}
		line := "  " + padRight(settingLabel(view), settingsLabelWidth) + " " + m.styles.Text.Bold(true).Render(value)
		if view.Locked || (view.Name == "organizationConfig" && m.setup.Managed != "") {
			line += "  " + m.styles.Muted.Render(i18n.T("ui.first_run.imposed"))
		}
		lines = append(lines, truncate(line, width))
	}
	return append(append(lines, ""), indentedLines(m.styles.Muted, i18n.Format("ui.first_run.summary_path", m.snapshot.Path), width)...)
}

// RunFirstRun asks the first-run settings in the terminal of in and out, and saves them.
func RunFirstRun(setup FirstRunSetup, in io.Reader, out io.Writer) (FirstRunResult, error) {
	dark := true
	if inFile, ok := in.(*os.File); ok {
		if outFile, ok := out.(*os.File); ok {
			dark = lipgloss.HasDarkBackground(inFile, outFile)
		}
	}
	final, err := tea.NewProgram(newFirstRunModel(setup, NewStyles(dark)), tea.WithInput(in), tea.WithOutput(out)).Run()
	if errors.Is(err, tea.ErrInterrupted) {
		return FirstRunResult{}, nil
	}
	if err != nil {
		return FirstRunResult{}, err
	}
	m, ok := final.(firstRunModel)
	if !ok {
		return FirstRunResult{}, nil
	}
	return m.result, nil
}
