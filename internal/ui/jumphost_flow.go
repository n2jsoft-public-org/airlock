package ui

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/example/acme-cli/internal/jumphost"
)

type JumphostDiscovery interface {
	ListAWSProfileSummaries(ctx context.Context) ([]jumphost.AWSProfileSummary, error)
	ListResourceSummaries(ctx context.Context, options jumphost.Options) ([]jumphost.ResourceSummary, error)
	ResolveDefaultLocalPort(ctx context.Context, options jumphost.Options) (int, error)
}

type jumphostStep int

const (
	jumphostStepProfile jumphostStep = iota
	jumphostStepType
	jumphostStepTarget
	jumphostStepPort
)

type jumphostWizardModel struct {
	ctx        context.Context
	runner     JumphostDiscovery
	styles     Styles
	step       jumphostStep
	askProfile bool
	errorMsg   string
	loading    bool
	loadingUI  loadingState
	canceled   bool

	options      jumphost.Options
	profileList  list.Model
	resourceList list.Model
	targetList   list.Model
	portInput    textinput.Model
}

type jumphostListItem struct {
	name string
	desc string
}

func (i jumphostListItem) FilterValue() string {
	if i.desc == "" {
		return i.name
	}
	return i.name + " " + i.desc
}
func (i jumphostListItem) Title() string       { return i.name }
func (i jumphostListItem) Description() string { return i.desc }

type jumphostTargetsLoadedMsg struct {
	targets []jumphost.ResourceSummary
	err     error
}

type jumphostProfilesLoadedMsg struct {
	profiles []jumphost.AWSProfileSummary
	err      error
}

type jumphostDefaultPortLoadedMsg struct {
	port int
	err  error
}

func RunJumphostWizard(ctx context.Context, runner JumphostDiscovery, base jumphost.Options) (jumphost.Options, bool, error) {
	model := newJumphostWizardModel(ctx, runner, base)
	program := tea.NewProgram(model, tea.WithAltScreen())

	finalModel, err := program.Run()
	if err != nil {
		return jumphost.Options{}, false, err
	}

	result, ok := finalModel.(jumphostWizardModel)
	if !ok {
		return jumphost.Options{}, true, nil
	}

	return result.options, result.canceled, nil
}

func newJumphostWizardModel(ctx context.Context, runner JumphostDiscovery, base jumphost.Options) jumphostWizardModel {
	askProfile := strings.TrimSpace(base.AwsProfile) == ""
	initialStep := jumphostStepProfile
	loading := true
	if !askProfile {
		initialStep = jumphostStepType
		loading = false
	}

	if strings.TrimSpace(base.AwsProfile) == "" {
		base.AwsProfile = strings.TrimSpace(os.Getenv("AWS_PROFILE"))
	}
	if strings.TrimSpace(base.AwsProfile) == "" {
		base.AwsProfile = "default"
	}

	resourceItems := []list.Item{
		jumphostListItem{name: "rds", desc: "Relational Database Service"},
		jumphostListItem{name: "eks", desc: "Elastic Kubernetes Service"},
		jumphostListItem{name: "elasticache", desc: "ElastiCache (Redis / Valkey)"},
	}

	profileList := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	profileList.Title = "Select AWS profile"
	profileList.SetShowStatusBar(false)
	profileList.SetFilteringEnabled(true)
	profileList.SetShowHelp(false)

	resourceList := list.New(resourceItems, list.NewDefaultDelegate(), 0, 0)
	resourceList.Title = "Select resource type"
	resourceList.SetShowStatusBar(false)
	resourceList.SetFilteringEnabled(false)
	resourceList.SetShowHelp(false)

	targetList := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	targetList.Title = "Select target"
	targetList.SetShowStatusBar(false)
	targetList.SetFilteringEnabled(true)
	targetList.SetShowHelp(false)

	portInput := textinput.New()
	portInput.Placeholder = "Local port"
	portInput.CharLimit = 6

	return jumphostWizardModel{
		ctx:          ctx,
		runner:       runner,
		styles:       NewStyles(),
		step:         initialStep,
		askProfile:   askProfile,
		loading:      loading,
		options:      base,
		profileList:  profileList,
		resourceList: resourceList,
		targetList:   targetList,
		portInput:    portInput,
	}
}

