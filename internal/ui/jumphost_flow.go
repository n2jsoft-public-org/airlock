package ui

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

type JumphostDiscovery interface {
	ListAWSProfileSummaries(ctx context.Context) ([]jumphost.AWSProfileSummary, error)
	ListResourceSummaries(ctx context.Context, options *jumphost.Options) ([]jumphost.ResourceSummary, error)
	ResolveDefaultLocalPort(ctx context.Context, options jumphost.Options) (int, error)
	CheckLocalPort(port int) error
	// ResolveRegion returns the region used for profile when none is chosen explicitly.
	ResolveRegion(ctx context.Context, profile string) string
	// ResolveDBUser returns user, else the default database user; "" when none is configured.
	ResolveDBUser(user string) string
	// LoadConnectionPrefs returns the Connect step choices saved for the database of options, if any.
	LoadConnectionPrefs(ctx context.Context, options jumphost.Options) (jumphost.ConnectionPrefs, bool)
	// SaveConnectionPrefs remembers the Connect step choices of options (never the password).
	SaveConnectionPrefs(ctx context.Context, options jumphost.Options) error
	// ListSavedConnections returns the remembered connections, most recent first, of profile and region when set.
	ListSavedConnections(ctx context.Context, profile, region string) ([]jumphost.SavedConnection, error)
	// ForgetConnection removes a remembered connection.
	ForgetConnection(ctx context.Context, ref jumphost.TargetRef) error
	// SearchResources lists the targets of every type; failures holds the types whose listing failed.
	SearchResources(ctx context.Context, options *jumphost.Options) ([]jumphost.ResourceSummary, map[string]error, error)
	// FindResource returns the target of options, or an error when it no longer exists; a
	// non-empty account must be the profile's, else a *jumphost.AccountMismatchError is returned.
	FindResource(ctx context.Context, options *jumphost.Options, account string) (jumphost.ResourceSummary, error)
}

type jumphostStep int

const (
	jumphostStepProfile jumphostStep = iota
	jumphostStepType
	jumphostStepTarget
	// jumphostStepReview summarizes the connection and lets the user edit its port (and database sign-in) before connecting.
	jumphostStepReview
	// jumphostStepStart lists the recent connections next to "New connection" and "Search a target".
	jumphostStepStart
)

// Editable fields of the review step, in focus order.
const (
	reviewFieldPort = iota
	// reviewFieldPasswordless toggles AWS IAM sign-in, where airlock generates short-lived tokens as passwords.
	reviewFieldPasswordless
	reviewFieldUser
	// reviewFieldPassword is asked only when passwordless sign-in is turned off.
	reviewFieldPassword
	// reviewFieldMCPWrites toggles write access for the MCP server of RDS targets.
	reviewFieldMCPWrites
)

// resourceTypeOption is one card of the resource type step.
type resourceTypeOption struct {
	name  string
	title string
	desc  string
}

var resourceTypeOptions = []resourceTypeOption{
	{name: "rds", title: "Relational Database Service", desc: "Tunnel to a database, with IDE data source and MCP server"},
	{name: "eks", title: "Elastic Kubernetes Service", desc: "Reach a private cluster API endpoint"},
	{name: "elasticache", title: "ElastiCache", desc: "Tunnel to a Redis / Valkey cache cluster"},
}

