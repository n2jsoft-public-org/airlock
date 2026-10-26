package ui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
	"github.com/n2jsoft-public-org/airlock/internal/sessions"
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
// An unreadable history never blocks a connection: it only warns. A history arriving once a flow
// started is ignored, so a late reload never changes an active flow. `airlock direct <name>` then
// opens that database's review, once.
func (m jumphostWizardModel) handleSavedLoaded(msg jumphostSavedLoadedMsg) (tea.Model, tea.Cmd) {
	if m.flow != flowStart {
		return m, nil
	}
	m.loading = false
	m.loadingUI.stop()
	m.hasHistory = len(msg.connections) > 0
	if m.hasHistory {
		// Update sizes it to the frame body.
		m.recent = newRecentPicker(m.styles, msg.connections, m.active, m.environments, m.directDefinitions(), time.Now())
		m.recent.settings = m.settings != nil
		m.flow = flowStart
		m.step = jumphostStepStart
	} else {
		m.flow = flowNew
		m.step = jumphostStepType
	}
	if msg.err != nil {
		m.errorMsg = i18n.Format("ui.wizard.saved_unreadable", msg.err)
	}
	if name := m.openDirect; name != "" {
		m.openDirect = ""
		return m.openDirectAtStart(name)
	}
	if m.dockerAtStart {
		return m.openDockerAtStart()
	}
	if m.settingsAtStart {
		m.settingsAtStart = false
		return m.openSettings(true)
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
	case recentPickerSettings:
		return m.openSettings(false)
	case recentPickerBack:
		return m.goBack()
	case recentPickerSwitch:
		item, _ := picker.selected()
		m.canceled = true
		m.switchTo = item.key()
		return m, m.finish()
	case recentPickerForget:
		// The start screen waits for the reload: the forgotten entry must not be picked meanwhile.
		item, _ := picker.selected()
		m.loading = true
		return m, tea.Batch(m.forgetCmd(item.conn.Ref), m.loadingUI.start(i18n.Format("ui.wizard.forgetting", item.conn.Ref.Name), []string{
			i18n.T("ui.jumphost_flow_start.tearing_page_out_of"),
		}))
	default:
		return m, cmd
	}
}

// openRecent reconnects to a remembered target: its region, type and name, and its profile
// unless --profile gave one; a profile that no longer exists is asked again.
func (m jumphostWizardModel) openRecent(conn jumphost.SavedConnection) (tea.Model, tea.Cmd) {
	if conn.Ref.Type == sessions.DirectResourceType {
		return m.openRecentDirect(conn)
	}
	m.flow = flowRecent
	m.errorMsg = ""
	m.options = m.base
	m.options.ResourceType = conn.Ref.Type
	m.options.Name = conn.Ref.Name
	m.options.AwsRegion = conn.Ref.Region
	m.recentAccount = conn.Ref.Account
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
	account := m.recentAccount
	lookup := func(ctx context.Context) tea.Cmd {
		return func() tea.Msg {
			target, err := m.runner.FindResource(ctx, &opts, account)
			return jumphostLookupMsg{target: target, err: err}
		}
	}
	load := m.await(lookup, false)
	return m, tea.Batch(load, m.loadingUI.start(i18n.Format("ui.wizard.checking", opts.Name), []string{
		i18n.T("ui.jumphost_flow_start.making_sure_it_is"),
		i18n.T("ui.jumphost_flow_start.knocking_on_target_door"),
	}))
}

// handleLookup continues to the review of a found target; a lookup error returns to the start
// screen, except a profile picked for the entry that reaches another account: another one is asked.
func (m jumphostWizardModel) handleLookup(msg jumphostLookupMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	m.loadingUI.stop()
	if mismatch, ok := errors.AsType[*jumphost.AccountMismatchError](msg.err); ok && m.step == jumphostStepProfile {
		m.errorMsg = i18n.Localize(mismatch)
		return m, nil
	}
	if msg.err != nil {
		start, cmd := m.returnToStart()
		start.errorMsg = i18n.Localize(msg.err)
		return start, cmd
	}
	m.targetIAMAuth = m.options.ResourceType == "rds" && msg.target.IAMAuth
	m.targetEngine = msg.target.Engine
	m.targetCacheAuth = msg.target.CacheAuth
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
		m.loading = false
		m.loadingUI.stop()
		m.errorMsg = i18n.Localize(msg.err)
		return m, nil
	}
	m.errorMsg = ""
	return m, m.loadSavedCmd()
}
