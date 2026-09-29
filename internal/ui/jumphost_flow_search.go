package ui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// searchTypeOrder is the order failed types are reported in.
var searchTypeOrder = []string{"rds", "eks", "elasticache"}

// jumphostSearchLoadedMsg carries the targets of every type found by a search.
type jumphostSearchLoadedMsg struct {
	err error
	// failures holds the types whose listing failed, keyed by type.
	failures map[string]error
	targets  []jumphost.ResourceSummary
}

// beginSearch starts the Search flow: the profile first, unless --profile gave it.
func (m jumphostWizardModel) beginSearch() (tea.Model, tea.Cmd) {
	m.flow = flowSearch
	m.errorMsg = ""
	if m.askProfile {
		return m.enterProfileStep()
	}
	return m.discoverWhenRegionKnown()
}

// startSearch lists the targets of every type in the current profile and region.
func (m jumphostWizardModel) startSearch() (tea.Model, tea.Cmd) {
	if m.currentRegion() == "" && m.regionResolved {
		m.errorMsg = "No AWS region set for this profile: pick one"
		m.regionUnblocks = true
		return m.openRegionPicker()
	}
	m.errorMsg = ""
	m.loading = true
	label := "Searching every resource type..."
	if region := m.currentRegion(); region != "" {
		label = fmt.Sprintf("Searching RDS, EKS and ElastiCache targets in %s...", region)
	}
	opts := m.options
	search := func() tea.Msg {
		targets, failures, err := m.runner.SearchResources(m.ctx, &opts)
		return jumphostSearchLoadedMsg{targets: targets, failures: failures, err: err}
	}
	return m, tea.Batch(search, m.loadingUI.start(label, []string{
		"Opening the cloud map...",
		"Asking every service at once...",
		"Querying AWS with dramatic suspense...",
	}))
}

// handleSearchLoaded lists the found targets with their type; types that failed only warn,
// unless nothing could be found at all.
func (m jumphostWizardModel) handleSearchLoaded(msg jumphostSearchLoadedMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	m.loadingUI.stop()
	switch {
	case msg.err != nil:
		return m.failDiscovery(msg.err.Error(), true)
	case len(msg.targets) == 0 && len(msg.failures) > 0:
		return m.failDiscovery(searchFailureSummary(msg.failures), true)
	case len(msg.targets) == 0:
		return m.failDiscovery("No targets found in "+m.currentRegion(), true)
	}

	m.searchWarnings = searchFailureSummary(msg.failures)
	items := make([]list.Item, 0, len(msg.targets))
	for _, target := range msg.targets {
		desc := target.Type
		if target.Description != "" {
			desc += " · " + target.Description
		}
		items = append(items, jumphostListItem{name: target.Name, desc: desc, iamAuth: target.IAMAuth, resourceType: target.Type})
	}
	m.targetList.ResetFilter()
	m.targetList.SetItems(items)
	m.targetList.Select(0)
	m.step = jumphostStepTarget
	return m, nil
}

// awsErrorCode finds the code of an AWS CLI error, e.g. "An error occurred (AccessDeniedException) when ...".
var awsErrorCode = regexp.MustCompile(`An error occurred \(([^)]+)\)`)

// searchFailureSummary lists the failed types in display order, e.g. "EKS: AccessDeniedException":
// the AWS error code when the error has one, else the whole error.
func searchFailureSummary(failures map[string]error) string {
	parts := make([]string, 0, len(failures))
	for _, resourceType := range searchTypeOrder {
		if err, ok := failures[resourceType]; ok {
			reason := err.Error()
			if match := awsErrorCode.FindStringSubmatch(reason); match != nil {
				reason = match[1]
			}
			parts = append(parts, strings.ToUpper(resourceType)+": "+reason)
		}
	}
	return strings.Join(parts, " · ")
}
