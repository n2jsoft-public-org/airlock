package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/sessions"
)

const (
	sidebarWidth = 34
	// Below this width the sidebar collapses into a row of pills above the details.
	compactBreakpoint = 90
	logTimeWidth      = len("15:04:05  ")
	minLogHeight      = 3
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// ssmLayout holds the outer sizes (borders included) of the dashboard regions.
type ssmLayout struct {
	sidebarWidth int
	mainWidth    int
	bodyHeight   int
	compact      bool
}

func (m ssmModel) layout() ssmLayout {
	width := maxInt(m.width-2, 20)
	l := ssmLayout{compact: m.width < compactBreakpoint, mainWidth: width}
	if !l.compact {
		l.sidebarWidth = sidebarWidth
		l.mainWidth = width - sidebarWidth - 1
	}
	l.bodyHeight = maxInt(m.height-lipgloss.Height(m.renderHeader(width))-lipgloss.Height(m.renderFooter(width)), 10)
	return l
}

// logPanelHeight is the outer height left for the logs panel (or the dialog replacing it).
func (m ssmModel) logPanelHeight(l ssmLayout) int {
	used := lipgloss.Height(m.renderDetails(l.mainWidth))
	if l.compact {
		used += lipgloss.Height(m.renderPills(l.mainWidth))
	}
	return maxInt(l.bodyHeight-used, minLogHeight+3)
}

func (m *ssmModel) refreshViewport() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	l := m.layout()
	// Border (2) and padding (2) around the log lines; border (2) and title (1) above/below.
	m.viewport.SetWidth(maxInt(1, l.mainWidth-4))
	m.viewport.SetHeight(maxInt(minLogHeight, m.logPanelHeight(l)-3))

	if s := m.selectedSession(); s != nil {
		if m.queriesShown(s) {
			m.viewport.SetContent(m.renderQueryLines(s, m.viewport.Width()))
		} else {
			m.viewport.SetContent(m.renderLogLines(s, m.viewport.Width()))
		}
	} else {
		m.viewport.SetContent("")
	}
}

// View is the dashboard on the alternate screen.
func (m ssmModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m ssmModel) render() string {
	switch m.mode {
	case ssmModeWizard:
		return m.wizard.render()
	case ssmModePreparing:
		keys := m.styles.KeyHints([][2]string{{"esc", i18n.T("ui.hint.cancel")}, {"ctrl+c", i18n.T("ui.hint.quit")}}, maxInt(m.width-4, 20))
		return m.styles.Container.Render(m.styles.Title.Render("airlock") + "\n\n" + m.loadingUI.Body(m.styles) + "\n\n" + keys)
	default:
	}

	if m.width <= 0 || m.height <= 0 {
		return ""
	}

	width := maxInt(m.width-2, 20)
	l := m.layout()

	var body string
	switch {
	case m.mode == ssmModeHelp:
		body = m.styles.renderHelp(m.helpGroups(), width, l.bodyHeight)
	case l.compact:
		body = m.renderMain(l)
	default:
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.renderSidebar(l), " ", m.renderMain(l))
	}

	view := lipgloss.JoinVertical(lipgloss.Left, m.renderHeader(width), body, m.renderFooter(width))
	return lipgloss.NewStyle().Padding(0, 1).Render(view)
}

func (m ssmModel) renderHeader(width int) string {
	left := m.styles.Title.Render("◆ airlock")
	if width >= compactBreakpoint {
		left += m.styles.Faint.Render(i18n.T("ui.ssm_session_view.ssm_port_forwarding"))
	}

	var counts []string
	if n := m.countSessions(func(s *sessions.Session) bool { return s.State == sessions.StateRunning && s.Ready }); n > 0 {
		counts = append(counts, m.styles.Success.Render(i18n.Format("ui.ssm_session_view.ready", n)))
	}
	if n := m.countSessions(func(s *sessions.Session) bool {
		return (s.Active() && (s.State != sessions.StateRunning || !s.Ready)) || s.ReconnectPending()
	}); n > 0 {
		counts = append(counts, m.styles.Warning.Render(i18n.Format("ui.ssm_session_view.pending", m.spinner(), n)))
	}
	if n := m.countSessions(func(s *sessions.Session) bool { return !s.Active() && !s.ReconnectPending() }); n > 0 {
		counts = append(counts, m.styles.Muted.Render(i18n.Format("ui.ssm_session_view.ended", n)))
	}
	right := strings.Join(counts, m.styles.Faint.Render("  ·  "))

	return spread(left, right, width) + "\n" + m.styles.Rule.Render(strings.Repeat("─", width))
}

