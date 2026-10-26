package ui

import (
	"errors"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

// settingsLoadedMsg carries the snapshot of the Settings screen; written reports that it follows
// a successful write, which closes the open editor.
type settingsLoadedMsg struct {
	err      error
	snapshot SettingsSnapshot
	written  bool
}

// settingRefusedMsg reports a write the store refused; an open editor stays open with its value.
type settingRefusedMsg struct {
	err error
}

// settingsProfilesMsg carries the names of the local AWS profiles, for the environments editor.
type settingsProfilesMsg struct {
	err   error
	names []string
}

// errNoProfileDiscovery is the profiles of a wizard that cannot list them.
var errNoProfileDiscovery = errors.New("no AWS profile discovery")

// loadSettingsProfilesCmd lists the AWS profiles for the environments editor.
func (m jumphostWizardModel) loadSettingsProfilesCmd() tea.Cmd {
	runner, ctx := m.runner, m.ctx
	return func() tea.Msg {
		if runner == nil {
			return settingsProfilesMsg{err: errNoProfileDiscovery}
		}
		profiles, err := runner.ListAWSProfileSummaries(ctx)
		names := make([]string, 0, len(profiles))
		for _, profile := range profiles {
			names = append(names, profile.Name)
		}
		return settingsProfilesMsg{names: names, err: err}
	}
}

// handleSettingsProfiles gives the profiles to the environments editor; they are ignored once it
// closed or the wizard left the screen.
func (m jumphostWizardModel) handleSettingsProfiles(msg settingsProfilesMsg) (tea.Model, tea.Cmd) {
	if editor, ok := m.settingsEditor.open.(environmentsEditor); ok && m.step == jumphostStepSettings {
		m.settingsEditor.open = editor.withProfiles(msg.names, msg.err)
	}
	return m, nil
}

// openSettings shows the Settings screen; leavesToHost makes esc return to the dashboard.
func (m jumphostWizardModel) openSettings(leavesToHost bool) (tea.Model, tea.Cmd) {
	m.settingsReturn, m.settingsLeavesToHost = m.step, leavesToHost
	m.settingsEditor = newSettingsEditor(m.settings.Validate)
	m.step = jumphostStepSettings
	m.errorMsg = ""
	m.loading = true
	store := m.settings
	return m, tea.Batch(func() tea.Msg {
		snapshot, err := store.Snapshot()
		return settingsLoadedMsg{snapshot: snapshot, err: err}
	}, m.loadingUI.start(i18n.T("ui.settings_step.reading_settings"), []string{i18n.T("ui.settings_step.leafing_through_config_json")}))
}

// writeSetting runs write, then reads the snapshot again; the screen waits for both.
func (m jumphostWizardModel) writeSetting(label string, write func() error) (tea.Model, tea.Cmd) {
	m.loading = true
	store := m.settings
	return m, tea.Batch(func() tea.Msg {
		if err := write(); err != nil {
			return settingRefusedMsg{err: err}
		}
		snapshot, err := store.Snapshot()
		return settingsLoadedMsg{snapshot: snapshot, err: err, written: true}
	}, m.loadingUI.start(label, []string{i18n.T("ui.settings_step.writing_it_down_in")}))
}

// handleSettingsLoaded shows the snapshot; the environment badges follow its environments. A
// snapshot arriving after the wizard left the screen is ignored, as is its loading.
func (m jumphostWizardModel) handleSettingsLoaded(msg settingsLoadedMsg) (tea.Model, tea.Cmd) {
	if m.step != jumphostStepSettings {
		return m, nil
	}
	m.loading = false
	m.loadingUI.stop()
	if msg.written {
		m.settingsEditor = m.settingsEditor.closeEdit()
	}
	m.settingsEditor = m.settingsEditor.loaded(msg.snapshot, msg.err)
	if msg.err == nil {
		m.environments = msg.snapshot.Environments
	}
	return m, nil
}

// handleSettingRefused shows why the write failed; an edit stays open with the typed value. A
// refusal arriving after the wizard left the screen is ignored.
func (m jumphostWizardModel) handleSettingRefused(msg settingRefusedMsg) (tea.Model, tea.Cmd) {
	if m.step != jumphostStepSettings {
		return m, nil
	}
	m.loading = false
	m.loadingUI.stop()
	m.settingsEditor = m.settingsEditor.refused(msg.err)
	return m, nil
}

// updateSettingsStep handles the Settings screen.
func (m jumphostWizardModel) updateSettingsStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, wasEnvironments := m.settingsEditor.open.(environmentsEditor)
	editor, outcome, cmd := m.settingsEditor.Update(msg)
	m.settingsEditor = editor
	store, key, value := m.settings, editor.requestKey, editor.requestValue
	switch outcome {
	case settingsBack:
		return m.leaveSettings()
	case settingsSave:
		return m.writeSetting(i18n.Format("ui.saving", key), func() error { return store.Set(key, value) })
	case settingsReset:
		return m.writeSetting(i18n.Format("ui.settings.resetting", key), func() error { return store.Unset(key) })
	case settingsPickRegion:
		setting, _ := editor.selected()
		m.regionPicker = newRegionPicker(setting.Value)
		m.pickingRegion, m.regionForSettings = true, true
		return m, textinput.Blink
	default:
		// The environments editor shows the profiles each environment receives.
		if _, isEnvironments := editor.open.(environmentsEditor); isEnvironments && !wasEnvironments {
			cmd = tea.Batch(cmd, m.loadSettingsProfilesCmd())
		}
		return m, cmd
	}
}

