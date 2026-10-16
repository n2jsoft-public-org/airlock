package ui

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/dockerdb"
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

type JumphostDiscovery interface {
	ListAWSProfileSummaries(ctx context.Context) ([]jumphost.AWSProfileSummary, error)
	ListResourceSummaries(ctx context.Context, options *jumphost.Options) ([]jumphost.ResourceSummary, error)
	ResolveDefaultLocalPort(ctx context.Context, options jumphost.Options) (int, error)
	CheckLocalPort(port int) error
	// ResolveRegion returns the region used for profile when none is chosen explicitly.
	ResolveRegion(ctx context.Context, profile string) string
	// ResolveDBUser returns user, else the default database user of engine (an AWS RDS engine
	// name); "" when none is configured.
	ResolveDBUser(engine, user string) string
	// LoadConnectionPrefs returns the Connect step choices saved for the database of options, if any.
	LoadConnectionPrefs(ctx context.Context, options jumphost.Options) (jumphost.ConnectionPrefs, bool)
	// SaveConnectionPrefs remembers the Connect step choices of options (never the password, remembered
	// in the OS keychain only when asked). Keychain failures come back wrapped in jumphost.ErrKeychain.
	SaveConnectionPrefs(ctx context.Context, options jumphost.Options) error
	// ListSavedConnections returns the remembered connections, most recent first, of profile and region when set.
	ListSavedConnections(ctx context.Context, profile, region string) ([]jumphost.SavedConnection, error)
	// ForgetConnection removes a remembered connection.
	ForgetConnection(ctx context.Context, ref jumphost.TargetRef) error
	// KeychainAvailable reports whether typed passwords can be remembered in the OS keychain.
	KeychainAvailable() bool
	// LoadKeychainPassword returns the password of options.DBUser remembered for the target of options.
	LoadKeychainPassword(ctx context.Context, options jumphost.Options) (string, bool)
	// ListDBSecrets lists the secrets that may hold the password of the RDS or cache target of options, linked ones first.
	ListDBSecrets(ctx context.Context, options *jumphost.Options) ([]jumphost.SecretSummary, error)
	// ListCacheIAMUsers lists the users of the cache of options signing in with AWS IAM.
	ListCacheIAMUsers(ctx context.Context, options *jumphost.Options) ([]string, error)
	// SearchResources lists the targets of every type; failures holds the types whose listing failed.
	SearchResources(ctx context.Context, options *jumphost.Options) ([]jumphost.ResourceSummary, map[string]error, error)
	// FindResource returns the target of options, or an error when it no longer exists; a
	// non-empty account must be the profile's, else a *jumphost.AccountMismatchError is returned.
	FindResource(ctx context.Context, options *jumphost.Options, account string) (jumphost.ResourceSummary, error)
	// ModuleDisabled reports whether config.json turns off the targets of resourceType.
	ModuleDisabled(resourceType string) bool
}

// DirectDatabaseStore holds the direct database definitions (saved-connections.json), which the wizard
// lists, adds, edits and deletes.
type DirectDatabaseStore interface {
	List() ([]directdb.Database, error)
	Get(name string) (directdb.Database, bool, error)
	Put(previousName string, db directdb.Database) error
	Delete(name string) error
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
	// jumphostStepDirectForm adds or edits a direct database, opened from the direct database list.
	jumphostStepDirectForm
	// jumphostStepSettings shows and edits the settings of config.json.
	jumphostStepSettings
)

// Editable fields of the review step, in focus order.
const (
	reviewFieldPort = iota
	// reviewFieldSignIn picks the RDS sign-in method (IAM token, password, Secrets Manager, auto).
	reviewFieldSignIn
	// reviewFieldSecret opens the secret picker of the Secrets Manager method.
	reviewFieldSecret
	reviewFieldUser
	// reviewFieldPassword is asked by the password method only.
	reviewFieldPassword
	// reviewFieldRemember keeps the typed password in the OS keychain.
	reviewFieldRemember
	// reviewFieldMCPWrites toggles write access for the MCP server of RDS targets.
	reviewFieldMCPWrites
)

// resourceTypeOption is one card of the resource type step; its texts are catalog keys, since the
// cards are built before the language is set.
type resourceTypeOption struct {
	name     string
	titleKey string
	descKey  string
}

var resourceTypeOptions = []resourceTypeOption{
	{name: "rds", titleKey: "ui.card.rds.title", descKey: "ui.card.rds.desc"},
	{name: "eks", titleKey: "ui.card.eks.title", descKey: "ui.card.eks.desc"},
	{name: "elasticache", titleKey: "ui.card.elasticache.title", descKey: "ui.card.elasticache.desc"},
	{name: "dynamodb", titleKey: "ui.card.dynamodb.title", descKey: "ui.card.dynamodb.desc"},
	{name: "s3", titleKey: "ui.card.s3.title", descKey: "ui.card.s3.desc"},
}