func (m ssmModel) renderSidebar(l ssmLayout) string {
	inner := l.sidebarWidth - 4
	innerHeight := l.bodyHeight - 2

	lines := []string{m.styles.PanelTitle.Render(i18n.T("ui.ssm_session_view.sessions")) + m.styles.Faint.Render(fmt.Sprintf("  %d", len(m.sessions))), ""}
	newHint := m.styles.Faint.Render(i18n.T("ui.ssm_session_view.new_connection")) + "  " + m.styles.KeyCap.Render("n")

	// Show a window of cards that keeps the selected one visible.
	const cardHeight = 4
	capacity := maxInt(1, (innerHeight-len(lines)-2)/cardHeight)
	first := 0
	if m.selected >= capacity {
		first = m.selected - capacity + 1
	}
	last := min(len(m.sessions), first+capacity)

	if first > 0 {
		lines = append(lines, m.styles.Faint.Render(i18n.Format("ui.ssm_session_view.more", first)))
	}
	for idx := first; idx < last; idx++ {
		lines = append(lines, m.renderCard(&m.sessions[idx], idx == m.selected, inner)...)
		lines = append(lines, "")
	}
	if last < len(m.sessions) {
		lines = append(lines, m.styles.Faint.Render(i18n.Format("ui.ssm_session_view.more_2", len(m.sessions)-last)))
	}

	for len(lines) < innerHeight-1 {
		lines = append(lines, "")
	}
	lines = append(lines, newHint)

	// A line wider than the panel would wrap and push the footer off the screen.
	for idx, line := range lines {
		lines[idx] = truncate(line, inner)
	}

	return m.styles.Panel.Width(l.sidebarWidth).Height(innerHeight + 2).Render(strings.Join(lines, "\n"))
}

func (m ssmModel) renderCard(s *sessions.Session, selected bool, width int) []string {
	icon, label, style := m.status(s)
	contentWidth := width - 2

	badges := m.badges(s)
	status := style.Render(icon + " " + label)
	// A long address (a direct server) gives way to the status rather than wrapping the card.
	address := truncate(sessionAddress(s), maxInt(0, contentWidth-lipgloss.Width(status)-1))
	rows := []string{
		badges + " " + m.styles.Text.Bold(true).Render(truncate(s.Target, contentWidth-lipgloss.Width(badges)-1)),
		spread(status, m.styles.Text.Render(address), contentWidth),
		m.styles.Muted.Render(truncate(sessionOrigin(s)+" · "+formatUptime(s), contentWidth)),
	}

	marker := "  "
	if selected {
		marker = m.styles.CardMarker.Render("▌ ")
	}
	for idx, row := range rows {
		row = marker + row
		if selected {
			row = m.styles.SelectedCard.Width(width).Render(row)
		}
		rows[idx] = row
	}
	return rows
}

func (m ssmModel) renderPills(width int) string {
	pills := make([]string, 0, len(m.sessions))
	for idx := range m.sessions {
		s := &m.sessions[idx]
		icon, _, style := m.status(s)
		pill := style.Render(icon) + " " + s.Target
		if !s.NoTunnel() {
			pill += fmt.Sprintf(" :%d", s.LocalPort)
		}
		if env := m.sessionEnvironment(s); !env.IsZero() {
			pill += " " + m.styles.EnvPillStyle(env).Render(strings.ToUpper(env.Name))
		}
		if s.MCPWrites() {
			pill += " " + m.styles.Danger.Render("WRITES")
		}
		if s.OpenedBy != "" {
			pill += " " + m.styles.Muted.Render("AGENT · "+s.OpenedBy)
		}
		if idx == m.selected {
			pills = append(pills, m.styles.SelectedCard.Padding(0, 1).Bold(true).Render(pill))
		} else {
			pills = append(pills, m.styles.Muted.Padding(0, 1).Render(pill))
		}
	}
	return lipgloss.NewStyle().Width(width).Render(strings.Join(pills, " ")) + "\n"
}

