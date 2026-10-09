package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// jumphostFlow is the path through the wizard chosen on the start screen.
type jumphostFlow int

const (
	// flowStart shows the start screen: recent connections, new connection, search.
	flowStart jumphostFlow = iota
	// flowNew picks a type, a profile, then a target.
	flowNew
	// flowSearch picks a profile, then a target among every type.
	flowSearch
	// flowRecent reconnects to a remembered target.
	flowRecent
)

// flowSteps lists the steps of the current flow in order. The Profile step is skipped when
// --profile gave the profile.
func (m jumphostWizardModel) flowSteps() []jumphostStep {
	switch m.flow {
	case flowNew:
		steps := []jumphostStep{jumphostStepType}
		// A direct or Docker database has no AWS profile.
		if m.askProfile && m.options.ResourceType != sessions.DirectResourceType && m.options.ResourceType != sessions.DockerResourceType {
			steps = append(steps, jumphostStepProfile)
		}
		return append(steps, jumphostStepTarget, jumphostStepReview)
	case flowSearch:
		var steps []jumphostStep
		if m.askProfile {
			steps = append(steps, jumphostStepProfile)
		}
		return append(steps, jumphostStepTarget, jumphostStepReview)
	case flowRecent:
		if m.recentNeedsProfile {
			return []jumphostStep{jumphostStepProfile, jumphostStepReview}
		}
		return []jumphostStep{jumphostStepReview}
	default:
		return []jumphostStep{jumphostStepStart}
	}
}

// previousStep returns the step before step in the flow; from the first step it is the start
// screen when there is a history. ok is false when there is nowhere to go back to.
func (m jumphostWizardModel) previousStep(step jumphostStep) (jumphostStep, bool) {
	steps := m.flowSteps()
	for idx, candidate := range steps {
		if candidate == step && idx > 0 {
			return steps[idx-1], true
		}
	}
	if m.hasHistory && m.flow != flowStart {
		return jumphostStepStart, true
	}
	// Without history, Search is started from the Type step of a new connection.
	if m.flow == flowSearch {
		return jumphostStepType, true
	}
	return step, false
}

// goBack moves to the previous step of the flow; from its first step, an embedded wizard returns
// to the sessions of its dashboard.
func (m jumphostWizardModel) goBack() (tea.Model, tea.Cmd) {
	prev, ok := m.previousStep(m.step)
	if !ok {
		if m.leavesToHost() {
			m.canceled = true
			return m, m.finish()
		}
		return m, nil
	}
	if prev == jumphostStepStart {
		return m.returnToStart()
	}
	if prev == jumphostStepType {
		m.flow = flowNew
	}
	m.step = prev
	m.errorMsg = ""
	return m, nil
}

// leavesToHost reports whether going back from the first step returns to the dashboard's sessions.
func (m jumphostWizardModel) leavesToHost() bool {
	return m.embedded && m.hostHasSessions
}

// returnToStart shows the start screen again, with the options the wizard was opened with.
func (m jumphostWizardModel) returnToStart() (jumphostWizardModel, tea.Cmd) {
	m = m.blurReviewInputs()
	m.flow = flowStart
	m.step = jumphostStepStart
	m.options = m.base
	m.recentNeedsProfile = false
	m.recentAccount = ""
	m.awaitingRegion = false
	m.searchWarnings = ""
	m.errorMsg = ""
	m.defaultRegion = ""
	m.regionResolved = false
	return m, m.resolveRegionCmd(m.ctx)
}

// failDiscovery reports errMsg on the step the loading started from. leaveTargets empties a
// target list that failed to reload and steps back from it, so targets of another region are
// never offered.
func (m jumphostWizardModel) failDiscovery(errMsg string, leaveTargets bool) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if leaveTargets && m.step == jumphostStepTarget {
		m.targetList.ResetFilter()
		m.targetList.SetItems(nil)
		if prev, ok := m.previousStep(jumphostStepTarget); ok {
			if prev == jumphostStepStart {
				m, cmd = m.returnToStart()
			} else {
				m.step = prev
			}
		}
	}
	// A flow started from the start screen that fails there is over.
	if m.step == jumphostStepStart && m.flow != flowStart {
		m, cmd = m.returnToStart()
	}
	// So is a search started from the Type step: the Type step belongs to a new connection.
	if m.step == jumphostStepType {
		m.flow = flowNew
	}
	m.searchWarnings = ""
	m.errorMsg = errMsg
	return m, cmd
}

// discoverWhenRegionKnown starts the flow's discovery, first waiting for the default region of
// the profile when none was chosen, so a profile without region opens the region picker.
func (m jumphostWizardModel) discoverWhenRegionKnown() (tea.Model, tea.Cmd) {
	if m.options.AwsRegion != "" || m.regionResolved {
		return m.startDiscovery()
	}
	m.awaitingRegion = true
	m.loading = true
	resolve := m.await(m.resolveRegionCmd, false)
	return m, tea.Batch(resolve, m.loadingUI.start(i18n.T("ui.jumphost_flow_nav.resolving_aws_region"), []string{
		i18n.T("ui.jumphost_flow_nav.asking_profile_where_it"),
	}))
}

// startDiscovery loads what the flow shows next once the profile and region are known.
func (m jumphostWizardModel) startDiscovery() (tea.Model, tea.Cmd) {
	switch m.flow {
	case flowSearch:
		return m.startSearch()
	case flowRecent:
		return m.startLookup()
	default:
		return m.startLoadingTargets()
	}
}

// enterProfileStep shows the profile list, loading it the first time.
func (m jumphostWizardModel) enterProfileStep() (tea.Model, tea.Cmd) {
	m.step = jumphostStepProfile
	m.errorMsg = ""
	if m.profilesLoaded {
		return m, nil
	}
	m.loading = true
	load := m.await(m.loadProfilesCmd, false)
	return m, tea.Batch(load, m.loadingUI.start(i18n.T("ui.jumphost_flow_nav.loading_aws_profiles"), []string{
		i18n.T("ui.jumphost_flow_nav.calibrating_jump_pads"),
		i18n.T("ui.jumphost_flow_nav.checking_which_profile_has"),
		i18n.T("ui.jumphost_flow_nav.talking_to_iam_in"),
		i18n.T("ui.jumphost_flow_nav.summoning_your_aws_identities"),
	}))
}
