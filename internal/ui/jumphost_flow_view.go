package ui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// Catalog keys of the help lines of the review's password fields, shared by every target signing in
// with a password.
const (
	reviewPasswordHelp = "ui.review.password_help"
	reviewRememberHelp = "ui.review.remember_help"
	reviewNoKeychain   = "ui.review.no_keychain"
)

const (
	resourceCardHeight = 4
	resourceCardWidth  = 72
	reviewPanelWidth   = 72
)

// View lets the wizard stand alone as a tea.Model; the dashboard embeds render().
func (m jumphostWizardModel) View() tea.View { return tea.NewView(m.render()) }

func (m jumphostWizardModel) render() string {
	bodyWidth, bodyHeight := m.styles.frameBodySize(m.width, m.height, m.errorMsg, m.notices())
	frame := wizardFrame{
		title:    "airlock",
		tagline:  i18n.T("ui.jumphost_flow_view.ssm_port_forwarding"),
		chips:    m.contextChips(),
		errorMsg: m.errorMsg,
		notices:  m.notices(),
	}
	frame.steps, frame.current = m.breadcrumb()

	switch {
	case m.loading:
		frame.heading = i18n.T("ui.jumphost_flow_view.working_on_it")
		frame.body = m.loadingUI.Body(m.styles)
		frame.keys = [][2]string{{"q", m.quitLabel()}}
		if m.pending {
			frame.keys = [][2]string{{"esc", i18n.T("ui.hint.cancel")}, {"q", m.quitLabel()}}
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
		frame.body = m.styles.renderHelp([]helpGroup{{title: i18n.T("ui.jumphost_flow_view.keys"), keys: m.stepKeys()}}, bodyWidth, bodyHeight)
		frame.keys = [][2]string{{"esc", i18n.T("ui.hint.close")}}
	default:
		frame.heading = m.stepHeading()
		frame.hint = m.stepHint()
		frame.body = m.stepBody(bodyWidth, bodyHeight)
		frame.keys = m.stepKeys()
	}

	return m.styles.renderWizardFrame(frame, m.width, m.height)
}

// notices are the lines of the banner under the breadcrumb: the update notice, then the
// organization's.
func (m jumphostWizardModel) notices() []string {
	if m.updateNotice == "" {
		return m.organizationNotice
	}
	return append([]string{"↑ " + m.updateNotice}, m.organizationNotice...)
}

func (m jumphostWizardModel) contextChips() []string {
	// The settings have no AWS context; they show the organization config they come from.
	if m.step == jumphostStepSettings {
		if source := m.settingsEditor.snapshot.Organization; source != "" {
			return []string{m.styles.contextChip(i18n.T("ui.settings.source_organization"), source)}
		}
		return nil
	}
	// A direct database has no AWS context; its review shows the server it connects to.
	if m.isDocker() {
		if m.step != jumphostStepReview {
			return nil
		}
		return []string{m.styles.contextChip(i18n.T("ui.hint.server"), m.dockerContainer.Address())}
	}
	if m.isDirect() {
		if m.step != jumphostStepReview {
			return nil
		}
		return []string{m.styles.contextChip(i18n.T("ui.hint.server"), m.directDB.Address())}
	}
	region := m.styles.contextChip(i18n.T("ui.hint.region"), "…") + m.styles.Faint.Render(i18n.T("ui.jumphost_flow_view.resolving"))
	if code := m.currentRegion(); code != "" {
		region = m.styles.regionChip(code)
	} else if m.regionResolved {
		region = m.styles.contextChip(i18n.T("ui.hint.region"), i18n.T("ui.none")) + m.styles.Faint.Render(i18n.T("ui.jumphost_flow_view.press_r"))
	}
	profile := m.styles.contextChip(i18n.T("ui.hint.profile"), m.options.AwsProfile)
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
		return i18n.T("ui.jumphost_flow_view.profile")
	case jumphostStepType:
		return i18n.T("ui.jumphost_flow_view.type")
	case jumphostStepTarget:
		return i18n.T("ui.jumphost_flow_view.target")
	case jumphostStepReview:
		return i18n.T("ui.jumphost_flow_view.connect")
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
		return i18n.T("ui.jumphost_flow_view.settings")
	case jumphostStepStart:
		return i18n.T("ui.jumphost_flow_view.recent_connections")
	case jumphostStepProfile:
		return i18n.T("ui.jumphost_flow_view.choose_aws_profile")
	case jumphostStepType:
		return i18n.T("ui.jumphost_flow_view.what_do_you_want")
	case jumphostStepTarget:
		switch {
		case m.flow == flowSearch:
			return i18n.T("ui.jumphost_flow_view.search_target")
		case m.isDocker():
			return i18n.T("ui.jumphost_flow_view.choose_docker_database")
		case m.isDirect():
			return i18n.T("ui.jumphost_flow_view.choose_direct_database")
		}
		return i18n.Format("ui.jumphost_flow_view.choose_target", strings.ToUpper(m.options.ResourceType))
	case jumphostStepReview:
		return i18n.T("ui.jumphost_flow_view.review_and_connect")
	case jumphostStepDirectForm:
		if m.directForm.previousName != "" {
			return i18n.Format("ui.wizard.edit_heading", m.directForm.previousName)
		}
		return i18n.T("ui.jumphost_flow_view.add_direct_database")
	default:
		return ""
	}
}