// directCard is the Type step card of direct databases, offered when the dashboard has a store.
var directCard = resourceTypeOption{
	name:     sessions.DirectResourceType,
	titleKey: "ui.card.direct.title",
	descKey:  "ui.card.direct.desc",
}

type jumphostWizardModel struct {
	profileList list.Model
	targetList  list.Model
	styles      Styles
	ctx         context.Context
	runner      JumphostDiscovery

	// databases holds the direct database definitions; nil hides the Direct database card.
	databases DirectDatabaseStore

	// settings reads and writes config.json; nil hides the Settings screen.
	settings SettingsStore

	// cancelLoad cancels the context of the awaited loading, stopping its AWS calls.
	cancelLoad context.CancelFunc

	// active holds the connectionKey of the dashboard's running sessions, which cannot be reopened.
	active map[string]bool

	// directList is the target step of a direct database; directDB the database the review
	// step connects to.
	directList directList
	// docker finds the database containers; nil hides the Docker database card.
	docker jumphost.DockerContainers
	// dockerList is the target step of a Docker database; dockerContainer the container the
	// review step connects to.
	dockerList      dockerList
	dockerContainer dockerdb.Container

	// recentAccount is the AWS account the recent connection was saved in.
	recentAccount string

	// searchWarnings names the types a search could not list.
	searchWarnings string
	errorMsg       string

	// switchTo is the connectionKey of the running session the dashboard shows instead of opening a new one.
	switchTo string

	// updateNotice, when not empty, is shown under the breadcrumb: a newer airlock release exists.
	updateNotice string
	// organizationNotice, shown under the update notice, is what the user must know about the
	// organization config: its warnings and last changes.
	organizationNotice []string

	// openDirect is the direct database whose review the wizard opens on once the history is
	// loaded (`airlock direct <name>`); "" otherwise.
	openDirect string

	// openDirectError is shown on the review openDirect opens: why it asks the password again.
	openDirectError string
	// dockerAtStart opens the Docker list once the history is loaded (`airlock docker`), and the
	// review of openDocker when set; openDockerError shows on that review.
	dockerAtStart   bool
	openDocker      string
	openDockerError string

	// warning is shown in the session log once connected (e.g. the keychain failed).
	warning      string
	targetEngine string

	// targetCacheAuth is how the selected ElastiCache target signs clients in.
	targetCacheAuth string

	// signIn is the chosen RDS sign-in method (jumphost.SignIn*).
	signIn string

	// prefilledFor is the user whose keychain password fills the password field; "" once the user
	// typed a password, or when none was prefilled.
	prefilledFor string

	// defaultRegion is the region the runner falls back to while options.AwsRegion is empty; "" until resolved.
	defaultRegion string
	recent        recentPicker

	// base are the options the wizard was opened with; every flow starts from them.
	base    jumphost.Options
	options jumphost.Options

	// secret is the picked Secrets Manager secret; a zero ARN means none.
	secret jumphost.SecretSummary

	// environments names the AWS profiles' environments (environments setting).
	environments  environment.Set
	portInput     textinput.Model
	userInput     textinput.Model
	passwordInput textinput.Model
	directDB      directdb.Database
	regionPicker  regionPicker

	// settingsEditor is the Settings screen (jumphostStepSettings).
	settingsEditor settingsEditor
	secretPicker   secretPicker
	loadingUI      loadingState

	// directForm adds or edits a direct database (jumphostStepDirectForm).
	directForm directForm
	step       jumphostStep
	flow       jumphostFlow

	// loadID identifies the result the loading waits for: a canceled loading's result carries an
	// older one and is dropped. pending reports that such a result is awaited, which esc cancels.
	loadID int

	// settingsReturn is the step esc returns to from the Settings screen.
	settingsReturn jumphostStep
	resourceIndex  int

	// reviewFocus is the focused review field (reviewFieldPort, ...).
	reviewFocus int
	width       int
	height      int
	askProfile  bool

	// hasHistory reports saved connections, which make the start screen the root of every flow.
	hasHistory bool

	// recentNeedsProfile asks a profile for a recent connection whose saved profile is gone.
	recentNeedsProfile bool

	// awaitingRegion starts the flow's discovery once the default region of the profile is known.
	awaitingRegion bool
	profilesLoaded bool
	loading        bool
	canceled       bool

	// showHelp lists the keys of the current step in place of its body.
	showHelp bool
	pending  bool

	// loadReplacesTargets reports that the awaited result replaces the listed targets, which a
	// canceled loading must not leave on screen (they belong to the previous region).
	loadReplacesTargets bool

	// embedded wizards report completion with jumphostWizardDoneMsg instead of quitting the program.
	embedded bool

	// directChoicesLoaded reports that the choices remembered for directDB are known.
	directChoicesLoaded bool

	// settingsLeavesToHost makes esc on the Settings screen return to the dashboard that opened it.
	settingsLeavesToHost bool

	// settingsAtStart opens the Settings screen once the history is loaded (`,` in the dashboard).
	settingsAtStart bool

	// regionForSettings makes the region picker choose a setting instead of the connection's region.
	regionForSettings bool

	// hostHasSessions reports that the embedded wizard's dashboard shows sessions to return to.
	hostHasSessions bool

	// targetIAMAuth reports whether the selected target accepts AWS IAM; targetEngine is its RDS engine.
	targetIAMAuth bool

	// remember keeps the typed password in the OS keychain; keychainAvailable offers it.
	remember          bool
	keychainAvailable bool

	// mcpWrites lets the MCP server modify data; off, it is read-only.
	mcpWrites bool
	// writesLocked keeps mcpWrites off: the organization locks MCP writes.
	writesLocked bool

	// regionResolved reports that defaultRegion holds the runner's answer, even an empty one.
	regionResolved bool
	pickingRegion  bool
	pickingSecret  bool

	// regionUnblocks continues the discovery the region picker was opened for, once a region is picked.
	regionUnblocks bool
}