type jumphostWizardModel struct {
	ctx        context.Context
	runner     JumphostDiscovery
	styles     Styles
	step       jumphostStep
	askProfile bool
	flow       jumphostFlow
	// base are the options the wizard was opened with; every flow starts from them.
	base jumphost.Options
	// hasHistory reports saved connections, which make the start screen the root of every flow.
	hasHistory bool
	// recentNeedsProfile asks a profile for a recent connection whose saved profile is gone.
	recentNeedsProfile bool
	// recentAccount is the AWS account the recent connection was saved in.
	recentAccount string
	// awaitingRegion starts the flow's discovery once the default region of the profile is known.
	awaitingRegion bool
	// searchWarnings names the types a search could not list.
	searchWarnings string
	profilesLoaded bool
	errorMsg       string
	loading        bool
	loadingUI      loadingState
	canceled       bool
	// embedded wizards report completion with jumphostWizardDoneMsg instead of quitting the program.
	embedded bool

	options     jumphost.Options
	profileList list.Model
	recent      recentPicker
	// active holds the connectionKey of the dashboard's running sessions, which cannot be reopened.
	active        map[string]bool
	targetList    list.Model
	portInput     textinput.Model
	userInput     textinput.Model
	passwordInput textinput.Model
	// targetIAMAuth reports whether the selected target accepts AWS IAM, which adds the sign-in fields.
	targetIAMAuth bool
	// passwordless signs in with generated AWS IAM tokens; off, the user types the password.
	passwordless bool
	// mcpWrites lets the MCP server modify data; off, it is read-only.
	mcpWrites     bool
	resourceIndex int
	// reviewFocus is the focused review field (reviewFieldPort, ...).
	reviewFocus int

	// defaultRegion is the region the runner falls back to while options.AwsRegion is empty; "" until resolved.
	defaultRegion string
	// regionResolved reports that defaultRegion holds the runner's answer, even an empty one.
	regionResolved bool
	regionPicker   regionPicker
	pickingRegion  bool
	// regionUnblocks continues the discovery the region picker was opened for, once a region is picked.
	regionUnblocks bool

	width  int
	height int
}

type jumphostListItem struct {
	name    string
	desc    string
	iamAuth bool
	// resourceType is set on search results, which mix types.
	resourceType string
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
	err error
	// prefs are the choices saved at the last connection to the target; saved is false without any.
	prefs jumphost.ConnectionPrefs
	port  int
	saved bool
}

// jumphostRegionResolvedMsg carries the default region of profile.
type jumphostRegionResolvedMsg struct {
	profile string
	region  string
}

// jumphostWizardDoneMsg is emitted by an embedded wizard once the user confirmed or canceled.
type jumphostWizardDoneMsg struct {
	options  jumphost.Options
	canceled bool
}

func newJumphostWizardModel(ctx context.Context, runner JumphostDiscovery, base jumphost.Options) jumphostWizardModel {
	askProfile := strings.TrimSpace(base.AwsProfile) == ""

	if strings.TrimSpace(base.AwsProfile) == "" {
		base.AwsProfile = strings.TrimSpace(os.Getenv("AWS_PROFILE"))
	}
	if strings.TrimSpace(base.AwsProfile) == "" {
		base.AwsProfile = "default"
	}
	base.AwsRegion = strings.TrimSpace(base.AwsRegion)

	styles := NewStyles()

	portInput := textinput.New()
	portInput.Placeholder = "Local port"
	portInput.Prompt = ""
	portInput.CharLimit = 6

	userInput := textinput.New()
	userInput.Placeholder = "Database user"
	if def := runner.ResolveDBUser(""); def != "" {
		userInput.Placeholder = def
	}
	userInput.Prompt = ""
	userInput.CharLimit = 63

	passwordInput := textinput.New()
	passwordInput.Placeholder = "Database password"
	passwordInput.Prompt = ""
	passwordInput.EchoMode = textinput.EchoPassword
	passwordInput.EchoCharacter = '•'
	passwordInput.CharLimit = 256

	m := jumphostWizardModel{
		ctx:           ctx,
		runner:        runner,
		styles:        styles,
		step:          jumphostStepStart,
		flow:          flowStart,
		askProfile:    askProfile,
		loading:       true,
		base:          base,
		options:       base,
		profileList:   newWizardList(styles, nil),
		targetList:    newWizardList(styles, nil),
		portInput:     portInput,
		userInput:     userInput,
		passwordInput: passwordInput,
		passwordless:  true,
		recent:        newRecentPicker(styles, nil, nil, time.Now()),
	}
	// Started here rather than in Init so the label survives: Init works on a copy of the model.
	m.loadingUI.start("Loading recent connections...", []string{"Remembering where you have been..."})
	return m
}

func (m jumphostWizardModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.loadSavedCmd(), loadingTickCmd()}
	if !m.askProfile {
		cmds = append(cmds, m.resolveRegionCmd())
	}
	return tea.Batch(cmds...)
}