func (m jumphostWizardModel) stepHint() string {
	switch m.step {
	case jumphostStepSettings:
		return i18n.T("ui.jumphost_flow_view.config_json_flags_of")
	case jumphostStepStart:
		return i18n.Plural(len(m.recent.list.Items()), "ui.count.connection")
	case jumphostStepProfile:
		return i18n.Plural(len(m.profileList.Items()), "ui.count.profile")
	case jumphostStepTarget:
		if m.isDocker() {
			if m.dockerList.err != nil || !m.dockerList.loaded {
				return ""
			}
			return i18n.Plural(m.dockerList.count(), "ui.count.container")
		}
		if m.isDirect() {
			if m.directList.err != nil {
				return ""
			}
			return i18n.Plural(m.directList.count(), "ui.count.database")
		}
		hint := i18n.Format("ui.wizard.targets_in_region", i18n.Plural(len(m.targetList.Items()), "ui.count.target"), m.currentRegion())
		if m.searchWarnings != "" {
			hint += " · ⚠ " + m.searchWarnings
		}
		return hint
	case jumphostStepReview:
		return i18n.T("ui.jumphost_flow_view.edit_any_field_then")
	case jumphostStepDirectForm:
		return i18n.T("ui.jumphost_flow_view.saved_to_saved_connections")
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
		return m.settingsEditor.View(m.styles, width, height)
	default:
		return ""
	}
}

