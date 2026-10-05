package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// helpGroup is a titled list of "key description" pairs of the help overlay.
type helpGroup struct {
	title string
	keys  [][2]string
}

// renderHelp renders every key of groups as a modal centered in width x height, cut to fit.
func (s Styles) renderHelp(groups []helpGroup, width, height int) string {
	keyWidth := 0
	for _, group := range groups {
		for _, k := range group.keys {
			keyWidth = max(keyWidth, lipgloss.Width(k[0]))
		}
	}

	var lines []string
	for idx, group := range groups {
		if idx > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, s.PanelTitle.Render(group.title))
		for _, k := range group.keys {
			key := s.Text.Bold(true).Render(k[0] + strings.Repeat(" ", keyWidth-lipgloss.Width(k[0])))
			lines = append(lines, "  "+key+"   "+s.Muted.Render(k[1]))
		}
	}
	lines = append(lines, "", s.Faint.Render("any key closes this help"))

	// No vertical padding: the help must fit a 24-line terminal.
	panel := s.ModalPanel.Padding(0, 2).MaxWidth(width).Render(strings.Join(lines, "\n"))
	return lipgloss.NewStyle().MaxHeight(height).Render(lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, panel))
}