func (m jumphostWizardModel) Init() tea.Cmd {
	if !m.askProfile {
		return nil
	}

	return tea.Batch(
		m.loadProfilesCmd(),
		m.loadingUI.start("Loading AWS profiles...", []string{
			"Calibrating jump pads...",
			"Checking which profile has the best vibes...",
			"Talking to IAM in polite terminal language...",
			"Summoning your AWS identities...",
		}),
	)
}

func (m jumphostWizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.profileList.SetSize(msg.Width-4, msg.Height-10)
		m.resourceList.SetSize(msg.Width-4, msg.Height-10)
		m.targetList.SetSize(msg.Width-4, msg.Height-10)
		return m, nil
	case loadingTickMsg:
		if !m.loading {
			return m, nil
		}
		return m, m.loadingUI.update(msg)
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.canceled = true
			return m, tea.Quit
		}
	case jumphostProfilesLoadedMsg:
		m.loading = false
		m.loadingUI.stop()
		if msg.err != nil {
			m.errorMsg = msg.err.Error()
			m.step = jumphostStepProfile
			return m, nil
		}
		if len(msg.profiles) == 0 {
			m.errorMsg = "No AWS profile found"
			m.step = jumphostStepProfile
			return m, nil
		}

		items := make([]list.Item, 0, len(msg.profiles))
		selectedIndex := 0
		currentProfile := strings.TrimSpace(m.options.AwsProfile)
		for idx, profile := range msg.profiles {
			if currentProfile != "" && profile.Name == currentProfile {
				selectedIndex = idx
			}
			items = append(items, jumphostListItem{name: profile.Name, desc: profile.Description})
		}
		m.profileList.SetItems(items)
		m.profileList.Select(selectedIndex)
		m.step = jumphostStepProfile
		return m, nil
	case jumphostTargetsLoadedMsg:
		m.loading = false
		m.loadingUI.stop()
		if msg.err != nil {
			m.errorMsg = msg.err.Error()
			m.step = jumphostStepType
			return m, nil
		}
		if len(msg.targets) == 0 {
			m.errorMsg = "No targets found"
			m.step = jumphostStepType
			return m, nil
		}

		items := make([]list.Item, 0, len(msg.targets))
		for _, target := range msg.targets {
			items = append(items, jumphostListItem{name: target.Name, desc: target.Description})
		}
		m.targetList.SetItems(items)
		m.targetList.Title = fmt.Sprintf("Select %s target", strings.ToUpper(m.options.ResourceType))
		m.step = jumphostStepTarget
		return m, nil
	case jumphostDefaultPortLoadedMsg:
		m.loading = false
		m.loadingUI.stop()
		if msg.err != nil {
			m.errorMsg = msg.err.Error()
			m.step = jumphostStepTarget
			return m, nil
		}

		m.portInput.SetValue(strconv.Itoa(msg.port))
		m.portInput.CursorEnd()
		m.portInput.Focus()
		m.step = jumphostStepPort
		return m, nil
	}

	switch m.step {
	case jumphostStepProfile:
		return m.updateProfileStep(msg)
	case jumphostStepType:
		return m.updateTypeStep(msg)
	case jumphostStepTarget:
		return m.updateTargetStep(msg)
	case jumphostStepPort:
		return m.updatePortStep(msg)
	default:
		return m, nil
	}
}

func (m jumphostWizardModel) updateProfileStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "enter" {
		selected, ok := m.profileList.SelectedItem().(jumphostListItem)
		if !ok {
			return m, nil
		}

		m.options.AwsProfile = selected.name
		m.step = jumphostStepType
		m.errorMsg = ""
		return m, nil
	}

	var cmd tea.Cmd
	m.profileList, cmd = m.profileList.Update(msg)
	return m, cmd
}

func (m jumphostWizardModel) updateTypeStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" {
		if !m.askProfile {
			return m, nil
		}

		m.step = jumphostStepProfile
		m.errorMsg = ""
		return m, nil
	}

	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "enter" {
		selected, ok := m.resourceList.SelectedItem().(jumphostListItem)
		if !ok {
			return m, nil
		}

		m.errorMsg = ""
		m.loading = true
		m.options.ResourceType = selected.name
		return m, tea.Batch(
			m.loadTargetsCmd(),
			m.loadingUI.start("Discovering AWS resources...", []string{
				"Opening the cloud map...",
				"Looking for targets that answer the ping...",
				"Querying AWS with dramatic suspense...",
				"Trying not to wake sleepy EC2 neighbors...",
			}),
		)
	}

	var cmd tea.Cmd
	m.resourceList, cmd = m.resourceList.Update(msg)
	return m, cmd
}

