package ui

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

type step int

const (
	stepSelect step = iota
	stepProfile
)

type commandItem struct {
	name string
	desc string
}

func (i commandItem) FilterValue() string {
	if i.desc == "" {
		return i.name
	}
	return i.name + " " + i.desc
}
func (i commandItem) Title() string       { return i.name }
func (i commandItem) Description() string { return i.desc }

type model struct {
	step            step
	loading         bool
	loadingUI       loadingState
	errorMsg        string
	list            list.Model
	profileList     list.Model
	styles          Styles
	selectedCmd     string
	selectedProfile string
	width           int
	height          int
}

type profileSummary struct {
	name string
	desc string
}

type profileLoadedMsg struct {
	profiles []profileSummary
	err      error
}

func newModel() model {
	styles := NewStyles()
	items := []list.Item{
		commandItem{name: "jumphost", desc: "Guided EKS/RDS SSM wizard"},
		commandItem{name: "update-translate", desc: "Download latest i18n bundle"},
	}

	commandList := newWizardList(styles, items)
	commandList.SetFilteringEnabled(false)
	commandList.SetShowTitle(false)

	defaultProfile := strings.TrimSpace(os.Getenv("AWS_PROFILE"))
	if defaultProfile == "" {
		defaultProfile = "default"
	}

	return model{
		step:            stepSelect,
		list:            commandList,
		profileList:     newWizardList(styles, nil),
		styles:          styles,
		selectedProfile: defaultProfile,
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		bodyWidth, bodyHeight := wizardBodySize(msg.Width, msg.Height)
		m.list.SetSize(bodyWidth, bodyHeight)
		m.profileList.SetSize(bodyWidth, bodyHeight)
		return m, nil
	case loadingTickMsg:
		if !m.loading {
			return m, nil
		}
		return m, m.loadingUI.update(msg)
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m.quit()
		case "q":
			// While filtering profiles, "q" is part of the typed filter.
			if m.step != stepProfile || !m.profileList.SettingFilter() {
				return m.quit()
			}
		}
		if m.loading {
			return m, nil
		}
	case profileLoadedMsg:
		m.loading = false
		m.loadingUI.stop()
		if msg.err != nil {
			m.errorMsg = msg.err.Error()
			return m, nil
		}

		items := make([]list.Item, 0, len(msg.profiles))
		selectedIndex := 0
		for idx, profile := range msg.profiles {
			if profile.name == m.selectedProfile {
				selectedIndex = idx
			}
			items = append(items, commandItem{name: profile.name, desc: profile.desc})
		}
		m.profileList.SetItems(items)
		m.profileList.Select(selectedIndex)
		m.errorMsg = ""
		return m, nil
	}

	switch m.step {
	case stepSelect:
		return m.updateSelectStep(msg)
	case stepProfile:
		return m.updateProfileStep(msg)
	default:
		return m, nil
	}
}

func (m model) quit() (tea.Model, tea.Cmd) {
	m.selectedCmd = ""
	m.selectedProfile = ""
	return m, tea.Quit
}

func (m model) updateSelectStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "enter" {
		item, ok := m.list.SelectedItem().(commandItem)
		if !ok {
			return m, nil
		}

		m.selectedCmd = item.name
		if item.name == "update-translate" {
			m.selectedProfile = ""
			return m, tea.Quit
		}

		m.step = stepProfile
		m.loading = true
		m.errorMsg = ""
		return m, tea.Batch(
			loadProfilesCmd(),
			m.loadingUI.start("Loading AWS profiles...", []string{
				"Negotiating with the cloud gremlins...",
				"Asking AWS nicely for profile names...",
				"Counting regions while coffee brews...",
				"Polishing terminal pixels for you...",
			}),
		)
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// updateProfileStep ends the picker once a profile is chosen; the region is switched later inside the wizard.
func (m model) updateProfileStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			if m.profileList.FilterState() != list.Unfiltered {
				break
			}
			m.step = stepSelect
			return m, nil
		case "enter":
			selected, ok := m.profileList.SelectedItem().(commandItem)
			if !ok {
				return m, nil
			}

			profile := strings.TrimSpace(selected.name)
			if profile == "" {
				profile = "default"
			}
			m.selectedProfile = profile
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.profileList, cmd = m.profileList.Update(msg)
	return m, cmd
}

func (m model) View() string {
	frame := wizardFrame{title: "acme", tagline: "CLI helper", errorMsg: m.errorMsg}

	switch {
	case m.loading:
		frame.heading = "Working on it"
		frame.body = m.loadingUI.Body(m.styles)
		frame.keys = [][2]string{{"q", "quit"}}
	case m.step == stepSelect:
		frame.heading = "What do you want to do?"
		frame.body = m.list.View()
		frame.keys = [][2]string{{"↑↓", "move"}, {"enter", "select"}, {"q", "quit"}}
	default:
		frame.chips = []string{m.styles.contextChip("command", m.selectedCmd)}
		frame.heading = "Choose an AWS profile"
		frame.hint = plural(len(m.profileList.Items()), "profile")
		frame.body = m.profileList.View()
		frame.keys = [][2]string{{"↑↓", "move"}, {"/", "filter"}, {"enter", "continue"}, {"esc", "back"}, {"q", "quit"}}
		if m.profileList.SettingFilter() {
			frame.keys = [][2]string{{"enter", "continue"}, {"esc", "clear filter"}}
		}
	}

	return m.styles.renderWizardFrame(frame, m.width, m.height)
}

func loadProfilesCmd() tea.Cmd {
	return func() tea.Msg {
		profiles, err := listAWSProfileSummaries()
		return profileLoadedMsg{profiles: profiles, err: err}
	}
}

func listAWSProfileSummaries() ([]profileSummary, error) {
	if _, err := exec.LookPath("aws"); err != nil {
		return []profileSummary{{name: "default", desc: "region n/a"}}, nil
	}

	cmd := exec.Command("aws", "configure", "list-profiles")
	raw, err := cmd.Output()
	if err != nil {
		return []profileSummary{{name: "default", desc: "region n/a"}}, nil
	}

	set := map[string]struct{}{}
	for _, line := range strings.Split(string(raw), "\n") {
		profile := strings.TrimSpace(line)
		if profile == "" {
			continue
		}
		set[profile] = struct{}{}
	}

	if len(set) == 0 {
		set["default"] = struct{}{}
	}

	profiles := make([]string, 0, len(set))
	for profile := range set {
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)

	summaries := make([]profileSummary, 0, len(profiles))
	for _, profile := range profiles {
		description := "region n/a"
		regionCmd := exec.Command("aws", "configure", "get", "region", "--profile", profile)
		regionRaw, regionErr := regionCmd.Output()
		if regionErr == nil {
			region := strings.TrimSpace(string(regionRaw))
			if region != "" {
				description = fmt.Sprintf("region %s", region)
			}
		}

		summaries = append(summaries, profileSummary{name: profile, desc: description})
	}

	return summaries, nil
}