func (m jumphostWizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		bodyWidth, bodyHeight := wizardBodySize(msg.Width, msg.Height)
		m.profileList.SetSize(bodyWidth, bodyHeight)
		m.targetList.SetSize(bodyWidth, bodyHeight)
		m.recent.SetSize(bodyWidth, bodyHeight)
		return m, nil
	case loadingTickMsg:
		if !m.loading {
			return m, nil
		}
		return m, m.loadingUI.update(msg)
	case jumphostRegionResolvedMsg:
		// Ignore answers for a profile the user already moved away from.
		if msg.profile == m.options.AwsProfile {
			m.defaultRegion = msg.region
			m.regionResolved = true
			if m.awaitingRegion {
				m.awaitingRegion = false
				m.loading = false
				m.loadingUI.stop()
				return m.startDiscovery()
			}
		}
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.canceled = true
			return m, m.finish()
		}
		if m.pickingRegion {
			return m.updateRegionPicker(msg)
		}
		if msg.String() == "q" && !m.typingText() {
			m.canceled = true
			return m, m.finish()
		}
		if m.loading {
			return m, nil
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
		m.profilesLoaded = true

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
			return m.failDiscovery(msg.err.Error(), true)
		}
		if len(msg.targets) == 0 {
			return m.failDiscovery(fmt.Sprintf("No %s targets found in %s", strings.ToUpper(m.options.ResourceType), m.currentRegion()), true)
		}

		items := make([]list.Item, 0, len(msg.targets))
		for _, target := range msg.targets {
			items = append(items, jumphostListItem{name: target.Name, desc: target.Description, iamAuth: target.IAMAuth})
		}
		m.targetList.ResetFilter()
		m.targetList.SetItems(items)
		m.targetList.Select(0)
		m.step = jumphostStepTarget
		return m, nil
	case jumphostSearchLoadedMsg:
		return m.handleSearchLoaded(msg)
	case jumphostSavedLoadedMsg:
		return m.handleSavedLoaded(msg)
	case jumphostLookupMsg:
		return m.handleLookup(msg)
	case jumphostForgotMsg:
		return m.handleForgot(msg)
	case jumphostDefaultPortLoadedMsg:
		m.loading = false
		m.loadingUI.stop()
		if msg.err != nil {
			return m.failDiscovery(msg.err.Error(), false)
		}

		m.portInput.SetValue(strconv.Itoa(msg.port))
		m.portInput.CursorEnd()
		// A --db-user flag wins over the user saved for the target.
		user := m.options.DBUser
		if strings.TrimSpace(user) == "" && msg.saved {
			user = msg.prefs.DBUser
		}
		m.userInput.SetValue(m.runner.ResolveDBUser(user))
		m.userInput.CursorEnd()
		// Every connection starts from the target's saved choices, else passwordless and with the
		// --mcp-allow-writes default. A password typed for another target is never reused.
		m.passwordless = !msg.saved || msg.prefs.SignInMethod() != jumphost.SignInPassword
		m.passwordInput.Reset()
		m.mcpWrites = m.options.MCPAllowWrites || (msg.saved && msg.prefs.MCPAllowWrites)
		m.step = jumphostStepReview
		m = m.focusReviewField(reviewFieldPort)
		return m, textinput.Blink
	}

	// Non-key messages such as cursor blinks reach the picker's search input while it is open.
	if m.pickingRegion {
		return m.updateRegionPicker(msg)
	}

	switch m.step {
	case jumphostStepStart:
		return m.updateStartStep(msg)
	case jumphostStepProfile:
		return m.updateProfileStep(msg)
	case jumphostStepType:
		return m.updateTypeStep(msg)
	case jumphostStepTarget:
		return m.updateTargetStep(msg)
	case jumphostStepReview:
		return m.updateReviewStep(msg)
	default:
		return m, nil
	}
}

// typingText reports whether printable keys belong to a text field (review inputs or list filter).
func (m jumphostWizardModel) typingText() bool {
	switch m.step {
	case jumphostStepStart:
		return m.recent.typing() || m.recent.confirmingForget()
	case jumphostStepReview:
		return true
	case jumphostStepProfile:
		return m.profileList.SettingFilter()
	case jumphostStepTarget:
		return m.targetList.SettingFilter()
	default:
		return false
	}
}

// currentRegion is the region the next discovery or session uses; "" while the default is still resolving.
func (m jumphostWizardModel) currentRegion() string {
	if m.options.AwsRegion != "" {
		return m.options.AwsRegion
	}
	return m.defaultRegion
}

