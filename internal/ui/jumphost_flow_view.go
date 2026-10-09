package ui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// Help lines of the review's password fields, shared by every target signing in with a password.
const (
	reviewPasswordHelp = "Kept in memory for the MCP server, unless remembered. The IDE asks for it."
	reviewRememberHelp = "Space toggles. On, the OS keychain keeps it for the next connections."
	reviewNoKeychain   = "No OS keychain available: asked at every connection"
)

const (
	resourceCardHeight = 4
	resourceCardWidth  = 72
	reviewPanelWidth   = 72
)

// View lets the wizard stand alone as a tea.Model; the dashboard embeds render().
func (m jumphostWizardModel) View() tea.View { return tea.NewView(m.render()) }

func (m jumphostWizardModel) render() string {
	bodyWidth, bodyHeight := m.styles.frameBodySize(m.width, m.height, m.errorMsg)
	frame := wizardFrame{
		title:    "airlock",
		tagline:  "SSM port forwarding",
		chips:    m.contextChips(),
		errorMsg: m.errorMsg,
		notice:   m.updateNotice,
	}
	frame.steps, frame.current = m.breadcrumb()

	switch {
	case m.loading:
		frame.heading = "Working on it"
		frame.body = m.loadingUI.Body(m.styles)
		frame.keys = [][2]string{{"q", m.quitLabel()}}
		if m.pending {
			frame.keys = [][2]string{{"esc", "cancel"}, {"q", m.quitLabel()}}
		}
	case m.pickingRegion:
		frame.heading = m.stepHeading()
		frame.body = m.regionPicker.View(m.styles, bodyWidth, bodyHeight)
		frame.keys = [][2]string{{"ctrl+c", m.quitLabel()}}
	case m.pickingSecret:
		frame.heading = m.stepHeading()
		frame.body = m.secretPicker.View(m.styles, bodyWidth, bodyHeight)
		frame.keys = [][2]string{{"ctrl+c", m.quitLabel()}}
	case m.showHelp:
		frame.heading = m.stepHeading()
		frame.body = m.styles.renderHelp([]helpGroup{{title: "Keys", keys: m.stepKeys()}}, bodyWidth, bodyHeight)
		frame.keys = [][2]string{{"esc", "close"}}
	default:
		frame.heading = m.stepHeading()
		frame.hint = m.stepHint()
		frame.body = m.stepBody(bodyWidth, bodyHeight)
		frame.keys = m.stepKeys()
	}

	return m.styles.renderWizardFrame(frame, m.width, m.height)
}

func (m jumphostWizardModel) contextChips() []string {
	// The settings have no AWS context.
	if m.step == jumphostStepSettings {
		return nil
	}
	// A direct database has no AWS context; its review shows the server it connects to.
	if m.isDocker() {
		if m.step != jumphostStepReview {
			return nil
		}
		return []string{m.styles.contextChip("server", m.dockerContainer.Address())}
	}
	if m.isDirect() {
		if m.step != jumphostStepReview {
			return nil
		}
		return []string{m.styles.contextChip("server", m.directDB.Address())}
	}
	region := m.styles.contextChip("region", "…") + m.styles.Faint.Render(" resolving")
	if code := m.currentRegion(); code != "" {
		region = m.styles.regionChip(code)
	} else if m.regionResolved {
		region = m.styles.contextChip("region", "none") + m.styles.Faint.Render(" · press r")
	}
	profile := m.styles.contextChip("profile", m.options.AwsProfile)
	if badge := m.styles.EnvBadge(m.environments.Match(m.options.AwsProfile)); badge != "" {
		profile += " " + badge
	}
	return []string{profile, region}
}

// breadcrumb lists the steps of the current flow and the index of the current one; the start
// screen has none.
func (m jumphostWizardModel) breadcrumb() (steps []string, current int) {
	if m.flow == flowStart || m.step == jumphostStepSettings {
		return nil, 0
	}
	for idx, step := range m.flowSteps() {
		steps = append(steps, m.stepLabel(step))
		// The form belongs to the Target step: it adds to or edits the direct database list.
		if step == m.step || (m.step == jumphostStepDirectForm && step == jumphostStepTarget) {
			current = idx
		}
	}
	return steps, current
}

