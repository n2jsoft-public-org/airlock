package ui

import "github.com/charmbracelet/lipgloss"

// Styles groups shared lipgloss styles used by the TUI.
type Styles struct {
	Container lipgloss.Style
	Title     lipgloss.Style
	Subtitle  lipgloss.Style
	Muted     lipgloss.Style
}

func NewStyles() Styles {
	return Styles{
		Container: lipgloss.NewStyle().
			Padding(1, 2),
		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("81")),
		Subtitle: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("223")),
		Muted: lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")),
	}
}
