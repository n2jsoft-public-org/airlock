package ui

import (
	"errors"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft-public-org/airlock/internal/directdb"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
	"github.com/n2jsoft-public-org/airlock/internal/sessions"
)

// directChoicesLoadedMsg carries the choices remembered for the direct database name: MCP writes,
// and its password when the OS keychain keeps it.
type directChoicesLoadedMsg struct {
	name              string
	keychainPassword  string
	prefs             jumphost.ConnectionPrefs
	saved             bool
	keychainAvailable bool
}

// enterDirectReview opens the review step of db; its remembered choices (password from the
// keychain, MCP writes) are loaded in the background.
func (m jumphostWizardModel) enterDirectReview(db directdb.Database) (jumphostWizardModel, tea.Cmd) {
	m.directDB = db
	m.options.ResourceType = sessions.DirectResourceType
	m.options.Name = db.Name
	m.options.DBUser = db.User
	m.errorMsg = ""
	m.step = jumphostStepReview
	// A password typed for another database is never reused.
	m.passwordInput.Reset()
	m.remember, m.prefilledFor = false, ""
	m.keychainAvailable, m.directChoicesLoaded = false, false
	m.mcpWrites = m.base.MCPAllowWrites && !m.writesLocked
	m = m.focusReviewField(reviewFieldPassword)
	return m, tea.Batch(textinput.Blink, m.loadDirectChoicesCmd(db))
}

// clearDirectOptions restores the options a direct review sets (target, user, password and
// sign-in) to the command-line defaults, so a direct database's user never reaches the review of
// another target as if --db-user gave it.
func (m jumphostWizardModel) clearDirectOptions() jumphostWizardModel {
	m.options.Name = m.base.Name
	m.options.DBUser = m.base.DBUser
	m.options.DBPassword = m.base.DBPassword
	m.options.DBSecretARN = m.base.DBSecretARN
	m.options.SignIn = m.base.SignIn
	m.options.RememberPassword = m.base.RememberPassword
	m.options.LocalPortNumber = m.base.LocalPortNumber
	return m
}

// loadDirectChoicesCmd loads the choices remembered for db, and its password from the keychain
// when the keychain keeps one for db's user.
func (m jumphostWizardModel) loadDirectChoicesCmd(db directdb.Database) tea.Cmd {
	runner, ctx := m.runner, m.ctx
	options := jumphost.Options{ResourceType: sessions.DirectResourceType, Name: db.Name, DBUser: db.User}
	return func() tea.Msg {
		msg := directChoicesLoadedMsg{name: db.Name, keychainAvailable: runner.KeychainAvailable()}
		msg.prefs, msg.saved = runner.LoadConnectionPrefs(ctx, options)
		if msg.saved && msg.prefs.PasswordInKeychain && msg.keychainAvailable {
			msg.keychainPassword, _ = runner.LoadKeychainPassword(ctx, options)
		}
		return msg
	}
}

// handleDirectChoicesLoaded applies the remembered choices to the review of the same database. A
// password typed meanwhile wins over the remembered one.
func (m jumphostWizardModel) handleDirectChoicesLoaded(msg directChoicesLoadedMsg) (tea.Model, tea.Cmd) {
	if m.step != jumphostStepReview || !m.isDirect() || m.directDB.Name != msg.name {
		return m, nil
	}
	m.directChoicesLoaded = true
	m.keychainAvailable = msg.keychainAvailable
	m.mcpWrites = !m.writesLocked && (m.base.MCPAllowWrites || (msg.saved && msg.prefs.MCPAllowWrites))
	m.remember = msg.saved && msg.prefs.PasswordInKeychain && msg.keychainAvailable
	if msg.keychainPassword != "" && m.passwordInput.Value() == "" {
		m.passwordInput.SetValue(msg.keychainPassword)
		m.passwordInput.CursorEnd()
		m.prefilledFor = m.directDB.User
	}
	return m, nil
}