type jumphostListItem struct {
	name   string
	desc   string
	engine string
	// cacheAuth is how an ElastiCache target signs clients in (jumphost.CacheAuth*).
	cacheAuth string
	// resourceType is set on search results, which mix types.
	resourceType string
	iamAuth      bool
	// env is the environment of the profile under the environments setting.
	env environment.Env
}

// environment is the environment of the profile.
func (i jumphostListItem) environment() environment.Env { return i.env }

func (i jumphostListItem) FilterValue() string {
	return strings.Join(strings.Fields(strings.Join([]string{i.name, i.resourceType, i.desc}, " ")), " ")
}

// itemType is the resource type shown as a badge on search results; "" on single-type lists.
func (i jumphostListItem) itemType() string { return i.resourceType }

func (i jumphostListItem) Title() string       { return i.name }
func (i jumphostListItem) Description() string { return i.desc }

type jumphostTargetsLoadedMsg struct {
	err     error
	targets []jumphost.ResourceSummary
}

type jumphostProfilesLoadedMsg struct {
	err      error
	profiles []jumphost.AWSProfileSummary
}

type jumphostDefaultPortLoadedMsg struct {
	err error
	// keychainPassword is the remembered password of the saved user; "" when none.
	keychainPassword string
	// prefs are the choices saved at the last connection to the target; saved is false without any.
	prefs             jumphost.ConnectionPrefs
	port              int
	saved             bool
	keychainAvailable bool
}

// jumphostSecretsLoadedMsg carries the secrets offered by the secret picker.
type jumphostSecretsLoadedMsg struct {
	err     error
	secrets []jumphost.SecretSummary
}

// jumphostRegionResolvedMsg carries the default region of profile.
type jumphostRegionResolvedMsg struct {
	profile string
	region  string
}

// awaitedMsg carries the result of a cancelable loading (see await).
type awaitedMsg struct {
	msg tea.Msg
	id  int
}

// jumphostWizardDoneMsg is emitted by an embedded wizard once the user confirmed or canceled.
type jumphostWizardDoneMsg struct {
	warning string
	// switchTo, set on a canceled wizard, is the connectionKey of the running session to show.
	switchTo string
	options  jumphost.Options
	canceled bool
}

// newJumphostWizardModel is the wizard on the dark palette (tests, and callers without a terminal).
func newJumphostWizardModel(ctx context.Context, runner JumphostDiscovery, base jumphost.Options) jumphostWizardModel {
	return newStyledWizardModel(ctx, runner, base, NewStyles(true))
}

// newStyledWizardModel is the wizard drawn with styles, the dashboard's palette.
func newStyledWizardModel(ctx context.Context, runner JumphostDiscovery, base jumphost.Options, styles Styles) jumphostWizardModel {
	askProfile := strings.TrimSpace(base.AwsProfile) == ""

	if strings.TrimSpace(base.AwsProfile) == "" {
		base.AwsProfile = strings.TrimSpace(os.Getenv("AWS_PROFILE"))
	}
	if strings.TrimSpace(base.AwsProfile) == "" {
		base.AwsProfile = "default"
	}
	base.AwsRegion = strings.TrimSpace(base.AwsRegion)

	portInput := textinput.New()
	portInput.SetStyles(textInputStyles())
	portInput.Placeholder = i18n.T("ui.jumphost_flow.local_port")
	portInput.Prompt = ""
	portInput.CharLimit = 6

	userInput := textinput.New()
	userInput.SetStyles(textInputStyles())
	userInput.Placeholder = i18n.T("ui.jumphost_flow.database_user")
	userInput.Prompt = ""
	userInput.CharLimit = 63

	passwordInput := textinput.New()
	passwordInput.SetStyles(textInputStyles())
	passwordInput.Placeholder = i18n.T("ui.jumphost_flow.database_password")
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
		recent:        newRecentPicker(styles, nil, nil, environment.Set{}, nil, time.Now()),
		// fitLists sizes it on every update: a zero-value list has no delegate and would panic.
		directList: newDirectList(styles, nil, nil, nil, environment.Set{}),
		dockerList: newDockerList(styles, nil, nil, nil, false),
	}
	// Started here rather than in Init so the label survives: Init works on a copy of the model.
	m.loadingUI.start(i18n.T("ui.jumphost_flow.loading_recent_connections"), []string{i18n.T("ui.jumphost_flow.remembering_where_you_have")})
	return m
}