func (m jumphostWizardModel) updateProfileStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" && !m.profileList.SettingFilter() && !m.profileList.IsFiltered() {
		return m.goBack()
	}
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "enter" {
		selected, ok := m.profileList.SelectedItem().(jumphostListItem)
		if !ok {
			return m, nil
		}

		if selected.name != m.options.AwsProfile {
			m.defaultRegion = ""
			m.regionResolved = false
		}
		m.options.AwsProfile = selected.name
		m.errorMsg = ""
		return m.discoverWhenRegionKnown()
	}

	var cmd tea.Cmd
	m.profileList, cmd = m.profileList.Update(msg)
	return m, cmd
}

func (m jumphostWizardModel) updateTypeStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch key.String() {
	case "esc":
		return m.goBack()
	case "up", "k", "shift+tab":
		m.resourceIndex = (m.resourceIndex - 1 + len(resourceTypeOptions)) % len(resourceTypeOptions)
		return m, nil
	case "down", "j", "tab":
		m.resourceIndex = (m.resourceIndex + 1) % len(resourceTypeOptions)
		return m, nil
	case "r":
		return m.openRegionPicker()
	case "s":
		// Without history there is no start screen: search starts from here.
		if !m.hasHistory {
			return m.beginSearch()
		}
		return m, nil
	case "enter":
		return m.pickResourceType(m.resourceIndex)
	}

	// Digits pick a card directly.
	if index, err := strconv.Atoi(key.String()); err == nil && index >= 1 && index <= len(resourceTypeOptions) {
		return m.pickResourceType(index - 1)
	}
	return m, nil
}

// pickResourceType chooses the type of a new connection, then asks the profile unless --profile gave it.
func (m jumphostWizardModel) pickResourceType(index int) (tea.Model, tea.Cmd) {
	m.resourceIndex = index
	m.options.ResourceType = resourceTypeOptions[index].name
	m.errorMsg = ""
	if m.askProfile {
		return m.enterProfileStep()
	}
	return m.discoverWhenRegionKnown()
}

func (m jumphostWizardModel) startLoadingTargets() (tea.Model, tea.Cmd) {
	if m.currentRegion() == "" && m.regionResolved {
		m.errorMsg = "No AWS region set for this profile: pick one"
		m.regionUnblocks = true
		return m.openRegionPicker()
	}
	m.errorMsg = ""
	m.loading = true
	label := "Discovering AWS resources..."
	if region := m.currentRegion(); region != "" {
		label = fmt.Sprintf("Discovering %s resources in %s...", strings.ToUpper(m.options.ResourceType), region)
	}
	return m, tea.Batch(
		m.loadTargetsCmd(),
		m.loadingUI.start(label, []string{
			"Opening the cloud map...",
			"Looking for targets that answer the ping...",
			"Querying AWS with dramatic suspense...",
			"Trying not to wake sleepy EC2 neighbors...",
		}),
	)
}

func (m jumphostWizardModel) updateTargetStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && !m.targetList.SettingFilter() {
		switch key.String() {
		case "esc":
			// A first esc clears an applied filter; the next one goes back.
			if m.targetList.IsFiltered() {
				m.targetList.ResetFilter()
				return m, nil
			}
			return m.goBack()
		case "r":
			return m.openRegionPicker()
		}
	}

	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "enter" {
		selected, ok := m.targetList.SelectedItem().(jumphostListItem)
		if !ok {
			return m, nil
		}

		m.options.Name = selected.name
		if selected.resourceType != "" {
			m.options.ResourceType = selected.resourceType
		}
		m.targetIAMAuth = m.options.ResourceType == "rds" && selected.iamAuth
		m.errorMsg = ""
		return m.startLoadingPort()
	}

	var cmd tea.Cmd
	m.targetList, cmd = m.targetList.Update(msg)
	return m, cmd
}

