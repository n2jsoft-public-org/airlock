package ui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// jumphostSavedLoadedMsg carries the remembered connections shown on the start screen.
type jumphostSavedLoadedMsg struct {
	err         error
	connections []jumphost.SavedConnection
}

// loadSavedCmd lists the remembered connections, filtered by --profile and --region when given.
func (m jumphostWizardModel) loadSavedCmd() tea.Cmd {
	profile := ""
	if !m.askProfile {
		profile = m.base.AwsProfile
	}
	region := m.base.AwsRegion
	return func() tea.Msg {
		connections, err := m.runner.ListSavedConnections(m.ctx, profile, region)
		return jumphostSavedLoadedMsg{connections: connections, err: err}
	}
}

// handleSavedLoaded opens the start screen on a history, else the Type step of a new connection.
// An unreadable history never blocks a connection: it only warns.
func (m jumphostWizardModel) handleSavedLoaded(msg jumphostSavedLoadedMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	m.loadingUI.stop()
	m.hasHistory = false
	m.flow = flowNew
	m.step = jumphostStepType
	if msg.err != nil {
		m.errorMsg = "connections.json unreadable: " + msg.err.Error()
	}
	return m, nil
}

// updateStartStep handles the start screen.
func (m jumphostWizardModel) updateStartStep(tea.Msg) (tea.Model, tea.Cmd) {
	return m, nil
}