// typeOptions are the cards of the Type step: the AWS types (the API-only ones, DynamoDB and S3, only
// with MCP, as their session is their MCP server), and direct databases when the dashboard has their
// store; disabled modules have none.
func (m jumphostWizardModel) typeOptions() []resourceTypeOption {
	options := make([]resourceTypeOption, 0, len(resourceTypeOptions)+2)
	for _, option := range resourceTypeOptions {
		if (!jumphost.APIOnly(option.name) || !m.options.SkipMCP) && !m.runner.ModuleDisabled(option.name) {
			options = append(options, option)
		}
	}
	if m.databases != nil && !m.runner.ModuleDisabled(sessions.DirectResourceType) {
		options = append(options, directCard)
	}
	// Docker lists both databases and caches.
	if m.docker != nil && (!m.runner.ModuleDisabled(sessions.DockerResourceType) || !m.runner.ModuleDisabled(sessions.DockerCacheResourceType)) {
		options = append(options, dockerCard)
	}
	return options
}

// isDocker reports whether a new connection works on a Docker database, past the Type step.
func (m jumphostWizardModel) isDocker() bool {
	return m.flow == flowNew && m.options.ResourceType == sessions.DockerResourceType &&
		m.step != jumphostStepType && m.step != jumphostStepStart
}

// isAPIOnly reports whether the flow works on an API-only type (DynamoDB, S3): no port, no sign-in,
// only the MCP write toggle.
func (m jumphostWizardModel) isAPIOnly() bool {
	return jumphost.APIOnly(m.options.ResourceType)
}

// isDirect reports whether the flow works on a direct database: its type is chosen and the Type
// step is behind, in a new connection or a recent direct entry reopened. A search never does, even
// when a direct list left its type behind.
func (m jumphostWizardModel) isDirect() bool {
	return (m.flow == flowNew || m.flow == flowRecent) && m.options.ResourceType == sessions.DirectResourceType &&
		m.step != jumphostStepType && m.step != jumphostStepStart
}

func (m jumphostWizardModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.loadSavedCmd(), loadingTickCmd()}
	if !m.askProfile {
		cmds = append(cmds, m.resolveRegionCmd(m.ctx))
	}
	return tea.Batch(cmds...)
}

// Update handles msg, then fits the lists to the body the error leaves, as it may take several lines.
func (m jumphostWizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := m.update(msg)
	if wizard, ok := updated.(jumphostWizardModel); ok {
		return wizard.fitLists(), cmd
	}
	return updated, cmd
}

// fitLists sizes the lists to the frame body.
func (m jumphostWizardModel) fitLists() jumphostWizardModel {
	bodyWidth, bodyHeight := m.styles.frameBodySize(m.width, m.height, m.errorMsg, m.notices())
	m.profileList.SetSize(bodyWidth, bodyHeight)
	m.targetList.SetSize(bodyWidth, bodyHeight)
	m.recent.SetSize(bodyWidth, bodyHeight)
	m.directList.SetSize(bodyWidth, bodyHeight)
	m.dockerList.SetSize(bodyWidth, bodyHeight)
	return m
}