// startLoadingPort suggests the local port and loads the saved choices of the chosen target.
func (m jumphostWizardModel) startLoadingPort() (tea.Model, tea.Cmd) {
	m.loading = true
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

func (m jumphostWizardModel) openRegionPicker() (tea.Model, tea.Cmd) {
	m.regionPicker = newRegionPicker(m.currentRegion())
	m.pickingRegion = true
	return m, textinput.Blink
}

func (m jumphostWizardModel) updateRegionPicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	picker, outcome, cmd := m.regionPicker.Update(msg)
	m.regionPicker = picker

	switch outcome {
	case regionPickerCanceled:
		m.pickingRegion = false
		unblocks := m.regionUnblocks
		m.regionUnblocks = false
		// Without a region, a flow started from the start screen or the Type step is over.
		if unblocks && (m.step == jumphostStepStart || m.step == jumphostStepType) {
			return m.failDiscovery(m.errorMsg, false)
		}
		return m, nil
	case regionPickerChosen:
		m.pickingRegion = false
		unblocks := m.regionUnblocks
		m.regionUnblocks = false
		region, _ := picker.choice()
		changed := region != m.currentRegion()
		m.options.AwsRegion = region
		m.errorMsg = ""
		// Targets are regional: the listed ones no longer apply.
		if unblocks || (changed && m.step == jumphostStepTarget) {
			return m.startDiscovery()
		}
		return m, nil
	default:
		return m, cmd
	}
}

func (m jumphostWizardModel) updateReviewStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			m = m.blurReviewInputs()
			return m.goBack()
		case "tab", "down":
			return m.moveReviewFocus(1), nil
		case "shift+tab", "up":
			return m.moveReviewFocus(-1), nil
		case "enter":
			return m.submitReview()
		}
		if m.reviewFocus == reviewFieldPasswordless || m.reviewFocus == reviewFieldMCPWrites {
			switch key.String() {
			case " ", "left", "right", "h", "l":
				m = m.toggleReviewField()
			}
			return m, nil
		}
	}

	var cmd tea.Cmd
	switch m.reviewFocus {
	case reviewFieldUser:
		m.userInput, cmd = m.userInput.Update(msg)
	case reviewFieldPassword:
		m.passwordInput, cmd = m.passwordInput.Update(msg)
	case reviewFieldPort:
		m.portInput, cmd = m.portInput.Update(msg)
	}
	return m, cmd
}

// toggleReviewField flips the focused toggle field.
func (m jumphostWizardModel) toggleReviewField() jumphostWizardModel {
	switch m.reviewFocus {
	case reviewFieldPasswordless:
		m.passwordless = !m.passwordless
	case reviewFieldMCPWrites:
		m.mcpWrites = !m.mcpWrites
	}
	m.errorMsg = ""
	return m
}

// reviewFields lists the editable review fields in focus order: the sign-in fields only apply to
// targets accepting AWS IAM, the password only once passwordless sign-in is turned off, and the
// MCP write toggle only to RDS targets getting an MCP server.
func (m jumphostWizardModel) reviewFields() []int {
	fields := []int{reviewFieldPort}
	if m.targetIAMAuth {
		fields = append(fields, reviewFieldPasswordless, reviewFieldUser)
		if !m.passwordless {
			fields = append(fields, reviewFieldPassword)
		}
	}
	if m.mcpApplies() {
		fields = append(fields, reviewFieldMCPWrites)
	}
	return fields
}

// mcpApplies reports whether the connection starts an MCP server, which only RDS targets get.
func (m jumphostWizardModel) mcpApplies() bool {
	return m.options.ResourceType == "rds" && !m.options.SkipMCP
}

// moveReviewFocus focuses the field delta positions away from the current one, wrapping around.
func (m jumphostWizardModel) moveReviewFocus(delta int) jumphostWizardModel {
	fields := m.reviewFields()
	current := 0
	for idx, field := range fields {
		if field == m.reviewFocus {
			current = idx
		}
	}
	next := ((current+delta)%len(fields) + len(fields)) % len(fields)
	return m.focusReviewField(fields[next])
}

// focusReviewField moves the cursor to field.
func (m jumphostWizardModel) focusReviewField(field int) jumphostWizardModel {
	m = m.blurReviewInputs()
	m.reviewFocus = field
	switch field {
	case reviewFieldUser:
		m.userInput.Focus()
	case reviewFieldPassword:
		m.passwordInput.Focus()
	case reviewFieldPort:
		m.portInput.Focus()
	}
	return m
}

func (m jumphostWizardModel) blurReviewInputs() jumphostWizardModel {
	m.portInput.Blur()
	m.userInput.Blur()
	m.passwordInput.Blur()
	return m
}