func (m jumphostWizardModel) stepLabel(step jumphostStep) string {
	switch step {
	case jumphostStepProfile:
		return "Profile"
	case jumphostStepType:
		return "Type"
	case jumphostStepTarget:
		return "Target"
	case jumphostStepReview:
		return "Connect"
	default:
		return ""
	}
}

// canGoBack reports whether esc leads somewhere from the current step.
func (m jumphostWizardModel) canGoBack() bool {
	_, ok := m.previousStep(m.step)
	return ok || m.leavesToHost()
}

func (m jumphostWizardModel) stepHeading() string {
	switch m.step {
	case jumphostStepSettings:
		return "Settings"
	case jumphostStepStart:
		return "Recent connections"
	case jumphostStepProfile:
		return "Choose an AWS profile"
	case jumphostStepType:
		return "What do you want to reach?"
	case jumphostStepTarget:
		switch {
		case m.flow == flowSearch:
			return "Search a target"
		case m.isDocker():
			return "Choose a Docker database"
		case m.isDirect():
			return "Choose a direct database"
		}
		return fmt.Sprintf("Choose the %s target", strings.ToUpper(m.options.ResourceType))
	case jumphostStepReview:
		return "Review and connect"
	case jumphostStepDirectForm:
		if m.directForm.previousName != "" {
			return "Edit " + m.directForm.previousName
		}
		return "Add a direct database"
	default:
		return ""
	}
}

func (m jumphostWizardModel) stepHint() string {
	switch m.step {
	case jumphostStepSettings:
		return "config.json · flags of this run win"
	case jumphostStepStart:
		return plural(len(m.recent.list.Items()), "connection")
	case jumphostStepProfile:
		return plural(len(m.profileList.Items()), "profile")
	case jumphostStepTarget:
		if m.isDocker() {
			if m.dockerList.err != nil || !m.dockerList.loaded {
				return ""
			}
			return plural(m.dockerList.count(), "container")
		}
		if m.isDirect() {
			if m.directList.err != nil {
				return ""
			}
			return plural(m.directList.count(), "database")
		}
		hint := plural(len(m.targetList.Items()), "target") + " in " + m.currentRegion()
		if m.searchWarnings != "" {
			hint += " · ⚠ " + m.searchWarnings
		}
		return hint
	case jumphostStepReview:
		return "edit any field, then connect"
	case jumphostStepDirectForm:
		return "saved to saved-connections.json · passwords never are"
	default:
		return ""
	}
}

func (m jumphostWizardModel) stepBody(width, height int) string {
	switch m.step {
	case jumphostStepStart:
		return m.recent.View(m.styles, width)
	case jumphostStepProfile:
		return m.profileList.View()
	case jumphostStepType:
		return m.renderResourceCards(width, height)
	case jumphostStepTarget:
		if m.isDocker() {
			return m.dockerList.View(m.styles, width)
		}
		if m.isDirect() {
			return m.directList.View(m.styles, width)
		}
		return m.targetList.View()
	case jumphostStepReview:
		return m.renderReview(width, height)
	case jumphostStepDirectForm:
		return m.directForm.View(m.styles, width)
	case jumphostStepSettings:
		return m.settingsEditor.View(m.styles, width)
	default:
		return ""
	}
}

