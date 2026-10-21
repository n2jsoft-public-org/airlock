package ui

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft/airlock/internal/dockerdb"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/sessions"
)

type dockerListOutcome int

const (
	dockerListOpen dockerListOutcome = iota
	// dockerListPicked opens the review of the selected container.
	dockerListPicked
	// dockerListSwitch shows the running session of a connected container.
	dockerListSwitch
	// dockerListReload lists the containers again.
	dockerListReload
	// dockerListBack leaves the list (esc without any filter to clear).
	dockerListBack
)

// dockerItem is one database container of the list.
type dockerItem struct {
	c         dockerdb.Container
	connected bool
}

func (i dockerItem) Title() string {
	if i.connected {
		return i.c.Name + i18n.T("ui.list.connected")
	}
	return i.c.Name
}

func (i dockerItem) Description() string { return i.c.Description() }

func (i dockerItem) FilterValue() string {
	return strings.Join([]string{i.c.Name, string(i.c.Engine), i.c.Image}, " ")
}

// dockerList is the target step of a Docker database: the running database and cache containers, or why
// docker could not list them. loaded is false until the first listing arrives.
type dockerList struct {
	err    error
	list   list.Model
	loaded bool
}

// newDockerList lists containers; active holds the connectionKey of the running sessions.
func newDockerList(styles Styles, containers []dockerdb.Container, err error, active map[string]bool, loaded bool) dockerList {
	items := make([]list.Item, 0, len(containers))
	for idx := range containers {
		c := &containers[idx]
		items = append(items, dockerItem{c: *c, connected: active[dockerConnectionKey(c)]})
	}
	return dockerList{list: newWizardList(styles, items), err: err, loaded: loaded}
}

func (p *dockerList) SetSize(width, height int) { p.list.SetSize(width, max(height, 3)) }

func (p dockerList) typing() bool { return p.list.SettingFilter() }

func (p dockerList) count() int { return len(p.list.Items()) }

func (p dockerList) selectedItem() (dockerItem, bool) {
	item, ok := p.list.SelectedItem().(dockerItem)
	return item, ok
}

// selectName selects the container name, when listed.
func (p *dockerList) selectName(name string) {
	for idx, item := range p.list.Items() {
		if entry, ok := item.(dockerItem); ok && entry.c.Name == name {
			p.list.Select(idx)
			return
		}
	}
}

// Update handles one message and reports what the user chose, if anything.
func (p dockerList) Update(msg tea.Msg) (dockerList, dockerListOutcome, tea.Cmd) {
	if key, isKey := msg.(tea.KeyPressMsg); isKey && !p.list.SettingFilter() {
		switch key.String() {
		case "r":
			return p, dockerListReload, nil
		case "esc":
			if !p.list.IsFiltered() {
				return p, dockerListBack, nil
			}
		case "enter":
			item, ok := p.selectedItem()
			switch {
			case !ok:
				return p, dockerListOpen, nil
			case item.connected:
				return p, dockerListSwitch, nil
			default:
				return p, dockerListPicked, nil
			}
		}
	}
	if p.err != nil {
		return p, dockerListOpen, nil
	}
	var cmd tea.Cmd
	p.list, cmd = p.list.Update(msg)
	return p, dockerListOpen, cmd
}

// View renders the loading, the docker error, the empty state, or the list.
func (p dockerList) View(styles Styles, width int) string {
	switch {
	case !p.loaded:
		return styles.Muted.Render(truncate(i18n.T("ui.docker_list.looking_for_database_and"), width))
	case p.err != nil:
		return styles.Danger.Width(width).Render(i18n.Localize(p.err))
	case p.count() == 0:
		return styles.Muted.Render(truncate(i18n.T("ui.docker_list.no_running_database_or"), width))
	}
	return p.list.View()
}

// dockerListedMsg carries a listing of the database and cache containers.
type dockerListedMsg struct {
	err        error
	containers []dockerdb.Container
}

// enterDockerList shows the Docker list, filled in the background: docker can take seconds.
func (m jumphostWizardModel) enterDockerList() (tea.Model, tea.Cmd) {
	m.options.Name = ""
	m.dockerList = newDockerList(m.styles, nil, nil, m.active, false)
	m = m.fitLists()
	m.step = jumphostStepTarget
	return m, m.listDockerCmd()
}

// listDockerCmd lists the database and cache containers, but those of a disabled module.
func (m jumphostWizardModel) listDockerCmd() tea.Cmd {
	source, ctx, runner := m.docker, m.ctx, m.runner
	return func() tea.Msg {
		containers, err := source.List(ctx)
		containers = slices.DeleteFunc(containers, func(c dockerdb.Container) bool {
			if c.Cache() {
				return runner.ModuleDisabled(sessions.DockerCacheResourceType)
			}
			return runner.ModuleDisabled(sessions.DockerResourceType)
		})
		return dockerListedMsg{containers: containers, err: err}
	}
}

// handleDockerListed shows a listing, keeping the selected container selected.
func (m jumphostWizardModel) handleDockerListed(msg dockerListedMsg) (tea.Model, tea.Cmd) {
	selected := ""
	if item, ok := m.dockerList.selectedItem(); ok {
		selected = item.c.Name
	}
	m.dockerList = newDockerList(m.styles, msg.containers, msg.err, m.active, true)
	m = m.fitLists()
	m.dockerList.selectName(selected)
	if m.openDocker != "" {
		if m.step == jumphostStepTarget && m.isDocker() {
			return m.openPendingDocker(msg.err)
		}
		m.openDocker, m.openDockerError = "", "" // a late listing: the user already left.
	}
	return m, nil
}

// updateDockerListStep handles the Docker list.
func (m jumphostWizardModel) updateDockerListStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	picker, outcome, cmd := m.dockerList.Update(msg)
	m.dockerList = picker
	item, _ := picker.selectedItem()
	switch outcome {
	case dockerListPicked:
		if item.c.Problem != "" {
			m.errorMsg = item.c.Problem
			return m, nil
		}
		return m.enterDockerReview(item.c)
	case dockerListSwitch:
		m.canceled = true
		m.switchTo = dockerConnectionKey(&item.c)
		return m, m.finish()
	case dockerListReload:
		return m, m.listDockerCmd()
	case dockerListBack:
		return m.goBack()
	default:
		return m, cmd
	}
}

// dockerListKeys are the keys of the Docker list.
func (m jumphostWizardModel) dockerListKeys(help, quit [2]string) [][2]string {
	if m.dockerList.typing() {
		return [][2]string{{"enter", i18n.T("ui.hint.select")}, {"esc", i18n.T("ui.hint.clear_filter")}}
	}
	keys := [][2]string{{"↑↓", i18n.T("ui.hint.move")}, {"/", i18n.T("ui.hint.filter")}}
	if item, ok := m.dockerList.selectedItem(); ok {
		enter := i18n.T("ui.hint.select")
		if item.connected {
			enter = i18n.T("ui.hint.switch_to")
		}
		keys = append(keys, [2]string{"enter", enter})
	}
	return append(keys, [2]string{"r", i18n.T("ui.hint.reload")}, [2]string{"esc", i18n.T("ui.hint.back")}, help, quit)
}

// dockerCard is the Type step card of Docker databases, offered when docker is installed.
var dockerCard = resourceTypeOption{
	name:     sessions.DockerResourceType,
	titleKey: "ui.card.docker.title",
	descKey:  "ui.card.docker.desc",
}