func (m jumphostWizardModel) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case awaitedMsg:
		if msg.id != m.loadID {
			return m, nil
		}
		m.pending = false
		return m.update(msg.msg)
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
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
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.canceled = true
			return m, m.finish()
		}
		if m.showHelp {
			m.showHelp = false
			return m, nil
		}
		if m.pickingRegion {
			return m.updateRegionPicker(msg)
		}
		if m.pickingSecret {
			return m.updateSecretPicker(msg)
		}
		if msg.String() == "q" && !m.typingText() {
			m.canceled = true
			return m, m.finish()
		}
		if msg.String() == "?" && !m.typingText() && !m.loading {
			m.showHelp = true
			return m, nil
		}
		if m.loading {
			if msg.String() == "esc" && m.pending {
				return m.cancelLoading()
			}
			return m, nil
		}
	case jumphostProfilesLoadedMsg:
		m.loading = false
		m.loadingUI.stop()
		if msg.err != nil {
			m.errorMsg = i18n.Localize(msg.err)
			m.step = jumphostStepProfile
			return m, nil
		}
		if len(msg.profiles) == 0 {
			m.errorMsg = i18n.T("ui.jumphost_flow.no_aws_profile_found")
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
			items = append(items, jumphostListItem{name: profile.Name, desc: profile.Description, env: m.environments.Match(profile.Name)})
		}
		m.profileList.SetItems(items)
		m.profileList.Select(selectedIndex)
		m.step = jumphostStepProfile
		return m, nil
	case jumphostTargetsLoadedMsg:
		m.loading = false
		m.loadingUI.stop()
		if msg.err != nil {
			return m.failDiscovery(i18n.Localize(msg.err), true)
		}
		if len(msg.targets) == 0 {
			return m.failDiscovery(i18n.Format("ui.jumphost_flow.no_targets_found_in", strings.ToUpper(m.options.ResourceType), m.currentRegion()), true)
		}

		items := make([]list.Item, 0, len(msg.targets))
		for _, target := range msg.targets {
			items = append(items, jumphostListItem{name: target.Name, desc: target.Description, iamAuth: target.IAMAuth, engine: target.Engine, cacheAuth: target.CacheAuth})
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
	case dockerListedMsg:
		return m.handleDockerListed(msg)
	case directDeletedMsg:
		return m.handleDirectDeleted(msg)
	case directSavedMsg:
		return m.handleDirectSaved(msg)
	case settingsLoadedMsg:
		return m.handleSettingsLoaded(msg)
	case settingRefusedMsg:
		return m.handleSettingRefused(msg)
	case settingsProfilesMsg:
		return m.handleSettingsProfiles(msg)
	case directChoicesLoadedMsg:
		return m.handleDirectChoicesLoaded(msg)
	case jumphostSecretsLoadedMsg:
		m.secretPicker = m.secretPicker.loaded(msg.secrets, msg.err)
		return m, nil
	case jumphostDefaultPortLoadedMsg:
		m.loading = false
		m.loadingUI.stop()
		if msg.err != nil {
			return m.failDiscovery(i18n.Localize(msg.err), false)
		}

		m.portInput.SetValue(strconv.Itoa(msg.port))
		m.portInput.CursorEnd()
		// A --db-user flag wins over the user saved for the target.
		user := m.options.DBUser
		if strings.TrimSpace(user) == "" && msg.saved {
			user = msg.prefs.DBUser
		}
		if m.options.ResourceType == "rds" {
			user = m.runner.ResolveDBUser(m.targetEngine, user)
		}
		m.userInput.SetValue(user)
		m.userInput.CursorEnd()
		// Every connection starts from the target's saved choices, else the first method offered and
		// the --mcp-allow-writes default. A password typed for another target is never reused.
		m.passwordInput.Reset()
		m.keychainAvailable = msg.keychainAvailable
		m = m.applySavedSignIn(msg)
		m.mcpWrites = !m.writesLocked && (m.options.MCPAllowWrites || (msg.saved && msg.prefs.MCPAllowWrites))
		m.step = jumphostStepReview
		m = m.focusReviewField(reviewFieldPort)
		m.userInput.SetSuggestions(nil)
		m.userInput.ShowSuggestions = false
		if m.options.ResourceType == "elasticache" && m.targetCacheAuth == jumphost.CacheAuthRBAC {
			return m, tea.Batch(textinput.Blink, m.cacheUsersCmd())
		}
		return m, textinput.Blink
	case jumphostCacheUsersMsg:
		// A list requested for another target, or after leaving the review, is stale.
		if m.step != jumphostStepReview || msg.target != cacheTargetKey(&m.options) {
			return m, nil
		}
		m.userInput.SetSuggestions(msg.users)
		m.userInput.ShowSuggestions = len(msg.users) > 0
		if strings.TrimSpace(m.userInput.Value()) == "" && len(msg.users) == 1 {
			m.userInput.SetValue(msg.users[0])
			m.userInput.CursorEnd()
		}
		return m, nil
	}

	// Non-key messages such as cursor blinks reach the picker's search input while it is open.
	if m.pickingRegion {
		return m.updateRegionPicker(msg)
	}
	if m.pickingSecret {
		return m.updateSecretPicker(msg)
	}

	switch m.step {
	case jumphostStepStart:
		return m.updateStartStep(msg)
	case jumphostStepDirectForm:
		return m.updateDirectFormStep(msg)
	case jumphostStepSettings:
		return m.updateSettingsStep(msg)
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
	case jumphostStepDirectForm:
		return true
	case jumphostStepSettings:
		return m.settingsEditor.typing()
	case jumphostStepProfile:
		return m.profileList.SettingFilter()
	case jumphostStepTarget:
		if m.isDocker() {
			return m.dockerList.typing()
		}
		if m.isDirect() {
			return m.directList.typing() || m.directList.confirmingDelete()
		}
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
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "esc" && !m.profileList.SettingFilter() && !m.profileList.IsFiltered() {
		return m.goBack()
	}
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "enter" {
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
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}

	options := m.typeOptions()
	switch key.String() {
	case "esc":
		return m.goBack()
	case "up", "k", "shift+tab":
		m.resourceIndex = (m.resourceIndex - 1 + len(options)) % len(options)
		return m, nil
	case "down", "j", "tab":
		m.resourceIndex = (m.resourceIndex + 1) % len(options)
		return m, nil
	case "r":
		return m.openRegionPicker()
	case ",":
		if m.settings != nil {
			return m.openSettings(false)
		}
		return m, nil
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
	if index, err := strconv.Atoi(key.String()); err == nil && index >= 1 && index <= len(options) {
		return m.pickResourceType(index - 1)
	}
	return m, nil
}

// pickResourceType chooses the type of a new connection, then asks the profile unless --profile
// gave it. A direct database needs neither profile nor region: its list shows at once.
func (m jumphostWizardModel) pickResourceType(index int) (tea.Model, tea.Cmd) {
	m.resourceIndex = index
	m = m.clearDirectOptions()
	m.options.ResourceType = m.typeOptions()[index].name
	m.errorMsg = ""
	if m.options.ResourceType == sessions.DirectResourceType {
		return m.enterDirectList()
	}
	if m.options.ResourceType == sessions.DockerResourceType {
		return m.enterDockerList()
	}
	if m.askProfile {
		return m.enterProfileStep()
	}
	return m.discoverWhenRegionKnown()
}

func (m jumphostWizardModel) startLoadingTargets() (tea.Model, tea.Cmd) {
	if m.currentRegion() == "" && m.regionResolved {
		m.errorMsg = i18n.T("ui.jumphost_flow.no_aws_region_set")
		m.regionUnblocks = true
		return m.openRegionPicker()
	}
	m.errorMsg = ""
	m.loading = true
	label := i18n.T("ui.jumphost_flow.discovering_aws_resources")
	if region := m.currentRegion(); region != "" {
		label = i18n.Format("ui.jumphost_flow.discovering_resources_in", strings.ToUpper(m.options.ResourceType), region)
	}
	load := m.await(m.loadTargetsCmd, true)
	return m, tea.Batch(
		load,
		m.loadingUI.start(label, []string{
			i18n.T("ui.jumphost_flow.opening_cloud_map"),
			i18n.T("ui.jumphost_flow.looking_for_targets_that"),
			i18n.T("ui.jumphost_flow.querying_aws_with_dramatic"),
			i18n.T("ui.jumphost_flow.trying_not_to_wake"),
		}),
	)
}

func (m jumphostWizardModel) updateTargetStep(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.isDocker() {
		return m.updateDockerListStep(msg)
	}
	if m.isDirect() {
		return m.updateDirectListStep(msg)
	}
	if key, ok := msg.(tea.KeyPressMsg); ok && !m.targetList.SettingFilter() {
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

	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "enter" {
		selected, ok := m.targetList.SelectedItem().(jumphostListItem)
		if !ok {
			return m, nil
		}

		m.options.Name = selected.name
		if selected.resourceType != "" {
			m.options.ResourceType = selected.resourceType
		}
		m.targetIAMAuth = m.options.ResourceType == "rds" && selected.iamAuth
		m.targetEngine = selected.engine
		m.targetCacheAuth = selected.cacheAuth
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
	load := m.await(m.loadDefaultPortCmd, false)
	return m, tea.Batch(
		load,
		m.loadingUI.start(i18n.T("ui.jumphost_flow.finding_best_local_port"), []string{
			i18n.T("ui.jumphost_flow.looking_for_chill_port"),
			i18n.T("ui.jumphost_flow.avoiding_port_drama_with"),
			i18n.T("ui.jumphost_flow.knocking_on_localhost_doors"),
			i18n.T("ui.jumphost_flow.port_scouting_mission_in"),
		}),
	)
}

func (m jumphostWizardModel) openRegionPicker() (tea.Model, tea.Cmd) {
	m.regionPicker = newRegionPicker(m.currentRegion())
	m.pickingRegion = true
	return m, textinput.Blink
}

func (m jumphostWizardModel) updateRegionPicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.regionForSettings {
		return m.updateSettingsRegionPicker(msg)
	}
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
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			m = m.blurReviewInputs()
			return m.goBack()
		case "tab", "down":
			return m.moveReviewFocus(1), nil
		case "shift+tab", "up":
			return m.moveReviewFocus(-1), nil
		case "enter":
			if m.reviewFocus == reviewFieldSecret {
				return m.openSecretPicker()
			}
			return m.submitReview()
		}
		if m.reviewFocus == reviewFieldSignIn || m.reviewFocus == reviewFieldRemember || m.reviewFocus == reviewFieldMCPWrites {
			switch key.String() {
			case "space", "right", "l":
				m = m.toggleReviewField(1)
			case "left", "h":
				m = m.toggleReviewField(-1)
			}
			return m, nil
		}
	}

	var cmd tea.Cmd
	switch m.reviewFocus {
	case reviewFieldUser:
		m.userInput, cmd = m.userInput.Update(msg)
		m = m.dropPrefilledPasswordOfOtherUser()
	case reviewFieldPassword:
		before := m.passwordInput.Value()
		m.passwordInput, cmd = m.passwordInput.Update(msg)
		if m.passwordInput.Value() != before {
			m.prefilledFor = ""
		}
	case reviewFieldPort:
		m.portInput, cmd = m.portInput.Update(msg)
	default:
		// The other fields are toggles and pickers, changed by toggleReviewField.
	}
	return m, cmd
}

