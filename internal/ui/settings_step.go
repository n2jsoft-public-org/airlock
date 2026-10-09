package ui

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/n2jsoft/airlock/internal/i18n"
)

// settingsLoadedMsg carries the snapshot of the Settings screen; written reports that it follows
// a successful write, which closes the edit.
type settingsLoadedMsg struct {
	err      error
	snapshot SettingsSnapshot
	written  bool
}

// settingRefusedMsg reports a write the store refused.
type settingRefusedMsg struct {
	err error
}

// openSettings shows the Settings screen; leavesToHost makes esc return to the dashboard.
func (m jumphostWizardModel) openSettings(leavesToHost bool) (tea.Model, tea.Cmd) {
	m.settingsReturn, m.settingsLeavesToHost = m.step, leavesToHost
	m.settingsEditor = newSettingsEditor()
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
	switch {
	case editor.editing:
		return [][2]string{{"enter", i18n.T("ui.hint.save")}, {"esc", i18n.T("ui.hint.cancel")}, {"ctrl+c", m.quitLabel()}}
	case editor.confirming:
		return [][2]string{{"y", i18n.T("ui.hint.reset")}, {i18n.T("ui.hint.any_key"), i18n.T("ui.hint.keep")}}
	case editor.err != nil:
		return [][2]string{{"esc", i18n.T("ui.hint.back")}, quit}
	}
	keys := [][2]string{{"↑↓", i18n.T("ui.hint.move")}, {"enter", i18n.T("ui.hint.change")}}
	if setting, ok := editor.selected(); ok && setting.FromFile {
		keys = append(keys, [2]string{"d", i18n.T("ui.hint.reset")})
	}
	return append(keys, [2]string{"esc", i18n.T("ui.hint.back")}, help, quit)
}