func (m ssmModel) renderMain(l ssmLayout) string {
	var parts []string
	if l.compact {
		parts = append(parts, m.renderPills(l.mainWidth))
	}
	parts = append(parts, m.renderDetails(l.mainWidth))

	logHeight := m.logPanelHeight(l)
	switch m.mode {
	case ssmModeConfirmQuit, ssmModeConfirmStop:
		parts = append(parts, lipgloss.Place(l.mainWidth, logHeight, lipgloss.Center, lipgloss.Center, m.renderDialog(l.mainWidth)))
	case ssmModeCopy:
		parts = append(parts, lipgloss.Place(l.mainWidth, logHeight, lipgloss.Center, lipgloss.Center, m.renderCopyMenu(l.mainWidth)))
	case ssmModePassword:
		parts = append(parts, lipgloss.Place(l.mainWidth, logHeight, lipgloss.Center, lipgloss.Center, m.renderPasswordDialog(l.mainWidth)))
	case ssmModeSignIn:
		parts = append(parts, lipgloss.Place(l.mainWidth, logHeight, lipgloss.Center, lipgloss.Center, m.renderSignInDialog(l.mainWidth)))
	case ssmModeApproval:
		parts = append(parts, lipgloss.Place(l.mainWidth, logHeight, lipgloss.Center, lipgloss.Center, m.renderApprovalDialog(l.mainWidth)))
	default:
		parts = append(parts, m.renderLogs(l.mainWidth, logHeight))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m ssmModel) renderDetails(width int) string {
	inner := width - 4
	s := m.selectedSession()
	if s == nil {
		empty := m.styles.Text.Bold(true).Render(i18n.T("ui.ssm_session_view.no_active_session")) + "\n" +
			m.styles.Muted.Render(i18n.T("ui.ssm_session_view.press")) + m.styles.KeyCap.Render("n") + m.styles.Muted.Render(i18n.T("ui.ssm_session_view.to_open_connection"))
		return m.styles.Panel.Width(width).Render(empty)
	}

	icon, label, style := m.status(s)
	title := spread(
		m.badges(s)+" "+m.styles.Text.Bold(true).Render(s.Target),
		style.Render(icon+" "+label),
		inner,
	)

	remote := strings.TrimSpace(s.Details.ConnectedServer)
	if remote == "" {
		remote = "n/a"
	}
	uptime := formatUptime(s)
	if s.Connections > 0 {
		uptime += m.styles.Muted.Render("  ·  " + i18n.Plural(s.Connections, "ui.count.connection"))
	}

	rows := []string{title, ""}
	if s.NoTunnel() {
		rows = append(rows, m.field(i18n.T("ui.direct_review.server"), m.styles.Title.Render(remote), inner))
		if !s.Direct() {
			rows = append(rows, m.field(i18n.T("ui.jumphost_flow_view.aws"), s.Profile+m.styles.Faint.Render(" · ")+s.Region, inner))
		}
	} else {
		rows = append(rows,
			m.field(i18n.T("ui.ssm_session_view.local"), m.styles.Title.Render(fmt.Sprintf("localhost:%d", s.LocalPort)), inner),
			m.field(i18n.T("ui.ssm_session_view.remote"), remote, inner),
			m.field(i18n.T("ui.jumphost_flow_view.aws"), s.Profile+m.styles.Faint.Render(" · ")+s.Region, inner),
		)
	}
	rows = append(rows, m.field(i18n.T("ui.ssm_session_view.uptime"), uptime, inner))
	if auth := strings.TrimSpace(s.Details.Auth); auth != "" {
		if canRevealPassword(s) {
			auth += "  " + m.styles.KeyCap.Render("p") + m.styles.Faint.Render(i18n.T("ui.ssm_session_view.show_password"))
		}
		rows = append(rows, m.field(i18n.T("ui.ssm_session_view.auth"), auth, inner))
	}
	if status := m.signInStatus(s); status != "" {
		rows = append(rows, m.field(i18n.T("ui.jumphost_flow_view.sign_in"), status, inner))
	}
	if ide := strings.TrimSpace(s.Details.IDEDataSource); ide != "" {
		rows = append(rows, m.field(i18n.T("ui.ssm_session_view.ide"), ide, inner))
	}
	if mcp := strings.TrimSpace(s.Details.MCP); mcp != "" {
		rows = append(rows, m.field(i18n.T("ui.jumphost_flow_view.mcp"), mcp, inner))
	}
	if kubeconfig := strings.TrimSpace(s.Details.Kubeconfig); kubeconfig != "" {
		rows = append(rows, m.field(i18n.T("ui.ssm_session_view.kube"), "KUBECONFIG="+shortenHome(kubeconfig), inner))
	}
	if s.Err != "" && !s.StopRequested {
		rows = append(rows, m.field(i18n.T("ui.ssm_session_view.error"), m.styles.Danger.Render(shownErr(s.LocalErr, s.Err)), inner))
	}

	panel := m.styles.Panel
	if m.isProduction(s) {
		panel = panel.BorderForeground(m.styles.WarningColor)
	}
	return panel.Width(width).Render(strings.Join(rows, "\n"))
}

func (m ssmModel) renderLogs(width, height int) string {
	title := m.styles.PanelTitle.Render(i18n.T("ui.ssm_session_view.logs"))
	if s := m.selectedSession(); s != nil && s.MCP != nil {
		// Both panels are named as tabs, the active one highlighted.
		logs, queries := m.styles.PanelTitle, m.styles.Faint
		if m.showQueries {
			logs, queries = m.styles.Faint, m.styles.PanelTitle
		}
		title = logs.Render(i18n.T("ui.ssm_session_view.logs")) + "   " + queries.Render(i18n.T("ui.ssm_session_view.mcp_calls")) + m.styles.Faint.Render(fmt.Sprintf(" %d", s.MCP.Total))
	}
	follow := m.styles.Success.Render(i18n.T("ui.ssm_session_view.live"))
	if !m.viewport.AtBottom() {
		follow = m.styles.Warning.Render(i18n.Format("ui.ssm_session_view.paused", int(m.viewport.ScrollPercent()*100))) + m.styles.Faint.Render(i18n.T("ui.ssm_session_view.g_follow"))
	}
	header := spread(title, follow, width-4)

	content := header + "\n" + m.viewport.View()
	return m.styles.Panel.Width(width).Height(height).Render(content)
}

func (m ssmModel) renderLogLines(s *sessions.Session, width int) string {
	textWidth := maxInt(10, width-logTimeWidth)
	indent := strings.Repeat(" ", logTimeWidth)

	var out []string
	for _, line := range s.Lines {
		if strings.TrimSpace(line.Text) == "" {
			out = append(out, "")
			continue
		}
		wrapped := strings.Split(lipgloss.NewStyle().Width(textWidth).Render(line.Text), "\n")
		style := m.logStyle(line.Text)
		for idx, part := range wrapped {
			prefix := indent
			if idx == 0 {
				prefix = m.styles.Faint.Render(line.At.Format("15:04:05")) + "  "
			}
			out = append(out, prefix+style.Render(strings.TrimRight(part, " ")))
		}
	}
	return strings.Join(out, "\n")
}

func (m ssmModel) logStyle(text string) lipgloss.Style {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "error"), strings.Contains(lower, "failed"), strings.Contains(lower, "cannot"):
		return m.styles.Danger
	case strings.Contains(lower, "warning"):
		return m.styles.Warning
	case strings.Contains(text, "Waiting for connections"), strings.Contains(text, "Connection accepted"), strings.Contains(text, "opened for sessionId"):
		return m.styles.Success
	case strings.HasPrefix(text, "Starting session"), strings.HasPrefix(text, "Stopping session"), strings.HasPrefix(text, "Session "):
		return m.styles.Muted
	default:
		return m.styles.Text
	}
}