// toggleReviewField flips the focused toggle, or cycles the sign-in method by delta.
func (m jumphostWizardModel) toggleReviewField(delta int) jumphostWizardModel {
	switch m.reviewFocus {
	case reviewFieldSignIn:
		m = m.cycleSignIn(delta)
	case reviewFieldRemember:
		m.remember = !m.remember
	case reviewFieldMCPWrites:
		m.mcpWrites = !m.mcpWrites && !m.writesLocked
	default:
		// The text fields take keys instead (updateReviewInput); the secret opens its picker.
	}
	m.errorMsg = ""
	return m
}

// reviewFields lists the editable review fields in focus order: the sign-in fields only apply to
// RDS targets offering a method, depend on it, and the MCP write toggle only to targets whose MCP
// server can write.
func (m jumphostWizardModel) reviewFields() []int {
	if m.isDocker() {
		return m.dockerReviewFields()
	}
	if m.isDirect() {
		return m.directReviewFields()
	}
	if m.isAPIOnly() {
		return []int{reviewFieldMCPWrites}
	}
	fields := []int{reviewFieldPort}
	if len(m.signInOptions()) > 0 {
		fields = append(fields, reviewFieldSignIn)
		fields = append(fields, m.signInFields()...)
	}
	if m.mcpWritesApply() {
		fields = append(fields, reviewFieldMCPWrites)
	}
	return fields
}

