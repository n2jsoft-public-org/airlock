package ui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// copyItem is one value the copy menu puts on the clipboard.
type copyItem struct {
	label string
	value string
}

// ssmCopiedMsg reports a copy to the clipboard.
type ssmCopiedMsg struct {
	err   error
	label string
}

// copyItems lists what the copy menu offers for s, always in the same order so numbers stay stable.
func copyItems(s *ssmSession) []copyItem {
	details := s.session.Details
	items := []copyItem{{label: "Local address", value: fmt.Sprintf("localhost:%d", s.session.LocalPort)}}
	add := func(label, value string) {
		if value = strings.TrimSpace(value); value != "" {
			items = append(items, copyItem{label: label, value: value})
		}
	}
	add("IAM token command", details.TokenCommand)
	add("Reconnect command", details.RestartCommand)
	if kubeconfig := strings.TrimSpace(details.Kubeconfig); kubeconfig != "" {
		add("KUBECONFIG export", "export KUBECONFIG="+kubeconfig)
	}
	if s.mcp != nil && len(s.mcp.events) > 0 {
		add("Last MCP query", s.mcp.events[len(s.mcp.events)-1].SQL)
	}
	return items
}

// openCopyMenu shows what can be copied from the selected session.
func (m ssmModel) openCopyMenu() (tea.Model, tea.Cmd) {
	if m.selectedSession() == nil {
		return m, nil
	}
	m.mode = ssmModeCopy
	m.copyCursor = 0
	return m, nil
}

// handleCopyKey moves in the copy menu and copies the chosen value, picked by number or with enter.
func (m ssmModel) handleCopyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.selectedSession()
	if s == nil {
		m.mode = ssmModeSessions
		return m, nil
	}
	items := copyItems(s)
	switch key := msg.String(); key {
	case "up", "k", "shift+tab":
		m.copyCursor = max(m.copyCursor-1, 0)
	case "down", "j", "tab":
		m.copyCursor = min(m.copyCursor+1, len(items)-1)
	case "enter":
		return m.copyValue(items[m.copyCursor])
	case "esc", "c", "q":
		m.mode = ssmModeSessions
		m.refreshViewport()
	case "ctrl+c":
		m.mode = ssmModeSessions
		return m.handleSessionKey(msg)
	default:
		if index, err := strconv.Atoi(key); err == nil && index >= 1 && index <= len(items) {
			return m.copyValue(items[index-1])
		}
	}
	return m, nil
}

// copyValue closes the menu and copies item in the background.
func (m ssmModel) copyValue(item copyItem) (tea.Model, tea.Cmd) {
	m.mode = ssmModeSessions
	m.refreshViewport()
	copier := m.copier
	return m, func() tea.Msg {
		return ssmCopiedMsg{label: item.label, err: copier(item.value)}
	}
}

// handleCopied confirms a copy, or shows why it failed.
func (m ssmModel) handleCopied(msg ssmCopiedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.errorMsg = "Could not copy " + msg.label + ": " + msg.err.Error()
	} else {
		m.notice = "Copied " + msg.label
	}
	m.refreshViewport()
	return m, nil
}

// renderCopyMenu draws the copy menu of the selected session as a dialog.
func (m ssmModel) renderCopyMenu(width int) string {
	s := m.selectedSession()
	if s == nil {
		return ""
	}
	items := copyItems(s)
	dialogWidth := min(width-4, 90)
	// Modal border (2) and padding (6) around the rows.
	inner := maxInt(dialogWidth-8, 20)
	labelWidth := 0
	for _, item := range items {
		labelWidth = max(labelWidth, lipgloss.Width(item.label))
	}

	rows := []string{m.styles.Text.Bold(true).Render("Copy to the clipboard"), ""}
	for idx, item := range items {
		marker := "  "
		label := m.styles.Text.Render(item.label + strings.Repeat(" ", labelWidth-lipgloss.Width(item.label)))
		if idx == m.copyCursor {
			marker = m.styles.CardMarker.Render("▌ ")
			label = m.styles.Text.Bold(true).Render(item.label + strings.Repeat(" ", labelWidth-lipgloss.Width(item.label)))
		}
		row := marker + m.styles.Faint.Render(strconv.Itoa(idx+1)) + "  " + label + "  " + m.styles.Muted.Render(item.value)
		rows = append(rows, truncate(row, inner))
	}
	return m.styles.ModalPanel.Width(dialogWidth - 2).Render(strings.Join(rows, "\n"))
}