// submitReview validates every field, focusing the first invalid one, and connects when all are valid.
func (m jumphostWizardModel) submitReview() (tea.Model, tea.Cmd) {
	port, err := strconv.Atoi(strings.TrimSpace(m.portInput.Value()))
	if err != nil || port <= 0 {
		m.errorMsg = "Port must be a positive number"
		return m.focusReviewField(reviewFieldPort), nil
	}
	if err := m.runner.CheckLocalPort(port); err != nil {
		m.errorMsg = err.Error()
		return m.focusReviewField(reviewFieldPort), nil
	}

	m.options.DBPassword = ""
	if m.targetIAMAuth {
		user := strings.TrimSpace(m.userInput.Value())
		if strings.ContainsAny(user, " \t") {
			m.errorMsg = "Database user must not contain spaces"
			return m.focusReviewField(reviewFieldUser), nil
		}
		if m.passwordless {
			resolved := m.runner.ResolveDBUser(user)
			if resolved == "" {
				m.errorMsg = "Enter the database user signing in with AWS IAM"
				return m.focusReviewField(reviewFieldUser), nil
			}
			m.options.DBUser = resolved
		} else {
			if user == "" {
				m.errorMsg = "Enter the database user signing in with the password"
				return m.focusReviewField(reviewFieldUser), nil
			}
			if m.passwordInput.Value() == "" {
				m.errorMsg = "Enter the database password, or turn passwordless back on"
				return m.focusReviewField(reviewFieldPassword), nil
			}
			m.options.DBUser = user
			m.options.DBPassword = m.passwordInput.Value()
		}
	}

	if m.mcpApplies() {
		m.options.MCPAllowWrites = m.mcpWrites
	}
	m.options.LocalPortNumber = port
	m.errorMsg = ""
	m = m.blurReviewInputs()
	// Remembering the choices is a convenience: a failure to save never blocks the connection.
	_ = m.runner.SaveConnectionPrefs(m.ctx, m.prefsOptions())
	return m, m.finish()
}

// finish ends the wizard: standalone wizards quit the program, embedded ones notify their host.
func (m jumphostWizardModel) finish() tea.Cmd {
	if !m.embedded {
		return tea.Quit
	}
	done := jumphostWizardDoneMsg{options: m.options, canceled: m.canceled}
	return func() tea.Msg { return done }
}

func (m jumphostWizardModel) loadTargetsCmd() tea.Cmd {
	opts := m.options
	return func() tea.Msg {
		targets, err := m.runner.ListResourceSummaries(m.ctx, &opts)
		return jumphostTargetsLoadedMsg{targets: targets, err: err}
	}
}

// loadDefaultPortCmd suggests a free local port and loads the choices saved for the target; its
// saved port is suggested instead while it is free.
func (m jumphostWizardModel) loadDefaultPortCmd() tea.Cmd {
	opts := m.options
	prefsOpts := m.prefsOptions()
	return func() tea.Msg {
		port, err := m.runner.ResolveDefaultLocalPort(m.ctx, opts)
		if err != nil {
			return jumphostDefaultPortLoadedMsg{err: err}
		}
		prefs, saved := m.runner.LoadConnectionPrefs(m.ctx, prefsOpts)
		if saved && prefs.LocalPort > 0 && m.runner.CheckLocalPort(prefs.LocalPort) == nil {
			port = prefs.LocalPort
		}
		return jumphostDefaultPortLoadedMsg{port: port, prefs: prefs, saved: saved}
	}
}

// prefsOptions are the options keying the target's saved choices: the region shown in the header,
// so choices are saved and loaded under the region the user sees.
func (m jumphostWizardModel) prefsOptions() jumphost.Options {
	opts := m.options
	if opts.AwsRegion == "" {
		opts.AwsRegion = m.defaultRegion
	}
	return opts
}

// resolveRegionCmd looks up the default region of the current profile unless a region was chosen explicitly.
func (m jumphostWizardModel) resolveRegionCmd() tea.Cmd {
	if m.options.AwsRegion != "" {
		return nil
	}
	profile := m.options.AwsProfile
	return func() tea.Msg {
		return jumphostRegionResolvedMsg{profile: profile, region: m.runner.ResolveRegion(m.ctx, profile)}
	}
}

func (m jumphostWizardModel) loadProfilesCmd() tea.Cmd {
	return func() tea.Msg {
		profiles, err := m.runner.ListAWSProfileSummaries(m.ctx)
		return jumphostProfilesLoadedMsg{profiles: profiles, err: err}
	}
}
