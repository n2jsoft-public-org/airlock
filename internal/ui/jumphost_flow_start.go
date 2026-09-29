package ui

import (
	"time"

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

// jumphostLookupMsg carries the target of a recent connection, checked before reconnecting.
type jumphostLookupMsg struct {
	err    error
	target jumphost.ResourceSummary
}

// jumphostForgotMsg reports the removal of a recent connection.
type jumphostForgotMsg struct {
	err error
}

// handleSavedLoaded opens the start screen on a history, else the Type step of a new connection.
// An unreadable history never blocks a connection: it only warns.
func (m jumphostWizardModel) handleSavedLoaded(msg jumphostSavedLoadedMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	m.loadingUI.stop()
	m.hasHistory = len(msg.connections) > 0
	if m.hasHistory {
		m.recent = newRecentPicker(m.styles, msg.connections, m.active, time.Now())
		m.recent.SetSize(wizardBodySize(m.width, m.height))
		m.flow = flowStart
		m.step = jumphostStepStart
	} else {
		m.flow = flowNew
		m.step = jumphostStepType
	}
	if msg.err != nil {
		m.errorMsg = "connections.json unreadable: " + msg.err.Error()
	}
	return m, nil
}

// updateStartStep handles the start screen: reconnect, new connection, search or forget.
func (m jumphostWizardModel) updateStartStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	picker, outcome, cmd := m.recent.Update(msg)
	m.recent = picker
	switch outcome {
	case recentPickerPicked:
		item, _ := picker.selected()
		return m.openRecent(item.conn)
	case recentPickerNew:
		m.flow = flowNew
		m.step = jumphostStepType
		m.errorMsg = ""
		return m, nil
	case recentPickerSearch:
		return m.beginSearch()
	case recentPickerForget:
		item, _ := picker.selected()
		return m, m.forgetCmd(item.conn.Ref)
	default:
		return m, cmd
	}
}

// openRecent reconnects to a remembered target: its region, type and name, and its profile
// unless --profile gave one; a profile that no longer exists is asked again.
func (m jumphostWizardModel) openRecent(conn jumphost.SavedConnection) (tea.Model, tea.Cmd) {
	m.flow = flowRecent
	m.errorMsg = ""
	m.options = m.base
	m.options.ResourceType = conn.Ref.Type
	m.options.Name = conn.Ref.Name
	m.options.AwsRegion = conn.Ref.Region
	m.recentNeedsProfile = m.askProfile && (conn.ProfileMissing || conn.Prefs.Profile == "")
	if m.recentNeedsProfile {
		return m.enterProfileStep()
	}
	if m.askProfile {
		m.options.AwsProfile = conn.Prefs.Profile
	}
	return m.startLookup()
}

// startLookup checks that the recent target still exists and whether it accepts AWS IAM.
func (m jumphostWizardModel) startLookup() (tea.Model, tea.Cmd) {
	m.errorMsg = ""
	m.loading = true
	opts := m.options
	lookup := func() tea.Msg {
		target, err := m.runner.FindResource(m.ctx, &opts)
		return jumphostLookupMsg{target: target, err: err}
	}
	return m, tea.Batch(lookup, m.loadingUI.start("Checking "+opts.Name+"...", []string{
		"Making sure it is still there...",
		"Knocking on the target's door...",
	}))
}

// handleLookup continues to the review of a found target; a lookup error returns to the start screen.
func (m jumphostWizardModel) handleLookup(msg jumphostLookupMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	m.loadingUI.stop()
	if msg.err != nil {
		start, cmd := m.returnToStart()
		start.errorMsg = msg.err.Error()
		return start, cmd
	}
	m.targetIAMAuth = m.options.ResourceType == "rds" && msg.target.IAMAuth
	return m.startLoadingPort()
}

func (m jumphostWizardModel) forgetCmd(ref jumphost.TargetRef) tea.Cmd {
	return func() tea.Msg {
		return jumphostForgotMsg{err: m.runner.ForgetConnection(m.ctx, ref)}
	}
}

// handleForgot reloads the history once an entry is forgotten.
func (m jumphostWizardModel) handleForgot(msg jumphostForgotMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.errorMsg = msg.err.Error()
		return m, nil
	}
	m.errorMsg = ""
	return m, m.loadSavedCmd()
}