// quitDialogBody says what stopping every active session does: a direct session has no local
// port to free, only the tunnels do.
func (m ssmModel) quitDialogBody() string {
	active := m.countSessions(func(s *sessions.Session) bool { return s.Active() })
	tunnels := m.countSessions(func(s *sessions.Session) bool { return s.Active() && !s.NoTunnel() })
	switch {
	case tunnels == 0:
		return i18n.Format("ui.session.stop_sessions", i18n.Plural(active, "ui.count.active_session"))
	case active == 1:
		return i18n.T("ui.ssm_session_view.this_stops_1_active")
	case tunnels == active:
		return i18n.Format("ui.session.stop_sessions_ports", i18n.Plural(active, "ui.count.active_session"))
	default:
		return i18n.Format("ui.session.stop_sessions_tunnels", i18n.Plural(active, "ui.count.active_session"))
	}
}

func (m ssmModel) renderDialog(width int) string {
	var title, body, confirm string
	switch m.mode {
	case ssmModeConfirmQuit:
		title = i18n.T("ui.ssm_session_view.quit_airlock")
		body = m.quitDialogBody()
		confirm = i18n.T("ui.ssm_session_view.stop_all_quit")
		if m.detach {
			title = i18n.T("ui.ssm_session_view.stop_every_session_and")
		}
	default:
		s := m.selectedSession()
		if s == nil {
			return ""
		}
		title = i18n.T("ui.ssm_session_view.stop_this_session")
		body = m.badge(s.ResourceType) + " " + s.Target + m.styles.Muted.Render(i18n.Format("ui.session.on_address", sessionAddress(s)))
		if cmd := strings.TrimSpace(s.Details.RestartCommand); cmd != "" {
			body += "\n\n" + m.styles.Faint.Render(i18n.T("ui.ssm_session_view.reconnect_later_with")) + "\n" + m.styles.Muted.Render(cmd)
		}
		confirm = i18n.T("ui.ssm_session_view.stop_session")
	}

	yes, no := m.styles.Button, m.styles.ButtonFocus
	if m.confirmYes {
		yes, no = m.styles.ButtonFocus, m.styles.Button
	}
	buttons := yes.Render(confirm) + "  " + no.Render(i18n.T("ui.ssm_session_view.cancel"))

	dialogWidth := min(width-4, maxInt(lipgloss.Width(buttons), 56))
	content := m.styles.Text.Bold(true).Render(title) + "\n\n" +
		lipgloss.NewStyle().Width(dialogWidth-8).Render(body) + "\n\n" +
		buttons
	return m.styles.ModalPanel.Width(dialogWidth).Render(content)
}

