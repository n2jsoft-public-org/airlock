package ui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// ssmPasswordReveal is the password dialog of a session reading its credentials from Secrets
// Manager. The passwords live here only while the dialog is open.
type ssmPasswordReveal struct {
	err   error
	creds []jumphost.DBCredentials
	// request identifies the read the dialog waits for; a closed dialog's read carries an older one.
	request int
	// cursor is the credentials Copy copies.
	cursor  int
	loading bool
	// hideFocused puts the focus on Hide instead of Copy.
	hideFocused bool
}

// ssmPasswordMsg carries the credentials read for the dialog's request.
type ssmPasswordMsg struct {
	err     error
	creds   []jumphost.DBCredentials
	request int
}

// canRevealPassword reports whether s reads its credentials from secrets the dashboard can show.
func canRevealPassword(s *sessions.Session) bool {
	return s != nil && s.HasCredentials
}

// revealPassword opens the password dialog of the selected session and reads its secrets.
func (m ssmModel) revealPassword() (tea.Model, tea.Cmd) {
	s := m.selectedSession()
	if s == nil {
		return m, nil
	}
	if !canRevealPassword(s) {
		m.notice = i18n.T("ui.ssm_password.this_session_does_not")
		m.refreshViewport()
		return m, nil
	}
	m.mode = ssmModePassword
	m.password = ssmPasswordReveal{request: m.password.request + 1, loading: true}
	ctx, service, id, request := m.ctx, m.service, s.ID, m.password.request
	return m, func() tea.Msg {
		creds, err := service.Credentials(ctx, id)
		return ssmPasswordMsg{request: request, creds: creds, err: err}
	}
}

// handlePassword shows the credentials read, unless their dialog was closed meanwhile.
func (m ssmModel) handlePassword(msg ssmPasswordMsg) (tea.Model, tea.Cmd) {
	if m.mode != ssmModePassword || msg.request != m.password.request {
		return m, nil
	}
	m.password.loading = false
	m.password.creds, m.password.err = msg.creds, msg.err
	m.password.hideFocused = msg.err != nil || len(msg.creds) == 0
	return m, nil
}

// handlePasswordKey picks, copies or hides the passwords.
func (m ssmModel) handlePasswordKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key := msg.String(); key {
	case "left", "right", "tab", "shift+tab":
		m.password.hideFocused = !m.password.hideFocused
	case "up", "k":
		m.password.cursor = max(m.password.cursor-1, 0)
	case "down", "j":
		m.password.cursor = max(min(m.password.cursor+1, len(m.password.creds)-1), 0)
	case "c", "y":
		return m.copyPassword(m.password.cursor)
	case "enter":
		if m.password.hideFocused {
			return m.hidePassword(), nil
		}
		return m.copyPassword(m.password.cursor)
	case "esc", "p", "q", "n":
		return m.hidePassword(), nil
	case "ctrl+c":
		return m.hidePassword().handleSessionKey(msg)
	default:
		if index, err := strconv.Atoi(key); err == nil && index >= 1 {
			return m.copyPassword(index - 1)
		}
	}
	return m, nil
}

// copyPassword copies the password of the index-th credentials and closes the dialog; nothing
// happens until they are read.
func (m ssmModel) copyPassword(index int) (tea.Model, tea.Cmd) {
	if index < 0 || index >= len(m.password.creds) {
		return m, nil
	}
	creds := m.password.creds[index]
	label := i18n.T("ui.jumphost_flow_view.password")
	if name := creds.Label(); name != "" {
		label = i18n.Format("ui.password.label_of", label, name)
	}
	m = m.hidePassword()
	return m.copyValue(copyItem{label: label, value: creds.Password})
}

// hidePassword closes the dialog and forgets the passwords.
func (m ssmModel) hidePassword() ssmModel {
	m.mode = ssmModeSessions
	m.password = ssmPasswordReveal{request: m.password.request}
	m.refreshViewport()
	return m
}

// renderPasswordDialog draws the credentials of the selected session, or the read in progress.
func (m ssmModel) renderPasswordDialog(width int) string {
	s := m.selectedSession()
	if s == nil {
		return ""
	}
	dialogWidth := min(width-4, 80)
	// Modal border (2) and padding (6) around the rows.
	inner := maxInt(dialogWidth-8, 20)
	creds := m.password.creds

	title := i18n.T("ui.password.title")
	if len(creds) > 1 {
		title = i18n.T("ui.password.title_plural")
	}
	rows := []string{m.styles.Text.Bold(true).Render(title)}
	if auth := strings.TrimSpace(s.Details.Auth); auth != "" {
		rows = append(rows, m.styles.Muted.Render(truncate(auth, inner)))
	}
	rows = append(rows, "")

	copyButton, hideButton := m.styles.ButtonFocus, m.styles.Button
	if m.password.hideFocused {
		copyButton, hideButton = m.styles.Button, m.styles.ButtonFocus
	}
	switch {
	case m.password.loading:
		rows = append(rows, m.styles.Warning.Render(m.spinner()+i18n.T("ui.password.reading_secrets")))
		copyButton = m.styles.Button
	case m.password.err != nil:
		rows = append(rows, lipgloss.NewStyle().Width(inner).Render(m.styles.Danger.Render("✗ "+i18n.Localize(m.password.err))))
	case len(creds) == 0:
		rows = append(rows, m.styles.Muted.Render(i18n.T("ui.ssm_password.no_credentials_found")))
	case len(creds) == 1 && creds[0].Label() == "":
		rows = append(rows, m.passwordLines(creds[0].Password, "", inner)...)
	default:
		for idx, c := range creds {
			marker, name := "  ", m.styles.Text.Render(c.Label())
			if idx == m.password.cursor {
				marker, name = m.styles.CardMarker.Render("▌ "), m.styles.Text.Bold(true).Render(c.Label())
			}
			rows = append(rows, marker+m.styles.Faint.Render(strconv.Itoa(idx+1))+"  "+name+m.styles.Muted.Render(" · "+c.User))
			rows = append(rows, m.passwordLines(c.Password, "     ", inner)...)
		}
	}
	rows = append(rows, "", copyButton.Render(i18n.T("ui.ssm_password.copy"))+"  "+hideButton.Render(i18n.T("ui.ssm_password.hide")))
	return m.styles.ModalPanel.Width(dialogWidth).Render(strings.Join(rows, "\n"))
}

// passwordLines renders password after indent, wrapped rather than truncated: a partial password
// is useless.
func (m ssmModel) passwordLines(password, indent string, width int) []string {
	lines := strings.Split(lipgloss.NewStyle().Width(maxInt(width-len(indent), 10)).Render(password), "\n")
	for idx, line := range lines {
		lines[idx] = indent + m.styles.Title.Render(strings.TrimRight(line, " "))
	}
	return lines
}