func (m jumphostWizardModel) stepKeys() [][2]string {
	quit := [2]string{"q", m.quitLabel()}
	help := [2]string{"?", "help"}
	switch m.step {
	case jumphostStepSettings:
		return m.settingsKeys(help, quit)
	case jumphostStepStart:
		switch {
		case m.recent.confirmingForget():
			return [][2]string{{"y", "forget"}, {"any key", "keep"}}
		case m.recent.typing():
			return [][2]string{{"enter", "select"}, {"esc", "clear filter"}}
		}
		enter := "reconnect"
		if item, ok := m.recent.selected(); ok && item.connected {
			enter = "switch to"
		}
		keys := [][2]string{{"↑↓", "move"}, {"/", "filter"}, {"enter", enter}, {"n", "new"}, {"s", "search"}, {"d", "forget"}}
		if m.settings != nil {
			keys = append(keys, [2]string{",", "settings"})
		}
		if m.canGoBack() {
			keys = append(keys, [2]string{"esc", "back"})
		}
		return append(keys, help, quit)
	case jumphostStepProfile:
		if m.profileList.SettingFilter() {
			return [][2]string{{"enter", "select"}, {"esc", "clear filter"}}
		}
		keys := [][2]string{{"↑↓", "move"}, {"/", "filter"}, {"enter", "select"}}
		if m.canGoBack() {
			keys = append(keys, [2]string{"esc", "back"})
		}
		return append(keys, help, quit)
	case jumphostStepType:
		keys := [][2]string{{"↑↓", "move"}, {"1-" + strconv.Itoa(len(m.typeOptions())), "pick"}, {"enter", "select"}, {"r", "region"}}
		if m.settings != nil {
			keys = append(keys, [2]string{",", "settings"})
		}
		if !m.hasHistory {
			keys = append(keys, [2]string{"s", "search"})
		}
		if m.canGoBack() {
			keys = append(keys, [2]string{"esc", "back"})
		}
		return append(keys, help, quit)
	case jumphostStepTarget:
		if m.isDocker() {
			return m.dockerListKeys(help, quit)
		}
		if m.isDirect() {
			return m.directListKeys(help, quit)
		}
		if m.targetList.SettingFilter() {
			return [][2]string{{"enter", "select"}, {"esc", "clear filter"}}
		}
		return [][2]string{{"↑↓", "move"}, {"/", "filter"}, {"enter", "select"}, {"r", "region"}, {"esc", "back"}, help, quit}
	case jumphostStepReview:
		keys := [][2]string{{"enter", "connect"}}
		if m.reviewFocus == reviewFieldSecret {
			keys = [][2]string{{"enter", "search"}}
		}
		if len(m.reviewFields()) > 1 {
			keys = append(keys, [2]string{"tab", "next field"})
		}
		if m.reviewFocus == reviewFieldSignIn || m.reviewFocus == reviewFieldRemember || m.reviewFocus == reviewFieldMCPWrites {
			keys = append(keys, [2]string{"space", "toggle"})
		}
		return append(keys, [2]string{"esc", "back"}, [2]string{"ctrl+c", m.quitLabel()})
	case jumphostStepDirectForm:
		keys := [][2]string{{"tab", "next field"}, {"ctrl+s", "save"}}
		if m.directForm.toggleFocused() {
			keys = append(keys, [2]string{"space", "change"})
		}
		return append(keys, [2]string{"esc", "cancel"}, [2]string{"ctrl+c", m.quitLabel()})
	default:
		return [][2]string{quit}
	}
}

// directListKeys are the keys of the direct database list.
func (m jumphostWizardModel) directListKeys(help, quit [2]string) [][2]string {
	switch {
	case m.directList.confirmingDelete():
		return [][2]string{{"y", "delete"}, {"any key", "keep"}}
	case m.directList.typing():
		return [][2]string{{"enter", "select"}, {"esc", "clear filter"}}
	}
	keys := [][2]string{{"↑↓", "move"}, {"/", "filter"}}
	if item, ok := m.directList.selectedItem(); ok {
		enter := "select"
		if item.connected {
			enter = "switch to"
		}
		keys = append(keys, [2]string{"enter", enter}, [2]string{"e", "edit"}, [2]string{"x", "delete"})
	}
	keys = append(keys, [2]string{"a", "add"})
	return append(keys, [2]string{"esc", "back"}, help, quit)
}

// renderResourceCards draws one bordered card per resource type, or single lines when height is short.
func (m jumphostWizardModel) renderResourceCards(width, height int) string {
	options := m.typeOptions()
	compact := height < len(options)*resourceCardHeight
	cardWidth := min(width, resourceCardWidth)

	rows := make([]string, 0, len(options))
	for idx, option := range options {
		selected := idx == m.resourceIndex
		title := m.styles.Text.Render(option.title)
		if selected {
			title = m.styles.Text.Bold(true).Render(option.title)
		}
		badge := m.styles.ResourceBadge(option.name)

		if compact {
			marker := "  "
			if selected {
				marker = m.styles.CardMarker.Render("▌ ")
			}
			rows = append(rows, truncate(marker+badge+" "+title, width))
			continue
		}

		// Border (2) and padding (2) around the card content.
		inner := cardWidth - 4
		header := spread(badge+"  "+title, m.styles.Faint.Render(strconv.Itoa(idx+1)), inner)
		desc := m.styles.Muted.Render(truncate(option.desc, inner))
		style := m.styles.Card
		if selected {
			style = m.styles.CardFocus
		}
		rows = append(rows, style.Width(cardWidth).Render(header+"\n"+desc))
	}
	return strings.Join(rows, "\n")
}