// directReviewFields are the review fields of a direct database: its password, whether the
// keychain keeps it, and the MCP write toggle. Everything else belongs to its definition.
func (m jumphostWizardModel) directReviewFields() []int {
	fields := []int{reviewFieldPassword}
	if m.keychainAvailable {
		fields = append(fields, reviewFieldRemember)
	}
	if m.mcpWritesApply() {
		fields = append(fields, reviewFieldMCPWrites)
	}
	return fields
}

// submitDirectReview connects to the direct database with the typed password.
func (m jumphostWizardModel) submitDirectReview() (tea.Model, tea.Cmd) {
	if m.passwordInput.Value() == "" {
		m.errorMsg = i18n.Format("ui.review.enter_password_of", m.directDB.User)
		return m.focusReviewField(reviewFieldPassword), nil
	}
	m.options.ResourceType = sessions.DirectResourceType
	m.options.Name = m.directDB.Name
	m.options.DBUser = m.directDB.User
	m.options.SignIn = jumphost.SignInPassword
	m.options.DBPassword = m.passwordInput.Value()
	m.options.DBSecretARN = ""
	m.options.RememberPassword = m.remember && m.keychainAvailable
	m.options.LocalPortNumber = 0
	return m.connectReviewed()
}

// renderDirectReviewPanel shows a direct database: its definition, read-only (edited from the
// list), then its password, the keychain and MCP write toggles, and the read-only warning of SQL
// Server. compact keeps only the target row above the fields.
func (m jumphostWizardModel) renderDirectReviewPanel(width int, compact bool) string {
	panelWidth := min(width, reviewPanelWidth)
	// Border (2) and padding (2) around the panel content.
	inner := panelWidth - 4
	db := &m.directDB

	name := m.styles.Text.Bold(true).Render(db.Name)
	env := m.environments.Lookup(db.Environment)
	name = m.envPrefix(env) + name
	var rows []string
	if compact {
		rows = append(rows, m.reviewRow(i18n.T("ui.jumphost_flow_view.target"), m.styles.ResourceBadge(sessions.DirectResourceType)+" "+name, inner))
	} else {
		rows = append(rows,
			m.reviewRow(i18n.T("ui.jumphost_flow_view.type"), m.styles.ResourceBadge(sessions.DirectResourceType)+" "+m.styles.Muted.Render(string(db.Engine)), inner),
			m.reviewRow(i18n.T("ui.jumphost_flow_view.target"), name, inner),
			m.reviewRow(i18n.T("ui.direct_review.server"), db.Address()+m.styles.Faint.Render(i18n.Format("ui.direct_review.tls", string(db.EffectiveTLS()))), inner),
			m.reviewRow(i18n.T("ui.direct_form.database"), m.directDatabaseLabel(), inner),
			m.reviewRow(i18n.T("ui.jumphost_flow_view.user"), db.User+m.styles.Faint.Render(i18n.T("ui.direct_review.edit_with_e_in")), inner),
			m.styles.Rule.Render(strings.Repeat("─", inner)),
		)
	}

	passwordHelp, rememberHelp := i18n.T(reviewPasswordHelp), i18n.T(reviewRememberHelp)
	mcpHelp := i18n.Format("ui.review.mcp_writes_as", db.User)
	if compact {
		passwordHelp, rememberHelp, mcpHelp = "", "", ""
	}
	rows = append(rows, m.reviewInput(reviewFieldPassword, i18n.T("ui.jumphost_flow_view.password"), inputView(m.passwordInput), passwordHelp, inner)...)
	switch {
	case m.keychainAvailable:
		rows = append(rows, m.reviewInput(reviewFieldRemember, i18n.T("ui.jumphost_flow_view.remember"), m.rememberToggle(), rememberHelp, inner)...)
	case m.directChoicesLoaded:
		rows = append(rows, m.reviewRow(i18n.T("ui.jumphost_flow_view.remember"), m.styles.Faint.Render(i18n.T(reviewNoKeychain)), inner))
	}
	if m.mcpWritesApply() {
		rows = append(rows, m.reviewInput(reviewFieldMCPWrites, i18n.T("ui.jumphost_flow_view.mcp"), m.mcpWritesToggle(), mcpHelp, inner)...)
		// SQL Server has no read-only transaction: read-only access rests on the user's grants.
		if !m.mcpWrites && jumphost.DirectReadOnlyWarning(db) != "" {
			rows = append(rows, "  "+m.styles.Warning.Render(truncate(i18n.Format("ui.review.read_only_rests_on", db.User), inner-2)))
		}
	}

	card := m.styles.Card
	if env.Production {
		card = card.BorderForeground(m.styles.WarningColor)
	}
	return card.Width(panelWidth).Render(strings.Join(rows, "\n"))
}

