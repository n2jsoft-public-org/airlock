package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
	m.viewport.Width = maxInt(1, l.mainWidth-4)
	m.viewport.Height = maxInt(minLogHeight, m.logPanelHeight(l)-3)

	if s := m.selectedSession(); s != nil {
		if m.queriesShown(s) {
			m.viewport.SetContent(m.renderQueryLines(s, m.viewport.Width))
		} else {
			m.viewport.SetContent(m.renderLogLines(s, m.viewport.Width))
		}
	} else {
		m.viewport.SetContent("")
	}
}

func (m ssmModel) View() string {
	switch m.mode {
	case ssmModeWizard:
		return m.wizard.View()
	case ssmModePreparing:
		keys := m.styles.KeyHints([][2]string{{"esc", "cancel"}, {"ctrl+c", "quit"}}, maxInt(m.width-4, 20))
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
		left += m.styles.Faint.Render("  SSM port forwarding")
	}

	var counts []string
	if n := m.countSessions(func(s *ssmSession) bool { return s.state == ssmSessionRunning && s.ready }); n > 0 {
		counts = append(counts, m.styles.Success.Render(fmt.Sprintf("● %d ready", n)))
	}
	if n := m.countSessions(func(s *ssmSession) bool {
		return (s.active() && (s.state != ssmSessionRunning || !s.ready)) || s.reconnectPending()
	}); n > 0 {
		counts = append(counts, m.styles.Warning.Render(fmt.Sprintf("%s %d pending", m.spinner(), n)))
	}
	if n := m.countSessions(func(s *ssmSession) bool { return !s.active() && !s.reconnectPending() }); n > 0 {
		counts = append(counts, m.styles.Muted.Render(fmt.Sprintf("○ %d ended", n)))
	}
	right := strings.Join(counts, m.styles.Faint.Render("  ·  "))

	return spread(left, right, width) + "\n" + m.styles.Rule.Render(strings.Repeat("─", width))
}

func (m ssmModel) renderSidebar(l ssmLayout) string {
	inner := l.sidebarWidth - 4
	innerHeight := l.bodyHeight - 2

	lines := []string{m.styles.PanelTitle.Render("Sessions") + m.styles.Faint.Render(fmt.Sprintf("  %d", len(m.sessions))), ""}
	newHint := m.styles.Faint.Render("+ new connection") + "  " + m.styles.KeyCap.Render("n")

	// Show a window of cards that keeps the selected one visible.
	const cardHeight = 4
	capacity := maxInt(1, (innerHeight-len(lines)-2)/cardHeight)
	first := 0
	if m.selected >= capacity {
		first = m.selected - capacity + 1
	}
	last := min(len(m.sessions), first+capacity)

	if first > 0 {
		lines = append(lines, m.styles.Faint.Render(fmt.Sprintf("  ↑ %d more", first)))
	}
	for idx := first; idx < last; idx++ {
		lines = append(lines, m.renderCard(m.sessions[idx], idx == m.selected, inner)...)
		lines = append(lines, "")
	}
	if last < len(m.sessions) {
		lines = append(lines, m.styles.Faint.Render(fmt.Sprintf("  ↓ %d more", len(m.sessions)-last)))
	}

	for len(lines) < innerHeight-1 {
		lines = append(lines, "")
	}
	lines = append(lines, newHint)

	return m.styles.Panel.Width(l.sidebarWidth - 2).Height(innerHeight).Render(strings.Join(lines, "\n"))
}