// mcpApplies reports whether the connection starts an MCP server: RDS targets, EKS clusters,
// Redis / Valkey caches and direct databases.
func (m jumphostWizardModel) mcpApplies() bool {
	if m.options.SkipMCP {
		return false
	}
	switch m.options.ResourceType {
	case "rds", "eks", sessions.DirectResourceType, sessions.DockerResourceType, sessions.DynamoDBResourceType, sessions.S3ResourceType:
		return true
	case "elasticache":
		return jumphost.CacheEngineSupported(m.targetEngine)
	default:
		return false
	}
}

type jumphostCacheUsersMsg struct {
	// target identifies the cache the users were listed for (see cacheTargetKey).
	target string
	users  []string
}

// cacheTargetKey identifies the target of options: resource type, name, profile and region.
func cacheTargetKey(o *jumphost.Options) string {
	return strings.Join([]string{o.ResourceType, o.Name, o.AwsProfile, o.AwsRegion}, "\x00")
}

// cacheUsersCmd lists the IAM users of the selected cache, suggested in the User field.
// A failure only leaves the field without suggestions.
func (m jumphostWizardModel) cacheUsersCmd() tea.Cmd {
	opts := m.options
	return func() tea.Msg {
		users, err := m.runner.ListCacheIAMUsers(m.ctx, &opts)
		if err != nil {
			users = nil
		}
		return jumphostCacheUsersMsg{target: cacheTargetKey(&opts), users: users}
	}
}

// mcpWritesApply reports whether the review offers the MCP write toggle: EKS is always read-only.
func (m jumphostWizardModel) mcpWritesApply() bool {
	return m.mcpApplies() && m.options.ResourceType != "eks"
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
	default:
		// Toggles have no input to focus.
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
	if m.isDocker() {
		return m.submitDockerReview()
	}
	if m.isDirect() {
		return m.submitDirectReview()
	}
	if m.isAPIOnly() {
		return m.connectReviewed()
	}
	port, err := strconv.Atoi(strings.TrimSpace(m.portInput.Value()))
	if err != nil || port <= 0 {
		m.errorMsg = i18n.T("ui.jumphost_flow.port_must_be_positive")
		return m.focusReviewField(reviewFieldPort), nil
	}
	if err := m.runner.CheckLocalPort(port); err != nil {
		m.errorMsg = i18n.Localize(err)
		return m.focusReviewField(reviewFieldPort), nil
	}

	if len(m.signInOptions()) > 0 {
		next, field, errMsg := m.applySignIn()
		if errMsg != "" {
			m.errorMsg = errMsg
			return m.focusReviewField(field), nil
		}
		m = next
	}

	m.options.LocalPortNumber = port
	return m.connectReviewed()
}