func (m jumphostWizardModel) updateTargetStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			m.step = jumphostStepType
			m.errorMsg = ""
			return m, nil
		case "enter":
			selected, ok := m.targetList.SelectedItem().(jumphostListItem)
			if !ok {
				return m, nil
			}

			m.options.Name = selected.name
			m.loading = true
			m.errorMsg = ""
			return m, tea.Batch(
				m.loadDefaultPortCmd(),
				m.loadingUI.start("Finding the best local port...", []string{
					"Looking for a chill port number...",
					"Avoiding port drama with your local apps...",
					"Knocking on localhost doors...",
					"Port scouting mission in progress...",
				}),
			)
		}
	}

	var cmd tea.Cmd
	m.targetList, cmd = m.targetList.Update(msg)
	return m, cmd
}

func (m jumphostWizardModel) updatePortStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			m.portInput.Blur()
			m.step = jumphostStepTarget
			m.errorMsg = ""
			return m, nil
		case "enter":
			value := strings.TrimSpace(m.portInput.Value())
			port, err := strconv.Atoi(value)
			if err != nil || port <= 0 {
				m.errorMsg = "Port must be a positive number"
				return m, nil
			}

			m.options.LocalPortNumber = port
			m.portInput.Blur()
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.portInput, cmd = m.portInput.Update(msg)
	return m, cmd
}

func (m jumphostWizardModel) loadTargetsCmd() tea.Cmd {
	opts := m.options
	return func() tea.Msg {
		targets, err := m.runner.ListResourceSummaries(m.ctx, opts)
		return jumphostTargetsLoadedMsg{targets: targets, err: err}
	}
}

func (m jumphostWizardModel) loadDefaultPortCmd() tea.Cmd {
	opts := m.options
	return func() tea.Msg {
		port, err := m.runner.ResolveDefaultLocalPort(m.ctx, opts)
		return jumphostDefaultPortLoadedMsg{port: port, err: err}
	}
}

func (m jumphostWizardModel) View() string {
	header := m.styles.Title.Render("acme jumphost")
	if m.loading {
		return m.loadingUI.View(m.styles, "acme jumphost")
	}

	errorLine := ""
	if m.errorMsg != "" {
		errorLine = "\n" + m.styles.Subtitle.Render(m.errorMsg) + "\n"
	}

	switch m.step {
	case jumphostStepProfile:
		return m.styles.Container.Render(
			header + "\n" +
				m.styles.Subtitle.Render(fmt.Sprintf("Current profile: %s", m.options.AwsProfile)) + "\n" +
				errorLine +
				m.profileList.View() + "\n\n" +
				m.styles.Muted.Render("Enter: select   q: quit"),
		)
	case jumphostStepType:
		help := "Enter: select   esc: back   q: quit"
		if !m.askProfile {
			help = "Enter: select   q: quit"
		}

		return m.styles.Container.Render(
			header + "\n" +
				m.styles.Subtitle.Render(fmt.Sprintf("Profile: %s", m.options.AwsProfile)) + "\n" +
				errorLine +
				m.resourceList.View() + "\n\n" +
				m.styles.Muted.Render(help),
		)
	case jumphostStepTarget:
		return m.styles.Container.Render(
			header + "\n" +
				m.styles.Subtitle.Render(fmt.Sprintf("Type: %s", strings.ToUpper(m.options.ResourceType))) + "\n" +
				errorLine +
				m.targetList.View() + "\n\n" +
				m.styles.Muted.Render("Enter: select   esc: back   q: quit"),
		)
	case jumphostStepPort:
		return m.styles.Container.Render(
			header + "\n" +
				m.styles.Subtitle.Render(fmt.Sprintf("Target: %s", m.options.Name)) + "\n" +
				errorLine +
				m.portInput.View() + "\n\n" +
				m.styles.Muted.Render("Enter: continue   esc: back   q: quit"),
		)
	default:
		return m.styles.Container.Render(header)
	}
}

func (m jumphostWizardModel) loadProfilesCmd() tea.Cmd {
	if !m.askProfile {
		return nil
	}

	return func() tea.Msg {
		profiles, err := m.runner.ListAWSProfileSummaries(m.ctx)
		return jumphostProfilesLoadedMsg{profiles: profiles, err: err}
	}
}