func (m ssmModel) renderFooter(width int) string {
	var lines []string
	if m.errorMsg != "" {
		lines = append(lines, lipgloss.NewStyle().Width(width).Render(m.styles.Danger.Render("✗ "+m.errorMsg)))
	}
	if m.notice != "" {
		lines = append(lines, lipgloss.NewStyle().Width(width).Render(m.styles.Muted.Render(m.notice)))
	}
	if m.connLost != "" {
		lines = append(lines, lipgloss.NewStyle().Width(width).Render(m.styles.Danger.Render("✗ "+m.connLost)+m.styles.Faint.Render(i18n.T("ui.ssm_session_view.r_reconnect_quit"))))
	}
	if m.quitting && m.hasActiveSessions() {
		lines = append(lines, m.styles.Warning.Render(m.spinner()+i18n.T("ui.session.stopping_sessions"))+m.styles.Faint.Render(i18n.T("ui.ssm_session_view.ctrl_c_again_to")))
	}

	var keys [][2]string
	switch {
	case m.connLost != "":
		keys = [][2]string{{"r", i18n.T("ui.hint.reconnect")}, {"q", i18n.T("ui.hint.quit")}}
	case m.mode == ssmModeConfirmQuit:
		keys = [][2]string{{"y", i18n.T("ui.hint.quit")}, {"n", i18n.T("ui.hint.cancel")}, {"←→", i18n.T("ui.hint.choose")}, {"enter", i18n.T("ui.hint.select")}, {"ctrl+c", i18n.T("ui.hint.quit")}}
		if m.detach {
			keys[4] = [2]string{"ctrl+c", i18n.T("ui.hint.leave")}
		}
	case m.mode == ssmModeConfirmStop:
		keys = [][2]string{{"y", i18n.T("ui.hint.stop")}, {"n", i18n.T("ui.hint.cancel")}, {"←→", i18n.T("ui.hint.choose")}, {"enter", i18n.T("ui.hint.select")}}
	case m.mode == ssmModeHelp:
		keys = [][2]string{{"esc", i18n.T("ui.hint.close")}}
	case m.mode == ssmModeCopy:
		keys = [][2]string{{"1-9", i18n.T("ui.hint.copy")}, {"↑↓", i18n.T("ui.hint.move")}, {"enter", i18n.T("ui.hint.copy")}, {"esc", i18n.T("ui.hint.close")}}
	case m.mode == ssmModePassword:
		keys = [][2]string{{"c", i18n.T("ui.hint.copy")}, {"esc", i18n.T("ui.hint.hide")}, {"↑↓", i18n.T("ui.hint.move")}, {"←→", i18n.T("ui.hint.choose")}, {"enter", i18n.T("ui.hint.select")}}
	case m.mode == ssmModeSignIn:
		keys = [][2]string{{"1-9", i18n.T("ui.hint.choose")}, {"↑↓", i18n.T("ui.hint.move")}, {"enter", i18n.T("ui.hint.choose")}, {"esc", i18n.T("ui.hint.later")}}
	case m.mode == ssmModeApproval:
		keys = [][2]string{{"y", i18n.T("ui.hint.allow")}, {"n", i18n.T("ui.hint.deny")}, {"←→", i18n.T("ui.hint.choose")}, {"enter", i18n.T("ui.hint.select")}}
	default:
		keys = [][2]string{{"n", i18n.T("ui.hint.new")}}
		switch s := m.selectedSession(); {
		case s == nil:
		case s.Active() && s.AWSSignInExpired:
			keys = append(keys, [2]string{"r", i18n.T("ui.ssm_session_view.sign_in_to_aws")}, [2]string{"x", i18n.T("ui.hint.stop")})
		case s.Active():
			keys = append(keys, [2]string{"x", i18n.T("ui.hint.stop")})
		default:
			keys = append(keys, [2]string{"r", i18n.T("ui.hint.restart")}, [2]string{"x", i18n.T("ui.hint.close")})
		}
		if s := m.selectedSession(); s != nil {
			keys = append(keys, [2]string{"c", i18n.T("ui.hint.copy")})
		}
		if s := m.selectedSession(); canRevealPassword(s) {
			keys = append(keys, [2]string{"p", "password"})
		}
		if s := m.selectedSession(); canRetrySignIn(s) {
			switch s.SignIn.State {
			case sessions.SignInChoosing, sessions.SignInSkipped, sessions.SignInFailed:
				keys = append(keys, [2]string{"a", i18n.T("ui.hint.sign_in")})
			default:
				// Nothing to choose while idle, listing, checking or signed in.
			}
		}
		keys = append(keys, [2]string{"tab", i18n.T("ui.hint.switch")}, [2]string{"1-9", i18n.T("ui.hint.jump")}, [2]string{"↑↓", i18n.T("ui.hint.scroll")})
		if s := m.selectedSession(); s != nil && s.MCP != nil {
			panel := i18n.T("ui.hint.queries")
			if m.showQueries {
				panel = i18n.T("ui.hint.logs")
			}
			keys = append(keys, [2]string{"m", panel})
		}
		if m.detach {
			keys = append(keys, [2]string{"?", i18n.T("ui.hint.help")}, [2]string{"q", i18n.T("ui.hint.leave")}, [2]string{"Q", i18n.T("ui.hint.stop_all")})
		} else {
			keys = append(keys, [2]string{"?", i18n.T("ui.hint.help")}, [2]string{"q", i18n.T("ui.hint.quit")})
		}
	}
	lines = append(lines, m.styles.KeyHintsPinned(keys, 2, width))
	return strings.Join(lines, "\n")
}

