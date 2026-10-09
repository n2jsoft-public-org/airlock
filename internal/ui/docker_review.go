package ui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/dockerdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// enterDockerReview opens the review of c, its password prefilled from the container. A local
// container is a scratch database: MCP writes start on.
func (m jumphostWizardModel) enterDockerReview(c dockerdb.Container) (jumphostWizardModel, tea.Cmd) {
	m.dockerContainer = c
	m.options.ResourceType = sessions.DockerResourceType
	m.options.Name = c.Name
	m.options.DBUser = c.User
	m.errorMsg = ""
	m.step = jumphostStepReview
	m.passwordInput.Reset()
	m.passwordInput.SetValue(c.Password)
	m.passwordInput.CursorEnd()
	m.mcpWrites = true
	m = m.focusReviewField(reviewFieldPassword)
	return m, textinput.Blink
}

// dockerReviewFields are the review fields of a Docker database: its password and the MCP
// write toggle; nothing is remembered.
func (m jumphostWizardModel) dockerReviewFields() []int {
	fields := []int{reviewFieldPassword}
	if m.mcpWritesApply() {
		fields = append(fields, reviewFieldMCPWrites)
	}
	return fields
}

// submitDockerReview connects to the container with the password of the review; an empty one
// only for a container that trusts its user.
func (m jumphostWizardModel) submitDockerReview() (tea.Model, tea.Cmd) {
	c := &m.dockerContainer
	if m.passwordInput.Value() == "" && !c.Trust {
		m.errorMsg = "Enter the password of " + c.User
		return m.focusReviewField(reviewFieldPassword), nil
	}
	m.options.ResourceType = sessions.DockerResourceType
	m.options.Name = c.Name
	m.options.DBUser = c.User
	m.options.SignIn = jumphost.SignInPassword
	m.options.DBPassword = m.passwordInput.Value()
	m.options.DBSecretARN = ""
	m.options.RememberPassword = false
	m.options.LocalPortNumber = 0
	return m.connectReviewed()
}

// renderDockerReviewPanel shows the container, read-only, then its password and the MCP write
// toggle. compact keeps only the target row above the fields.
func (m jumphostWizardModel) renderDockerReviewPanel(width int, compact bool) string {
	panelWidth := min(width, reviewPanelWidth)
	// Border (2) and padding (2) around the panel content.
	inner := panelWidth - 4
	c := &m.dockerContainer
	badge := m.styles.ResourceBadge(sessions.DockerResourceType)
	name := m.styles.Text.Bold(true).Render(c.Name)
	var rows []string
	if compact {
		rows = append(rows, m.reviewRow("Target", badge+" "+name, inner))
	} else {
		rows = append(rows,
			m.reviewRow("Type", badge+" "+m.styles.Muted.Render(string(c.Engine)), inner),
			m.reviewRow("Target", name+m.styles.Faint.Render(" · "+c.Image), inner),
			m.reviewRow("Server", c.Address()+m.styles.Faint.Render(" · TLS disable"), inner),
			m.reviewRow("Database", m.dockerDatabaseLabel(), inner),
			m.reviewRow("User", c.User, inner),
			m.styles.Rule.Render(strings.Repeat("─", inner)),
		)
	}
	passwordHelp := "Read from the container's environment: edit it if it changed since."
	mcpHelp := "Space toggles. On, AI assistants write as " + c.User + "."
	if compact {
		passwordHelp, mcpHelp = "", ""
	}
	rows = append(rows, m.reviewInput(reviewFieldPassword, "Password", inputView(m.passwordInput), passwordHelp, inner)...)
	if m.mcpWritesApply() {
		rows = append(rows, m.reviewInput(reviewFieldMCPWrites, "MCP", m.mcpWritesToggle(), mcpHelp, inner)...)
		db := directdb.Database{Engine: c.Engine, User: c.User}
		if !m.mcpWrites && jumphost.DirectReadOnlyWarning(&db) != "" {
			rows = append(rows, "  "+m.styles.Warning.Render(truncate("⚠ Read-only rests on "+c.User+"'s permissions (SQL Server)", inner-2)))
		}
	}
	return m.styles.Card.Width(panelWidth).Render(strings.Join(rows, "\n"))
}

// dockerDatabaseLabel names the database the connection opens: the container's, else the login's default.
func (m jumphostWizardModel) dockerDatabaseLabel() string {
	if m.dockerContainer.Database != "" {
		return m.dockerContainer.Database
	}
	return m.styles.Faint.Render("the login's default")
}

// openDockerAtStart shows the Docker list as a new connection whose Type step is behind, so esc
// leads to it; handleDockerListed then opens the review of openDocker, when set.
func (m jumphostWizardModel) openDockerAtStart() (tea.Model, tea.Cmd) {
	m.dockerAtStart = false
	for idx, option := range m.typeOptions() {
		if option.name == sessions.DockerResourceType {
			m.resourceIndex = idx
		}
	}
	m.flow = flowNew
	m.options = m.base
	m.options.ResourceType = sessions.DockerResourceType
	return m.enterDockerList()
}

// openPendingDocker opens the review of openDocker once the list arrived; an unknown or
// unopenable container stays on the list, with the reason.
func (m jumphostWizardModel) openPendingDocker(listErr error) (tea.Model, tea.Cmd) {
	name, reason := m.openDocker, m.openDockerError
	m.openDocker, m.openDockerError = "", ""
	if listErr != nil {
		return m, nil
	}
	m.dockerList.selectName(name)
	item, ok := m.dockerList.selectedItem()
	switch {
	case !ok || item.c.Name != name:
		m.errorMsg = "no running postgres or sqlserver container named " + name
		return m, nil
	case item.c.Problem != "":
		m.errorMsg = item.c.Problem
		return m, nil
	}
	m, cmd := m.enterDockerReview(item.c)
	if reason != "" {
		// The container's password was just rejected: it is typed again.
		m.passwordInput.Reset()
		m.errorMsg = reason
	}
	return m, cmd
}