// connectReviewed connects with the validated review: it applies the MCP write toggle, remembers
// the choices and ends the wizard.
func (m jumphostWizardModel) connectReviewed() (tea.Model, tea.Cmd) {
	if m.mcpWritesApply() {
		m.options.MCPAllowWrites = m.mcpWrites
	}
	m.errorMsg = ""
	m = m.blurReviewInputs()
	// Remembering the choices is a convenience: a failure to save never blocks the connection.
	// A keychain failure is reported in the session log.
	m.warning = ""
	if err := m.runner.SaveConnectionPrefs(m.ctx, m.prefsOptions()); errors.Is(err, jumphost.ErrKeychain) {
		m.warning = i18n.Localize(err)
	}
	return m, m.finish()
}

// await builds the loading the wizard waits for with a context that esc cancels, stopping its
// AWS calls, and tags its result; replacesTargets tells that the result replaces the listed
// targets. A nil command stays nil.
func (m *jumphostWizardModel) await(build func(ctx context.Context) tea.Cmd, replacesTargets bool) tea.Cmd {
	ctx, cancel := context.WithCancel(m.ctx)
	cmd := build(ctx)
	if cmd == nil {
		cancel()
		return nil
	}
	m.loadID++
	m.pending = true
	m.loadReplacesTargets = replacesTargets
	m.cancelLoad = cancel
	id := m.loadID
	return func() tea.Msg {
		msg := cmd()
		cancel()
		return awaitedMsg{id: id, msg: msg}
	}
}

// cancelLoading stops waiting for the loading's result, which is dropped when it comes, and returns
// to the step it started from; the AWS call ends in the background.
func (m jumphostWizardModel) cancelLoading() (tea.Model, tea.Cmd) {
	if m.cancelLoad != nil {
		m.cancelLoad()
		m.cancelLoad = nil
	}
	m.loadID++
	m.pending = false
	m.loading = false
	m.loadingUI.stop()
	m.awaitingRegion = false
	// Profiles never listed leave nothing to show: back to the step before.
	if m.step == jumphostStepProfile && !m.profilesLoaded {
		return m.goBack()
	}
	return m.failDiscovery("", m.loadReplacesTargets)
}

// finish ends the wizard: standalone wizards quit the program, embedded ones notify their host.
func (m jumphostWizardModel) finish() tea.Cmd {
	if !m.embedded {
		return tea.Quit
	}
	done := jumphostWizardDoneMsg{options: m.options, canceled: m.canceled, warning: m.warning, switchTo: m.switchTo}
	return func() tea.Msg { return done }
}

func (m jumphostWizardModel) loadTargetsCmd(ctx context.Context) tea.Cmd {
	opts := m.options
	return func() tea.Msg {
		targets, err := m.runner.ListResourceSummaries(ctx, &opts)
		return jumphostTargetsLoadedMsg{targets: targets, err: err}
	}
}

// loadDefaultPortCmd suggests a free local port and loads the choices saved for the target; its
// saved port is suggested instead while it is free.
func (m jumphostWizardModel) loadDefaultPortCmd(ctx context.Context) tea.Cmd {
	opts := m.options
	prefsOpts := m.prefsOptions()
	return func() tea.Msg {
		port, err := m.runner.ResolveDefaultLocalPort(ctx, opts)
		if err != nil {
			return jumphostDefaultPortLoadedMsg{err: err}
		}
		prefs, saved := m.runner.LoadConnectionPrefs(ctx, prefsOpts)
		if saved && prefs.LocalPort > 0 && m.runner.CheckLocalPort(prefs.LocalPort) == nil {
			port = prefs.LocalPort
		}
		msg := jumphostDefaultPortLoadedMsg{port: port, prefs: prefs, saved: saved, keychainAvailable: m.runner.KeychainAvailable()}
		if saved && prefs.SignInMethod() == jumphost.SignInPassword && prefs.PasswordInKeychain && msg.keychainAvailable {
			keyOpts := prefsOpts
			// A --db-user flag wins over the saved user, as in the review.
			if strings.TrimSpace(keyOpts.DBUser) == "" {
				keyOpts.DBUser = prefs.DBUser
			}
			msg.keychainPassword, _ = m.runner.LoadKeychainPassword(ctx, keyOpts)
		}
		return msg
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
func (m jumphostWizardModel) resolveRegionCmd(ctx context.Context) tea.Cmd {
	if m.options.AwsRegion != "" {
		return nil
	}
	profile := m.options.AwsProfile
	return func() tea.Msg {
		return jumphostRegionResolvedMsg{profile: profile, region: m.runner.ResolveRegion(ctx, profile)}
	}
}

func (m jumphostWizardModel) loadProfilesCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		profiles, err := m.runner.ListAWSProfileSummaries(ctx)
		return jumphostProfilesLoadedMsg{profiles: profiles, err: err}
	}
}