func (m jumphostWizardModel) stepKeys() [][2]string {
	quit := [2]string{"q", m.quitLabel()}
	help := [2]string{"?", i18n.T("ui.hint.help")}
	switch m.step {
	case jumphostStepSettings:
		return m.settingsKeys(help, quit)
	case jumphostStepStart:
		switch {
		case m.recent.confirmingForget():
			return [][2]string{{"y", i18n.T("ui.hint.forget")}, {i18n.T("ui.hint.any_key"), i18n.T("ui.hint.keep")}}
		case m.recent.typing():
			return [][2]string{{"enter", i18n.T("ui.hint.select")}, {"esc", i18n.T("ui.hint.clear_filter")}}
		}
		enter := i18n.T("ui.hint.reconnect")
		if item, ok := m.recent.selected(); ok && item.connected {
			enter = i18n.T("ui.hint.switch_to")
		}
		keys := [][2]string{{"↑↓", i18n.T("ui.hint.move")}, {"/", i18n.T("ui.hint.filter")}, {"enter", enter}, {"n", i18n.T("ui.hint.new")}, {"s", i18n.T("ui.hint.search")}, {"d", i18n.T("ui.hint.forget")}}
		if m.settings != nil {
			keys = append(keys, [2]string{",", i18n.T("ui.hint.settings")})
		}
		if m.canGoBack() {
			keys = append(keys, [2]string{"esc", i18n.T("ui.hint.back")})
		}
		return append(keys, help, quit)
	case jumphostStepProfile:
		if m.profileList.SettingFilter() {
			return [][2]string{{"enter", i18n.T("ui.hint.select")}, {"esc", i18n.T("ui.hint.clear_filter")}}
		}
		keys := [][2]string{{"↑↓", i18n.T("ui.hint.move")}, {"/", i18n.T("ui.hint.filter")}, {"enter", i18n.T("ui.hint.select")}}
		if m.canGoBack() {
			keys = append(keys, [2]string{"esc", i18n.T("ui.hint.back")})
		}
		return append(keys, help, quit)
	case jumphostStepType:
		keys := [][2]string{{"↑↓", i18n.T("ui.hint.move")}, {"1-" + strconv.Itoa(len(m.typeOptions())), i18n.T("ui.hint.pick")}, {"enter", i18n.T("ui.hint.select")}, {"r", i18n.T("ui.hint.region")}}
		if m.settings != nil {
			keys = append(keys, [2]string{",", i18n.T("ui.hint.settings")})
		}
		if !m.hasHistory {
			keys = append(keys, [2]string{"s", i18n.T("ui.hint.search")})
		}
		if m.canGoBack() {
			keys = append(keys, [2]string{"esc", i18n.T("ui.hint.back")})
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
			return [][2]string{{"enter", i18n.T("ui.hint.select")}, {"esc", i18n.T("ui.hint.clear_filter")}}
		}
		return [][2]string{{"↑↓", i18n.T("ui.hint.move")}, {"/", i18n.T("ui.hint.filter")}, {"enter", i18n.T("ui.hint.select")}, {"r", i18n.T("ui.hint.region")}, {"esc", i18n.T("ui.hint.back")}, help, quit}
	case jumphostStepReview:
		keys := [][2]string{{"enter", i18n.T("ui.hint.connect")}}
		if m.reviewFocus == reviewFieldSecret {
			keys = [][2]string{{"enter", i18n.T("ui.hint.search")}}
		}
		if len(m.reviewFields()) > 1 {
			keys = append(keys, [2]string{"tab", i18n.T("ui.hint.next_field")})
		}
		if m.reviewFocus == reviewFieldSignIn || m.reviewFocus == reviewFieldRemember || m.reviewFocus == reviewFieldMCPWrites {
			keys = append(keys, [2]string{"space", i18n.T("ui.hint.toggle")})
		}
		return append(keys, [2]string{"esc", i18n.T("ui.hint.back")}, [2]string{"ctrl+c", m.quitLabel()})
	case jumphostStepDirectForm:
		keys := [][2]string{{"tab", i18n.T("ui.hint.next_field")}, {"ctrl+s", i18n.T("ui.hint.save")}}
		if m.directForm.toggleFocused() {
			keys = append(keys, [2]string{"space", i18n.T("ui.hint.change")})
		}
		return append(keys, [2]string{"esc", i18n.T("ui.hint.cancel")}, [2]string{"ctrl+c", m.quitLabel()})
	default:
		return [][2]string{quit}
	}
}

// directListKeys are the keys of the direct database list.
func (m jumphostWizardModel) directListKeys(help, quit [2]string) [][2]string {
	switch {
	case m.directList.confirmingDelete():
		return [][2]string{{"y", i18n.T("ui.hint.delete")}, {i18n.T("ui.hint.any_key"), i18n.T("ui.hint.keep")}}
	case m.directList.typing():
		return [][2]string{{"enter", i18n.T("ui.hint.select")}, {"esc", i18n.T("ui.hint.clear_filter")}}
	}
	keys := [][2]string{{"↑↓", i18n.T("ui.hint.move")}, {"/", i18n.T("ui.hint.filter")}}
	if item, ok := m.directList.selectedItem(); ok {
		enter := i18n.T("ui.hint.select")
		if item.connected {
			enter = i18n.T("ui.hint.switch_to")
		}
		keys = append(keys, [2]string{"enter", enter}, [2]string{"e", i18n.T("ui.hint.edit")}, [2]string{"x", i18n.T("ui.hint.delete")})
	}
	keys = append(keys, [2]string{"a", i18n.T("ui.hint.add")})
	return append(keys, [2]string{"esc", i18n.T("ui.hint.back")}, help, quit)
}

