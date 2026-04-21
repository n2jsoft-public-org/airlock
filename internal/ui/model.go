package ui

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type step int

const (
	stepSelect step = iota
	stepProfile
	stepRegion
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
	regionInput     textinput.Model
	styles          Styles
	selectedCmd     string
	selectedProfile string
	selectedRegion  string
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
	items := []list.Item{
		commandItem{name: "jumphost", desc: "Guided EKS/RDS SSM wizard"},
		commandItem{name: "update-translate", desc: "Download latest i18n bundle"},
	}

	commandList := list.New(items, list.NewDefaultDelegate(), 0, 0)
	commandList.Title = "acme commands"
	commandList.SetShowStatusBar(false)
	commandList.SetFilteringEnabled(false)
	commandList.SetShowHelp(false)

	profileList := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	profileList.Title = "Select AWS profile"
	profileList.SetShowStatusBar(false)
	profileList.SetFilteringEnabled(true)
	profileList.SetShowHelp(false)

	regionInput := textinput.New()
	regionInput.Placeholder = "AWS region"
	regionInput.CharLimit = 40

	defaultProfile := strings.TrimSpace(os.Getenv("AWS_PROFILE"))
	if defaultProfile == "" {
		defaultProfile = "default"
	}

	defaultRegion := strings.TrimSpace(os.Getenv("AWS_REGION"))
	if defaultRegion == "" {
		defaultRegion = "eu-west-3"
	}

	regionInput.SetValue(defaultRegion)

	return model{
		step:            stepSelect,
		list:            commandList,
		profileList:     profileList,
		regionInput:     regionInput,
		styles:          NewStyles(),
		selectedProfile: defaultProfile,
		selectedRegion:  defaultRegion,
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width-4, msg.Height-7)
		m.profileList.SetSize(msg.Width-4, msg.Height-7)
		return m, nil
	case loadingTickMsg:
		if !m.loading {
			return m, nil
		}
		return m, m.loadingUI.update(msg)
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.selectedCmd = ""
			m.selectedProfile = ""
			m.selectedRegion = ""
			return m, tea.Quit
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
	case stepRegion:
		return m.updateRegionStep(msg)
	default:
		return m, nil
	}
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
			m.selectedRegion = ""
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

func (m model) updateProfileStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
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
			m.step = stepRegion
			m.regionInput.Focus()
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.profileList, cmd = m.profileList.Update(msg)
	return m, cmd
}

func (m model) updateRegionStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			m.step = stepProfile
			m.regionInput.Blur()
			return m, nil
		case "enter":
			region := strings.TrimSpace(m.regionInput.Value())
			if region == "" {
				region = "eu-west-3"
				m.regionInput.SetValue(region)
			}
			m.selectedRegion = region
			m.regionInput.Blur()
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.regionInput, cmd = m.regionInput.Update(msg)
	return m, cmd
}

func (m model) View() string {
	if m.loading {
		return m.loadingUI.View(m.styles, "acme CLI helper")
	}

	if m.step == stepSelect {
		return m.styles.Container.Render(
			m.styles.Title.Render("acme CLI helper") + "\n" +
				m.styles.Subtitle.Render("Select a command") + "\n\n" +
				m.list.View() + "\n\n" +
				m.styles.Muted.Render("Enter: select   q: quit"),
		)
	}

	if m.step == stepProfile {
		errorLine := ""
		if m.errorMsg != "" {
			errorLine = "\n" + m.styles.Subtitle.Render(m.errorMsg) + "\n"
		}

		content := m.styles.Title.Render("acme CLI helper") + "\n" +
			m.styles.Subtitle.Render(fmt.Sprintf("Command: %s", m.selectedCmd)) + "\n\n" +
			m.styles.Subtitle.Render("AWS profile") +
			errorLine + "\n" +
			m.profileList.View() + "\n\n" +
			m.styles.Muted.Render("Enter: continue   esc: back   q: quit")

		return m.styles.Container.Render(content)
	}

	content := m.styles.Title.Render("acme CLI helper") + "\n" +
		m.styles.Subtitle.Render(fmt.Sprintf("Command: %s", m.selectedCmd)) + "\n\n" +
		m.styles.Subtitle.Render(fmt.Sprintf("AWS profile: %s", m.selectedProfile)) + "\n\n" +
		m.styles.Subtitle.Render("AWS region") + "\n" +
		m.regionInput.View() + "\n\n" +
		m.styles.Muted.Render("Enter: run command   esc: back   q: quit")

	return m.styles.Container.Render(content)
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