// renderReview shows the chosen connection with its editable fields and the connect button.
// Short terminals get a compact card: the header already shows profile and region.
func (m jumphostWizardModel) renderReview(width, height int) string {
	panel := m.renderReviewPanel(width, false)
	if lipgloss.Height(panel) > height {
		return m.renderReviewPanel(width, true)
	}

	button := m.styles.ButtonFocus.Render("Connect  ⏎")
	if lipgloss.Height(panel)+2 > height {
		return panel
	}
	return panel + "\n\n" + button
}

// reviewUserInput is the user input as the review shows it: with AWS IAM, an empty user signs in
// as the default user of the engine, which is then its placeholder.
func (m jumphostWizardModel) reviewUserInput() textinput.Model {
	input := m.userInput
	if m.signIn == jumphost.SignInIAM {
		if def := m.runner.ResolveDBUser(m.targetEngine, ""); def != "" {
			input.Placeholder = def
		}
	}
	return input
}

func (m jumphostWizardModel) renderReviewPanel(width int, compact bool) string {
	if m.isDocker() {
		return m.renderDockerReviewPanel(width, compact)
	}
	if m.isDirect() {
		return m.renderDirectReviewPanel(width, compact)
	}
	panelWidth := min(width, reviewPanelWidth)
	// Border (2) and padding (2) around the panel content.
	inner := panelWidth - 4

	var rows []string
	if compact {
		rows = append(rows, m.reviewRow("Target", m.styles.ResourceBadge(m.options.ResourceType)+" "+m.styles.Text.Bold(true).Render(m.options.Name), inner))
	} else {
		region := m.currentRegion()
		if name := awsRegionName(region); name != "" {
			region += m.styles.Faint.Render(" · " + name)
		}
		rows = append(rows,
			m.reviewRow("Type", m.styles.ResourceBadge(m.options.ResourceType), inner),
			m.reviewRow("Target", m.styles.Text.Bold(true).Render(m.options.Name), inner),
			m.reviewRow("AWS", m.envPrefix(m.environments.Match(m.options.AwsProfile))+m.options.AwsProfile+m.styles.Faint.Render(" · ")+region, inner),
			m.styles.Rule.Render(strings.Repeat("─", inner)),
		)
	}

	portHelp := "Ports already used by sessions or local apps are skipped."
	signInHelp := "←/→ or space change the method."
	userHelp := "The database user the password belongs to."
	mcpHelp := "Space toggles. On, AI assistants write as the user above."
	switch m.signIn {
	case jumphost.SignInIAM:
		userHelp = "Signs in with AWS IAM."
		if def := m.runner.ResolveDBUser(m.targetEngine, ""); def != "" {
			userHelp = "Signs in with AWS IAM. Leave empty for " + def + "."
		}
	case jumphost.SignInSecret:
		userHelp = "Only needed when the secret holds a bare password (no username)."
	case jumphost.SignInAuto:
		signInHelp = "Tagged read-only secrets; the master user secret with MCP writes."
	}
	if m.targetSQLServer() && m.signIn == jumphost.SignInAuto {
		mcpHelp = "Space toggles. On, SQL Server uses the master user secret."
	}
	passwordHelp, rememberHelp := reviewPasswordHelp, reviewRememberHelp
	secretHelp := "Enter searches the secrets of this account and region."
	if m.options.ResourceType == "elasticache" {
		mcpHelp = "Space toggles. On, AI assistants can run write commands as the user above."
		passwordHelp = "Kept in memory for the MCP server, unless remembered."
		switch m.signIn {
		case jumphost.SignInIAM:
			userHelp = "The RBAC user (its user id) signing in with AWS IAM."
		case jumphost.SignInPassword:
			userHelp = "The RBAC user; leave empty for default (AUTH token)."
		}
	}
	if compact {
		portHelp, signInHelp, userHelp, passwordHelp, rememberHelp, secretHelp, mcpHelp = "", "", "", "", "", "", ""
	}
	rows = append(rows, m.reviewInput(reviewFieldPort, "Port", inputView(m.portInput), portHelp, inner)...)
	if len(m.signInOptions()) > 0 {
		rows = append(rows, m.reviewInput(reviewFieldSignIn, "Sign-in", m.signInSelector(), signInHelp, inner)...)
		for _, field := range m.signInFields() {
			switch field {
			case reviewFieldUser:
				rows = append(rows, m.reviewInput(reviewFieldUser, "User", inputView(m.reviewUserInput()), userHelp, inner)...)
			case reviewFieldPassword:
				rows = append(rows, m.reviewInput(reviewFieldPassword, "Password", inputView(m.passwordInput), passwordHelp, inner)...)
			case reviewFieldRemember:
				rows = append(rows, m.reviewInput(reviewFieldRemember, "Remember", m.rememberToggle(), rememberHelp, inner)...)
			case reviewFieldSecret:
				rows = append(rows, m.reviewInput(reviewFieldSecret, "Secret", m.secretValue(), secretHelp, inner)...)
			default:
				// The other fields have their own rows.
			}
		}
		if m.signIn == jumphost.SignInPassword && !m.keychainAvailable {
			rows = append(rows, m.reviewRow("Remember", m.styles.Faint.Render(reviewNoKeychain), inner))
		}
	}
	switch {
	case m.mcpWritesApply():
		rows = append(rows, m.reviewInput(reviewFieldMCPWrites, "MCP", m.mcpWritesToggle(), mcpHelp, inner)...)
	case m.mcpApplies():
		rows = append(rows, m.reviewRow("MCP", m.styles.Muted.Render("○ read-only")+m.styles.Faint.Render("  AI assistants can only read the cluster"), inner))
	}

	card := m.styles.Card
	if m.environments.Match(m.options.AwsProfile).Production {
		card = card.BorderForeground(m.styles.WarningColor)
	}
	return card.Width(panelWidth).Render(strings.Join(rows, "\n"))
}