func (m ssmModel) renderCard(s *ssmSession, selected bool, width int) []string {
	icon, label, style := m.status(s)
	contentWidth := width - 2

	badges := m.badges(s)
	rows := []string{
		badges + " " + m.styles.Text.Bold(true).Render(truncate(s.session.Target, contentWidth-lipgloss.Width(badges)-1)),
		spread(style.Render(icon+" "+label), m.styles.Text.Render(fmt.Sprintf("localhost:%d", s.session.LocalPort)), contentWidth),
		m.styles.Muted.Render(truncate(fmt.Sprintf("%s · %s · %s", s.session.Profile, s.session.Region, formatUptime(s)), contentWidth)),
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
	for idx, s := range m.sessions {
		icon, _, style := m.status(s)
		pill := fmt.Sprintf("%s %s :%d", style.Render(icon), s.session.Target, s.session.LocalPort)
		if m.production.matches(s.session.Profile) {
			pill += " " + m.styles.Warning.Bold(true).Render("PROD")
		}
		if s.mcpWrites() {
			pill += " " + m.styles.Danger.Render("WRITES")
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
	default:
		parts = append(parts, m.renderLogs(l.mainWidth, logHeight))
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m ssmModel) renderDetails(width int) string {
	inner := width - 4
	s := m.selectedSession()
	if s == nil {
		empty := m.styles.Text.Bold(true).Render("No active session") + "\n" +
			m.styles.Muted.Render("Press ") + m.styles.KeyCap.Render("n") + m.styles.Muted.Render(" to open a connection.")
		return m.styles.Panel.Width(width - 2).Render(empty)
	}

	icon, label, style := m.status(s)
	title := spread(
		m.badges(s)+" "+m.styles.Text.Bold(true).Render(s.session.Target),
		style.Render(icon+" "+label),
		inner,
	)

	remote := strings.TrimSpace(s.session.Details.ConnectedServer)
	if remote == "" {
		remote = "n/a"
	}
	uptime := formatUptime(s)
	if s.connections > 0 {
		uptime += m.styles.Muted.Render("  ·  " + plural(s.connections, "connection"))
	}

	rows := []string{
		title,
		"",
		m.field("Local", m.styles.Title.Render(fmt.Sprintf("localhost:%d", s.session.LocalPort)), inner),
		m.field("Remote", remote, inner),
		m.field("AWS", s.session.Profile+m.styles.Faint.Render(" · ")+s.session.Region, inner),
		m.field("Uptime", uptime, inner),
	}
	if auth := strings.TrimSpace(s.session.Details.Auth); auth != "" {
		rows = append(rows, m.field("Auth", auth, inner))
	}
	if ide := strings.TrimSpace(s.session.Details.IDEDataSource); ide != "" {
		rows = append(rows, m.field("IDE", ide, inner))
	}
	if mcp := strings.TrimSpace(s.session.Details.MCP); mcp != "" {
		rows = append(rows, m.field("MCP", mcp, inner))
	}
	if kubeconfig := strings.TrimSpace(s.session.Details.Kubeconfig); kubeconfig != "" {
		rows = append(rows, m.field("Kube", "KUBECONFIG="+shortenHome(kubeconfig), inner))
	}
	if s.err != nil && !s.stopRequested {
		rows = append(rows, m.field("Error", m.styles.Danger.Render(s.err.Error()), inner))
	}

	panel := m.styles.Panel
	if m.production.matches(s.session.Profile) {
		panel = panel.BorderForeground(colorWarning)
	}
	return panel.Width(width - 2).Render(strings.Join(rows, "\n"))
}

func (m ssmModel) renderLogs(width, height int) string {
	title := m.styles.PanelTitle.Render("Logs")
	if s := m.selectedSession(); s != nil && s.mcp != nil {
		// Both panels are named as tabs, the active one highlighted.
		logs, queries := m.styles.PanelTitle, m.styles.Faint
		if m.showQueries {
			logs, queries = m.styles.Faint, m.styles.PanelTitle
		}
		title = logs.Render("Logs") + "   " + queries.Render("MCP queries") + m.styles.Faint.Render(fmt.Sprintf(" %d", s.mcp.total))
	}
	follow := m.styles.Success.Render("● live")
	if !m.viewport.AtBottom() {
		follow = m.styles.Warning.Render(fmt.Sprintf("❚❚ paused %d%%", int(m.viewport.ScrollPercent()*100))) + m.styles.Faint.Render("  G follow")
	}
	header := spread(title, follow, width-4)

	content := header + "\n" + m.viewport.View()
	return m.styles.Panel.Width(width - 2).Height(height - 2).Render(content)
}

func (m ssmModel) renderLogLines(s *ssmSession, width int) string {
	textWidth := maxInt(10, width-logTimeWidth)
	indent := strings.Repeat(" ", logTimeWidth)

	var out []string
	for _, line := range s.lines {
		if strings.TrimSpace(line.text) == "" {
			out = append(out, "")
			continue
		}
		wrapped := strings.Split(lipgloss.NewStyle().Width(textWidth).Render(line.text), "\n")
		style := m.logStyle(line.text)
		for idx, part := range wrapped {
			prefix := indent
			if idx == 0 {
				prefix = m.styles.Faint.Render(line.at.Format("15:04:05")) + "  "
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

func (m ssmModel) renderDialog(width int) string {
	var title, body, confirm string
	switch m.mode {
	case ssmModeConfirmQuit:
		active := m.countSessions(func(s *ssmSession) bool { return s.active() })
		title = "Quit airlock?"
		body = fmt.Sprintf("This stops %s and frees their local ports.", plural(active, "active session"))
		if active == 1 {
			body = "This stops 1 active session and frees its local port."
		}
		confirm = "Stop all & quit"
	default:
		s := m.selectedSession()
		if s == nil {
			return ""
		}
		title = "Stop this session?"
		body = m.badge(s.session.ResourceType) + " " + s.session.Target + m.styles.Muted.Render(fmt.Sprintf(" on localhost:%d", s.session.LocalPort))
		if cmd := strings.TrimSpace(s.session.Details.RestartCommand); cmd != "" {
			body += "\n\n" + m.styles.Faint.Render("Reconnect later with:") + "\n" + m.styles.Muted.Render(cmd)
		}
		confirm = "Stop session"
	}

	yes, no := m.styles.Button, m.styles.ButtonFocus
	if m.confirmYes {
		yes, no = m.styles.ButtonFocus, m.styles.Button
	}
	buttons := yes.Render(confirm) + "  " + no.Render("Cancel")

	dialogWidth := min(width-4, maxInt(lipgloss.Width(buttons), 56))
	content := m.styles.Text.Bold(true).Render(title) + "\n\n" +
		lipgloss.NewStyle().Width(dialogWidth-8).Render(body) + "\n\n" +
		buttons
	return m.styles.ModalPanel.Width(dialogWidth - 2).Render(content)
}

func (m ssmModel) renderFooter(width int) string {
	var lines []string
	if m.errorMsg != "" {
		lines = append(lines, lipgloss.NewStyle().Width(width).Render(m.styles.Danger.Render("✗ "+m.errorMsg)))
	}
	if m.notice != "" {
		lines = append(lines, lipgloss.NewStyle().Width(width).Render(m.styles.Muted.Render(m.notice)))
	}
	if m.quitting && m.hasActiveSessions() {
		lines = append(lines, m.styles.Warning.Render(m.spinner()+" Stopping sessions...")+m.styles.Faint.Render("  ctrl+c again to force"))
	}

	var keys [][2]string
	switch m.mode {
	case ssmModeConfirmQuit:
		keys = [][2]string{{"y", "quit"}, {"n", "cancel"}, {"←→", "choose"}, {"enter", "select"}, {"ctrl+c", "quit"}}
	case ssmModeConfirmStop:
		keys = [][2]string{{"y", "stop"}, {"n", "cancel"}, {"←→", "choose"}, {"enter", "select"}}
	case ssmModeHelp:
		keys = [][2]string{{"esc", "close"}}
	case ssmModeCopy:
		keys = [][2]string{{"1-9", "copy"}, {"↑↓", "move"}, {"enter", "copy"}, {"esc", "close"}}
	default:
		keys = [][2]string{{"n", "new"}}
		switch s := m.selectedSession(); {
		case s == nil:
		case s.active():
			keys = append(keys, [2]string{"x", "stop"})
		default:
			keys = append(keys, [2]string{"r", "restart"}, [2]string{"x", "close"})
		}
		if s := m.selectedSession(); s != nil {
			keys = append(keys, [2]string{"c", "copy"})
		}
		keys = append(keys, [2]string{"tab", "switch"}, [2]string{"1-9", "jump"}, [2]string{"↑↓", "scroll"})
		if s := m.selectedSession(); s != nil && s.mcp != nil {
			panel := "queries"
			if m.showQueries {
				panel = "logs"
			}
			keys = append(keys, [2]string{"m", panel})
		}
		keys = append(keys, [2]string{"?", "help"}, [2]string{"q", "quit"})
	}
	lines = append(lines, m.styles.KeyHintsPinned(keys, 2, width))
	return strings.Join(lines, "\n")
}

// helpGroups lists every key of the dashboard for its help.
func (m ssmModel) helpGroups() []helpGroup {
	return []helpGroup{
		{title: "Sessions", keys: [][2]string{
			{"n", "Open a new connection"},
			{"r", "Restart an ended session"},
			{"x", "Stop the session, or close it once ended"},
			{"c", "Copy its address, a command or the last MCP query"},
			{"tab ←→", "Switch session"},
			{"1-9", "Jump to a session"},
		}},
		{title: "Logs", keys: [][2]string{
			{"↑↓ pgup pgdn", "Scroll, pausing the live tail"},
			{"g home", "Start of the logs"},
			{"G end", "Back to the live tail"},
			{"m", "Logs or MCP queries (database sessions)"},
		}},
		{title: "airlock", keys: [][2]string{
			{"?", "This help"},
			{"q ctrl+c", "Stop all sessions and quit"},
		}},
	}
}

// status returns the icon, label and style describing the session state.
func (m ssmModel) status(s *ssmSession) (icon, label string, style lipgloss.Style) {
	switch s.state {
	case ssmSessionRunning:
		if s.ready {
			return "●", "Ready", m.styles.Success
		}
		return m.spinner(), "Connecting", m.styles.Warning
	case ssmSessionStarting:
		return m.spinner(), "Starting", m.styles.Warning
	case ssmSessionStopping:
		return m.spinner(), "Stopping", m.styles.Warning
	default:
		switch {
		case s.reconnecting:
			return m.spinner(), reconnectStatus(s), m.styles.Warning
		case s.reconnectPending():
			return "↻", reconnectStatus(s), m.styles.Warning
		case s.stopRequested:
			return "○", "Stopped", m.styles.Muted
		case s.err != nil:
			return "✗", "Failed", m.styles.Danger
		default:
			return "○", "Closed", m.styles.Muted
		}
	}
}

func (m ssmModel) badge(resourceType string) string {
	return m.styles.ResourceBadge(resourceType)
}

// badges renders the resource type of s, flagged when its MCP server may modify data.
func (m ssmModel) badges(s *ssmSession) string {
	badges := m.badge(s.session.ResourceType)
	if m.production.matches(s.session.Profile) {
		badges += " " + m.styles.ProdBadge()
	}
	if s.mcpWrites() {
		badges += " " + m.styles.WritesBadge()
	}
	return badges
}

func (m ssmModel) field(label, value string, width int) string {
	return m.styles.FieldLabel.Render(label) + truncate(value, maxInt(1, width-m.styles.FieldLabel.GetWidth()))
}

func (m ssmModel) spinner() string {
	return spinnerFrames[m.frame%len(spinnerFrames)]
}

func (m ssmModel) countSessions(match func(*ssmSession) bool) int {
	count := 0
	for _, s := range m.sessions {
		if match(s) {
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

func formatUptime(s *ssmSession) string {
	end := s.endedAt
	if end.IsZero() {
		end = time.Now()
	}
	d := max(end.Sub(s.startedAt).Round(time.Second), 0)

	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}