// renderResourceCards draws one bordered card per resource type, or single lines when height is short.
func (m jumphostWizardModel) renderResourceCards(width, height int) string {
	options := m.typeOptions()
	compact := height < len(options)*resourceCardHeight
	cardWidth := min(width, resourceCardWidth)

	rows := make([]string, 0, len(options))
	for idx, option := range options {
		selected := idx == m.resourceIndex
		title := m.styles.Text.Render(i18n.T(option.titleKey))
		if selected {
			title = m.styles.Text.Bold(true).Render(i18n.T(option.titleKey))
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
		desc := m.styles.Muted.Render(truncate(i18n.T(option.descKey), inner))
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

	button := m.styles.ButtonFocus.Render(i18n.T("ui.jumphost_flow_view.connect_2"))
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
		rows = append(rows, m.reviewRow(i18n.T("ui.jumphost_flow_view.target"), m.styles.ResourceBadge(m.options.ResourceType)+" "+m.styles.Text.Bold(true).Render(m.options.Name), inner))
	} else {
		region := m.currentRegion()
		if name := awsRegionName(region); name != "" {
			region += m.styles.Faint.Render(" · " + name)
		}
		rows = append(rows,
			m.reviewRow(i18n.T("ui.jumphost_flow_view.type"), m.styles.ResourceBadge(m.options.ResourceType), inner),
			m.reviewRow(i18n.T("ui.jumphost_flow_view.target"), m.styles.Text.Bold(true).Render(m.options.Name), inner),
			m.reviewRow(i18n.T("ui.jumphost_flow_view.aws"), m.envPrefix(m.environments.Match(m.options.AwsProfile))+m.options.AwsProfile+m.styles.Faint.Render(" · ")+region, inner),
			m.styles.Rule.Render(strings.Repeat("─", inner)),
		)
	}

	portHelp := i18n.T("ui.jumphost_flow_view.ports_already_used_by")
	signInHelp := i18n.T("ui.jumphost_flow_view.or_space_change_method")
	userHelp := i18n.T("ui.jumphost_flow_view.database_user_password_belongs")
	mcpHelp := i18n.T("ui.jumphost_flow_view.space_toggles_on_ai")
	switch m.signIn {
	case jumphost.SignInIAM:
		userHelp = i18n.T("ui.jumphost_flow_view.signs_in_with_aws")
		if def := m.runner.ResolveDBUser(m.targetEngine, ""); def != "" {
			userHelp = i18n.Format("ui.review.iam_leave_empty_for", def)
		}
	case jumphost.SignInSecret:
		userHelp = i18n.T("ui.jumphost_flow_view.only_needed_when_secret")
	case jumphost.SignInAuto:
		signInHelp = i18n.T("ui.jumphost_flow_view.tagged_read_only_secrets")
	}
	if m.targetSQLServer() && m.signIn == jumphost.SignInAuto {
		mcpHelp = i18n.T("ui.jumphost_flow_view.space_toggles_on_sql")
	}
	passwordHelp, rememberHelp := i18n.T(reviewPasswordHelp), i18n.T(reviewRememberHelp)
	secretHelp := i18n.T("ui.jumphost_flow_view.enter_searches_secrets_of")
	if m.options.ResourceType == "elasticache" {
		mcpHelp = i18n.T("ui.jumphost_flow_view.space_toggles_on_ai_2")
		passwordHelp = i18n.T("ui.jumphost_flow_view.kept_in_memory_for")
		switch m.signIn {
		case jumphost.SignInIAM:
			userHelp = i18n.T("ui.jumphost_flow_view.rbac_user_its_user")
		case jumphost.SignInPassword:
			userHelp = i18n.T("ui.jumphost_flow_view.rbac_user_leave_empty")
		}
	}
	switch m.options.ResourceType {
	case sessions.DynamoDBResourceType:
		mcpHelp = i18n.T("ui.jumphost_flow_view.space_toggles_dynamodb_writes")
	case sessions.S3ResourceType:
		mcpHelp = i18n.T("ui.jumphost_flow_view.space_toggles_s3_writes")
	}
	if compact {
		portHelp, signInHelp, userHelp, passwordHelp, rememberHelp, secretHelp, mcpHelp = "", "", "", "", "", "", ""
	}
	if !m.isAPIOnly() {
		rows = append(rows, m.reviewInput(reviewFieldPort, i18n.T("ui.jumphost_flow_view.port"), inputView(m.portInput), portHelp, inner)...)
	}
	if len(m.signInOptions()) > 0 {
		rows = append(rows, m.reviewInput(reviewFieldSignIn, i18n.T("ui.jumphost_flow_view.sign_in"), m.signInSelector(), signInHelp, inner)...)
		for _, field := range m.signInFields() {
			switch field {
			case reviewFieldUser:
				rows = append(rows, m.reviewInput(reviewFieldUser, i18n.T("ui.jumphost_flow_view.user"), inputView(m.reviewUserInput()), userHelp, inner)...)
			case reviewFieldPassword:
				rows = append(rows, m.reviewInput(reviewFieldPassword, i18n.T("ui.jumphost_flow_view.password"), inputView(m.passwordInput), passwordHelp, inner)...)
			case reviewFieldRemember:
				rows = append(rows, m.reviewInput(reviewFieldRemember, i18n.T("ui.jumphost_flow_view.remember"), m.rememberToggle(), rememberHelp, inner)...)
			case reviewFieldSecret:
				rows = append(rows, m.reviewInput(reviewFieldSecret, i18n.T("ui.jumphost_flow_view.secret"), m.secretValue(), secretHelp, inner)...)
			default:
				// The other fields have their own rows.
			}
		}
		if m.signIn == jumphost.SignInPassword && !m.keychainAvailable {
			rows = append(rows, m.reviewRow(i18n.T("ui.jumphost_flow_view.remember"), m.styles.Faint.Render(i18n.T(reviewNoKeychain)), inner))
		}
	}
	switch {
	case m.mcpWritesApply():
		rows = append(rows, m.reviewInput(reviewFieldMCPWrites, i18n.T("ui.jumphost_flow_view.mcp"), m.mcpWritesToggle(), mcpHelp, inner)...)
	case m.mcpApplies():
		rows = append(rows, m.reviewRow(i18n.T("ui.jumphost_flow_view.mcp"), m.styles.Muted.Render(i18n.T("ui.jumphost_flow_view.read_only"))+m.styles.Faint.Render(i18n.T("ui.jumphost_flow_view.ai_assistants_can_only")), inner))
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
		labels = append(labels, i18n.T(signInLabels[method]))
		if method == m.signIn {
			chosen = i18n.T(signInLabels[method])
		}
	}
	return choiceSelector(m.styles, labels, chosen)
}

// rememberToggle renders whether the password is kept in the OS keychain.
func (m jumphostWizardModel) rememberToggle() string {
	if m.remember {
		return m.styles.Success.Render(i18n.T("ui.jumphost_flow_view.on")) + m.styles.Faint.Render(i18n.T("ui.jumphost_flow_view.in_os_keychain"))
	}
	return m.styles.Muted.Render(i18n.T("ui.jumphost_flow_view.off")) + m.styles.Faint.Render(i18n.T("ui.jumphost_flow_view.asked_at_every_connection"))
}

// secretValue renders the picked secret, or how to pick one.
func (m jumphostWizardModel) secretValue() string {
	if m.secret.ARN == "" {
		return m.styles.Muted.Render(i18n.T("ui.none")) + m.styles.Faint.Render(i18n.T("ui.jumphost_flow_view.enter_to_search"))
	}
	return m.styles.Text.Render(m.secret.Name)
}

// mcpWritesToggle renders whether the MCP server may modify data.
func (m jumphostWizardModel) mcpWritesToggle() string {
	if m.writesLocked {
		return m.styles.Muted.Render(i18n.T("ui.jumphost_flow_view.read_only")) + m.styles.Faint.Render(i18n.T("ui.jumphost_flow_view.writes_locked"))
	}
	if m.mcpWrites {
		return m.styles.Warning.Render(i18n.T("ui.jumphost_flow_view.writes_allowed")) + m.styles.Faint.Render(i18n.T("ui.jumphost_flow_view.ai_assistants_can_modify"))
	}
	return m.styles.Muted.Render(i18n.T("ui.jumphost_flow_view.read_only")) + m.styles.Faint.Render(i18n.T("ui.jumphost_flow_view.ai_assistants_can_only_2"))
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
		return i18n.T("ui.hint.cancel")
	}
	return i18n.T("ui.hint.quit")
}