// updateSettingsRegionPicker chooses the region setting the picker was opened for.
func (m jumphostWizardModel) updateSettingsRegionPicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	picker, outcome, cmd := m.regionPicker.Update(msg)
	m.regionPicker = picker
	switch outcome {
	case regionPickerCanceled:
		m.pickingRegion, m.regionForSettings = false, false
		return m, nil
	case regionPickerChosen:
		m.pickingRegion, m.regionForSettings = false, false
		region, _ := picker.choice()
		// Like the editors, picking the current region writes nothing.
		if setting, _ := m.settingsEditor.selected(); region == setting.Value {
			return m, nil
		}
		store, key := m.settings, m.settingsEditor.requestKey
		return m.writeSetting(i18n.Format("ui.saving", key), func() error { return store.Set(key, region) })
	default:
		return m, cmd
	}
}

// leaveSettings returns where the Settings screen was opened from. The default region is resolved
// again, unless one was chosen, so the next connection follows defaultRegion. The start screen
// loads the recent connections again, so their environment badges follow environments.
func (m jumphostWizardModel) leaveSettings() (tea.Model, tea.Cmd) {
	if m.settingsLeavesToHost {
		m.canceled = true
		return m, m.finish()
	}
	m.step = m.settingsReturn
	m.errorMsg = ""
	var resolve tea.Cmd
	if m.options.AwsRegion == "" {
		m.defaultRegion, m.regionResolved = "", false
		resolve = m.resolveRegionCmd(m.ctx)
	}
	if m.step != jumphostStepStart {
		return m, resolve
	}
	m.loading = true
	return m, tea.Batch(resolve, m.loadSavedCmd(), m.loadingUI.start(i18n.T("ui.jumphost_flow.loading_recent_connections"), []string{
		i18n.T("ui.jumphost_flow.remembering_where_you_have"),
	}))
}

// settingsKeys are the keys of the Settings screen.
func (m jumphostWizardModel) settingsKeys(help, quit [2]string) [][2]string {
	editor := m.settingsEditor
	setting, ok := editor.selected()
	switch {
	case editor.open != nil:
		// ctrl+c still quits, but its hint would push the editor's own keys out of 80 columns.
		return editor.open.Keys()
	case editor.confirming:
		return [][2]string{{"y", i18n.T("ui.hint.reset")}, {i18n.T("ui.hint.any_key"), i18n.T("ui.hint.keep")}}
	case !ok:
		return [][2]string{{"esc", i18n.T("ui.hint.back")}, quit}
	}
	keys := [][2]string{{"←→", i18n.T("ui.hint.panes")}, {"↑↓", i18n.T("ui.hint.move")}}
	// The right pane shows the way back to the sections first, and leaves ↑↓ out to fit 80 columns.
	if editor.focus == focusSettings {
		keys = [][2]string{{"←/esc", i18n.T("ui.hint.back")}}
	}
	switch {
	case editor.focus == focusSections:
	case setting.Kind == KindToggle && !setting.Locked:
		keys = append(keys, [2]string{"space", i18n.T("ui.hint.toggle")})
	case !setting.Locked:
		keys = append(keys, [2]string{"enter", i18n.T("ui.hint.change")})
	case setting.Kind != KindToggle && setting.Kind != KindRegion:
		// A locked key opens read-only; the line under the row tells the organization imposes it.
		keys = append(keys, [2]string{"enter", i18n.T("ui.hint.view")})
	}
	if editor.focus == focusSettings {
		if setting.FromFile && !setting.Locked {
			keys = append(keys, [2]string{"d", i18n.T("ui.hint.default")})
		}
		return append(keys, help, quit)
	}
	return append(keys, [2]string{"esc", i18n.T("ui.hint.back")}, help, quit)
}