// directDatabaseLabel names the database a direct connection opens: the definition's, else the
// engine's default.
func (m jumphostWizardModel) directDatabaseLabel() string {
	switch {
	case m.directDB.Database != "":
		return m.directDB.Database
	case m.directDB.Engine == directdb.EnginePostgres:
		return "postgres" + m.styles.Faint.Render(i18n.T("ui.direct_review.default"))
	default:
		return m.styles.Faint.Render(i18n.T("ui.direct_review.login_default"))
	}
}

// directDefinitions maps the direct databases to their names; nil without store, or when it
// cannot be read (recent entries then show no address).
func (m jumphostWizardModel) directDefinitions() map[string]directdb.Database {
	if m.databases == nil {
		return nil
	}
	dbs, err := m.databases.List()
	if err != nil {
		return nil
	}
	byName := make(map[string]directdb.Database, len(dbs))
	for _, db := range dbs {
		byName[db.Name] = db
	}
	return byName
}

// directDefinition reads the definition of the direct database name.
func (m jumphostWizardModel) directDefinition(name string) (directdb.Database, error) {
	if m.databases == nil {
		return directdb.Database{}, errors.New(i18n.T("ui.direct_review.direct_databases_are_not"))
	}
	db, found, err := m.databases.Get(name)
	switch {
	case err != nil:
		return directdb.Database{}, err
	case !found:
		return directdb.Database{}, directdb.NotFoundError(name)
	}
	return db, nil
}

// openRecentDirect opens the review of a remembered direct database, its definition read again; a
// definition gone keeps the start screen, with the error.
func (m jumphostWizardModel) openRecentDirect(conn jumphost.SavedConnection) (tea.Model, tea.Cmd) {
	db, err := m.directDefinition(conn.Ref.Name)
	if err != nil {
		m.errorMsg = i18n.Localize(err)
		return m, nil
	}
	m.flow = flowRecent
	m.options = m.base
	m.recentNeedsProfile, m.recentAccount = false, ""
	return m.enterDirectReview(db)
}

// openDirectAtStart opens the review of the direct database name (`airlock direct <name>`) as a
// new connection whose Target step lists the direct databases, so esc leads to them. An unknown
// name stays on the first screen, with the error. openDirectError, when set, shows on the review.
func (m jumphostWizardModel) openDirectAtStart(name string) (tea.Model, tea.Cmd) {
	reason := m.openDirectError
	m.openDirectError = ""
	db, err := m.directDefinition(name)
	if err != nil {
		m.errorMsg = i18n.Localize(err)
		return m, nil
	}
	options := m.typeOptions()
	for idx := range options {
		if options[idx].name == sessions.DirectResourceType {
			m.resourceIndex = idx
		}
	}
	m.flow = flowNew
	m.options = m.base
	m.options.ResourceType = sessions.DirectResourceType
	m = m.reloadDirectList(name)
	m.step = jumphostStepTarget
	m, cmd := m.enterDirectReview(db)
	m.errorMsg = reason
	return m, cmd
}