// envPrefix is the badge of env and a space, else "".
func (m jumphostWizardModel) envPrefix(env environment.Env) string {
	if env.IsZero() {
		return ""
	}
	return m.styles.EnvBadge(env) + " "
}

// signInSelector renders the sign-in methods, the chosen one highlighted.
func (m jumphostWizardModel) signInSelector() string {
	labels := make([]string, 0, len(m.signInOptions()))
	chosen := ""
	for _, method := range m.signInOptions() {
		labels = append(labels, signInLabels[method])
		if method == m.signIn {
			chosen = signInLabels[method]
		}
	}
	return choiceSelector(m.styles, labels, chosen)
}

// rememberToggle renders whether the password is kept in the OS keychain.
func (m jumphostWizardModel) rememberToggle() string {
	if m.remember {
		return m.styles.Success.Render("● on") + m.styles.Faint.Render("  in the OS keychain")
	}
	return m.styles.Muted.Render("○ off") + m.styles.Faint.Render("  asked at every connection")
}

// secretValue renders the picked secret, or how to pick one.
func (m jumphostWizardModel) secretValue() string {
	if m.secret.ARN == "" {
		return m.styles.Muted.Render("none") + m.styles.Faint.Render("  enter to search")
	}
	return m.styles.Text.Render(m.secret.Name)
}

// mcpWritesToggle renders whether the MCP server may modify data.
func (m jumphostWizardModel) mcpWritesToggle() string {
	if m.mcpWrites {
		return m.styles.Warning.Render("● writes allowed") + m.styles.Faint.Render("  AI assistants can modify data")
	}
	return m.styles.Muted.Render("○ read-only") + m.styles.Faint.Render("  AI assistants can only query")
}

func (m jumphostWizardModel) reviewRow(label, value string, width int) string {
	return "  " + m.styles.FieldLabel.Render(label) + truncate(value, maxInt(1, width-2-m.styles.FieldLabel.GetWidth()))
}

// reviewInput renders an editable field; the focused one gets the accent marker and its help line, if any.
func (m jumphostWizardModel) reviewInput(field int, label, input, help string, width int) []string {
	if field != m.reviewFocus {
		return []string{"  " + m.styles.FieldLabel.Render(label) + input}
	}
	rows := []string{m.styles.CardMarker.Render("▌ ") + m.styles.FieldLabel.Foreground(m.styles.AccentColor).Render(label) + input}
	if help != "" {
		rows = append(rows, "  "+m.styles.Faint.Render(truncate(help, width-2)))
	}
	return rows
}

func (m jumphostWizardModel) quitLabel() string {
	// Without any session to return to, canceling an embedded wizard also quits its host.
	if m.embedded && m.hostHasSessions {
		return "cancel"
	}
	return "quit"
}

func plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}
