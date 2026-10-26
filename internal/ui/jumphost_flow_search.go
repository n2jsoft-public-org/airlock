package ui

import (
	"context"
	"regexp"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
)

// searchTypeOrder is the order failed types are reported in.
var searchTypeOrder = []string{"rds", "eks", "elasticache", "dynamodb", "s3", "logs"}

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
	// A type picked earlier (the Type step is left with esc) must not leak into the search.
	m.options.ResourceType = ""
	m.errorMsg = ""
	if m.askProfile {
		return m.enterProfileStep()
	}
	return m.discoverWhenRegionKnown()
}

// startSearch lists the targets of every type in the current profile and region.
func (m jumphostWizardModel) startSearch() (tea.Model, tea.Cmd) {
	if m.currentRegion() == "" && m.regionResolved {
		m.errorMsg = i18n.T("ui.jumphost_flow.no_aws_region_set")
		m.regionUnblocks = true
		return m.openRegionPicker()
	}
	m.errorMsg = ""
	m.loading = true
	label := i18n.T("ui.jumphost_flow_search.searching_every_resource_type")
	if region := m.currentRegion(); region != "" {
		label = i18n.Format("ui.jumphost_flow_search.searching_rds_eks_and", region)
	}
	opts := m.options
	search := func(ctx context.Context) tea.Cmd {
		return func() tea.Msg {
			targets, failures, err := m.runner.SearchResources(ctx, &opts)
			return jumphostSearchLoadedMsg{targets: targets, failures: failures, err: err}
		}
	}
	load := m.await(search, true)
	return m, tea.Batch(load, m.loadingUI.start(label, []string{
		i18n.T("ui.jumphost_flow.opening_cloud_map"),
		i18n.T("ui.jumphost_flow_search.asking_every_service_at"),
		i18n.T("ui.jumphost_flow.querying_aws_with_dramatic"),
	}))
}

// handleSearchLoaded lists the found targets with their type; types that failed only warn,
// unless nothing could be found at all.
func (m jumphostWizardModel) handleSearchLoaded(msg jumphostSearchLoadedMsg) (tea.Model, tea.Cmd) {
	m.loading = false
	m.loadingUI.stop()
	switch {
	case msg.err != nil:
		return m.failDiscovery(i18n.Localize(msg.err), true)
	case len(msg.targets) == 0 && len(msg.failures) > 0:
		return m.failDiscovery(searchFailureSummary(msg.failures), true)
	case len(msg.targets) == 0:
		return m.failDiscovery(i18n.Format("ui.wizard.no_targets_in", m.currentRegion()), true)
	}

	m.searchWarnings = searchFailureSummary(msg.failures)
	items := make([]list.Item, 0, len(msg.targets))
	for _, target := range msg.targets {
		items = append(items, jumphostListItem{name: target.Name, desc: target.Description, iamAuth: target.IAMAuth, engine: target.Engine, cacheAuth: target.CacheAuth, resourceType: target.Type})
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
			reason := i18n.Localize(err)
			if match := awsErrorCode.FindStringSubmatch(err.Error()); match != nil {
				reason = match[1]
			}
			parts = append(parts, strings.ToUpper(resourceType)+": "+reason)
		}
	}
	return strings.Join(parts, " · ")
}