// helpGroups lists every key of the dashboard for its help.
func (m ssmModel) helpGroups() []helpGroup {
	quitKeys := [][2]string{{"?", i18n.T("ui.ssm_session_view.this_help")}, {"q ctrl+c", i18n.T("ui.ssm_session_view.stop_all_sessions_and")}}
	if m.detach {
		quitKeys = [][2]string{{"?", i18n.T("ui.ssm_session_view.this_help")}, {"q ctrl+c", i18n.T("ui.ssm_session_view.leave_this_view_sessions")}, {"Q", i18n.T("ui.ssm_session_view.stop_every_session_and_2")}}
	}
	if m.settings != nil {
		quitKeys = append([][2]string{{",", i18n.T("ui.ssm_session_view.settings_change_config_json")}}, quitKeys...)
	}
	return []helpGroup{
		{title: i18n.T("ui.ssm_session_view.sessions"), keys: [][2]string{
			{"n", i18n.T("ui.ssm_session_view.open_new_connection")},
			{"r", i18n.T("ui.ssm_session_view.restart_ended_session_or")},
			{"x", i18n.T("ui.ssm_session_view.stop_session_or_close")},
			{"c", i18n.T("ui.ssm_session_view.copy_its_address_command")},
			{"a p", i18n.T("ui.ssm_session_view.choose_database_credentials_or")},
			{"tab ←→ 1-9", i18n.T("ui.ssm_session_view.switch_session_or_jump")},
		}},
		{title: i18n.T("ui.ssm_session_view.logs"), keys: [][2]string{
			{"↑↓ pgup pgdn", i18n.T("ui.ssm_session_view.scroll_pausing_live_tail")},
			{"g home", i18n.T("ui.ssm_session_view.start_of_logs")},
			{"G end", i18n.T("ui.ssm_session_view.back_to_live_tail")},
			{"m", i18n.T("ui.ssm_session_view.logs_or_mcp_calls")},
		}},
		{title: "airlock", keys: quitKeys},
	}
}

// status returns the icon, label and style describing the session state.
func (m ssmModel) status(s *sessions.Session) (icon, label string, style lipgloss.Style) {
	switch s.State {
	case sessions.StateRunning:
		if s.AWSSignInExpired {
			return "⚠", i18n.T("ui.ssm_session_view.aws_sign_in_expired"), m.styles.Warning
		}
		if s.Ready {
			return "●", i18n.T("ui.ssm_session_view.ready_2"), m.styles.Success
		}
		return m.spinner(), i18n.T("ui.ssm_session_view.connecting"), m.styles.Warning
	case sessions.StateStarting:
		return m.spinner(), i18n.T("ui.ssm_session_view.starting"), m.styles.Warning
	case sessions.StateStopping:
		return m.spinner(), i18n.T("ui.ssm_session_view.stopping"), m.styles.Warning
	default:
		switch {
		case s.Reconnecting:
			return m.spinner(), reconnectStatus(s), m.styles.Warning
		case s.ReconnectPending():
			return "↻", reconnectStatus(s), m.styles.Warning
		case s.StopRequested:
			return "○", i18n.T("ui.ssm_session_view.stopped"), m.styles.Muted
		case s.Err != "":
			return "✗", i18n.T("ui.ssm_session_view.failed"), m.styles.Danger
		default:
			return "○", i18n.T("ui.ssm_session_view.closed"), m.styles.Muted
		}
	}
}

func (m ssmModel) badge(resourceType string) string {
	return m.styles.ResourceBadge(resourceType)
}

// badges renders the resource type of s, flagged when its MCP server may modify data.
func (m ssmModel) badges(s *sessions.Session) string {
	badges := m.badge(s.ResourceType)
	if envBadge := m.styles.EnvBadge(m.sessionEnvironment(s)); envBadge != "" {
		badges += " " + envBadge
	}
	if s.MCPWrites() {
		badges += " " + m.styles.WritesBadge()
	}
	if s.OpenedBy != "" {
		badges += " " + m.styles.AgentBadge(s.OpenedBy)
	}
	return badges
}

func (m ssmModel) field(label, value string, width int) string {
	return m.styles.FieldLabel.Render(label) + truncate(value, maxInt(1, width-m.styles.FieldLabel.GetWidth()))
}

func (m ssmModel) spinner() string {
	return spinnerFrames[m.frame%len(spinnerFrames)]
}

func (m ssmModel) countSessions(match func(*sessions.Session) bool) int {
	count := 0
	for idx := range m.sessions {
		if match(&m.sessions[idx]) {
			count++
		}
	}
	return count
}

// spread places left and right on one line of the given width, truncating left when needed.
func spread(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		left = truncate(left, maxInt(0, width-lipgloss.Width(right)-1))
		right = truncate(right, maxInt(0, width-lipgloss.Width(left)-1))
		gap = maxInt(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	}
	return left + strings.Repeat(" ", gap) + right
}

// shortenHome writes path under the home directory as ~/..., which shells expand back.
func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if rel, err := filepath.Rel(home, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		return "~/" + filepath.ToSlash(rel)
	}
	return path
}

func truncate(value string, width int) string {
	return ansi.Truncate(value, width, "…")
}

func formatUptime(s *sessions.Session) string {
	end := s.EndedAt
	if end.IsZero() {
		end = time.Now()
	}
	d := max(end.Sub(s.StartedAt).Round(time.Second), 0)

	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// address is where tools reach s: the tunnel's local port, or the server of a direct database.
func sessionAddress(s *sessions.Session) string {
	if s.NoTunnel() {
		return s.Details.ConnectedServer
	}
	return fmt.Sprintf("localhost:%d", s.LocalPort)
}

// origin says how s is reached: its AWS profile and region, or "direct".
func sessionOrigin(s *sessions.Session) string {
	if s.Direct() {
		return s.ResourceType
	}
	return s.Profile + " · " + s.Region
}

// shownErr is the error text a person reads: the localized one when the session has it.
func shownErr(local, english string) string {
	if local != "" {
		return local
	}
	return english
}
