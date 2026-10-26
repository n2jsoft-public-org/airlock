package ui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/n2jsoft-public-org/airlock/internal/aiclient"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
)

// clientsProjectColumn is the width of the project column: its box and the auto hint.
const clientsProjectColumn = 12

// clientsEditor edits mcp.clients as a grid: Auto-detect, then one row per client with its
// project and user files. The grid is the parsed selection, so every accepted value round-trips.
type clientsEditor struct {
	// detected are the clients found in the project directory; nil when it is unknown.
	detected []aiclient.Client
	sel      aiclient.Selection
	view     SettingView
	// row 0 is Auto-detect, row n the client aiclient.Clients[n-1]; user is the column.
	row  int
	user bool
}

// newClientsEditor opens on the effective selection; one that does not parse opens on the default.
func newClientsEditor(view SettingView, detected []aiclient.Client) valueEditor {
	sel, err := aiclient.Parse(view.Value)
	if err != nil {
		sel = aiclient.AutoDetect
	}
	return clientsEditor{view: view, detected: detected, sel: sel}
}

func (c clientsEditor) Update(msg tea.Msg) (valueEditor, editorOutcome, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return c, editorStay, nil
	}
	count := len(aiclient.Clients) + 1
	switch key.String() {
	case "up", "k":
		c.row = (c.row - 1 + count) % count
	case "down", "j":
		c.row = (c.row + 1) % count
	case "left", "h":
		c.user = false
	case "right", "l":
		c.user = true
	case "esc":
		return c, editorCancel, nil
	case "enter":
		if !c.view.Locked {
			return c, editorSave, nil
		}
	case "space":
		if !c.view.Locked {
			c.toggle()
		}
	case "n":
		if !c.view.Locked {
			c.sel = aiclient.Selection{}
		}
	}
	return c, editorStay, nil
}

// toggle flips the focused cell: Auto-detect, or a target added last or removed.
func (c *clientsEditor) toggle() {
	if c.row == 0 {
		c.sel.Detect = !c.sel.Detect
		return
	}
	target := c.target(c.row)
	// The selection is cloned: the editor before this key shares its targets.
	if idx := slices.Index(c.sel.Targets, target); idx >= 0 {
		c.sel.Targets = slices.Delete(slices.Clone(c.sel.Targets), idx, idx+1)
	} else {
		c.sel.Targets = append(slices.Clone(c.sel.Targets), target)
	}
}

// target is the focused cell of row: its client, in the project or user file.
func (c clientsEditor) target(row int) aiclient.Target {
	scope := aiclient.ScopeProject
	if c.user {
		scope = aiclient.ScopeUser
	}
	return aiclient.Target{Client: aiclient.Clients[row-1], Scope: scope}
}

func (c clientsEditor) View(styles Styles, width, _ int) string {
	// No blank line under the label: the grid, its value and its help fit the pane at 80x24.
	lines := []string{
		truncate(styles.Text.Bold(true).Render(c.view.Label), width),
		truncate(c.marker(styles, 0)+c.box(styles, c.sel.Detect, c.row == 0)+" "+c.label(styles, 0, i18n.T("ui.clients_editor.auto_detect")), width),
	}
	if found := c.foundHere(); found != "" {
		lines = append(lines, truncate("      "+styles.Muted.Render(found), width))
	}
	nameWidth := 0
	for _, client := range aiclient.Clients {
		nameWidth = max(nameWidth, lipgloss.Width(clientName(client)))
	}
	nameWidth += 2
	header := strings.Repeat(" ", 2+nameWidth) + styles.Muted.Render(padRight(i18n.T("ui.clients_editor.project"), clientsProjectColumn)+i18n.T("ui.clients_editor.user"))
	lines = append(lines, truncate(header, width))
	for idx, client := range aiclient.Clients {
		row := idx + 1
		project := c.box(styles, slices.Contains(c.sel.Targets, aiclient.Target{Client: client, Scope: aiclient.ScopeProject}), c.row == row && !c.user)
		if c.sel.Detect && slices.Contains(c.detected, client) {
			project += " " + styles.Muted.Render(aiclient.Auto)
		}
		user := c.box(styles, slices.Contains(c.sel.Targets, aiclient.Target{Client: client, Scope: aiclient.ScopeUser}), c.row == row && c.user)
		line := c.marker(styles, row) + c.label(styles, row, padRight(clientName(client), nameWidth)) + padRight(project, clientsProjectColumn) + user
		lines = append(lines, truncate(line, width))
	}
	lines = append(lines, "",
		truncate(styles.Muted.Render(i18n.T("ui.clients_editor.value"))+"  "+styles.Text.Render(c.Value()), width),
		truncate(styles.Muted.Render(i18n.T("ui.clients_editor.project_help")), width),
		truncate(styles.Muted.Render(i18n.T("ui.clients_editor.user_help")), width))
	return strings.Join(lines, "\n")
}

// foundHere names the clients detected in the project directory; empty when it is unknown.
func (c clientsEditor) foundHere() string {
	switch {
	case c.detected == nil:
		return ""
	case len(c.detected) == 0:
		return i18n.T("ui.clients_editor.none_found")
	}
	names := make([]string, 0, len(c.detected))
	for _, client := range c.detected {
		names = append(names, clientName(client))
	}
	return i18n.Format("ui.clients_editor.found", strings.Join(names, ", "))
}

func (c clientsEditor) marker(styles Styles, row int) string {
	if row == c.row {
		return styles.CardMarker.Render("▌ ")
	}
	return "  "
}

func (c clientsEditor) label(styles Styles, row int, text string) string {
	if row == c.row {
		return styles.Text.Bold(true).Render(text)
	}
	return styles.Text.Render(text)
}

// box is a checkbox; the focused one takes the accent.
func (c clientsEditor) box(styles Styles, checked, focused bool) string {
	text, style := "[ ]", styles.Faint
	if checked {
		text, style = "[x]", styles.Success
	}
	if focused {
		style = styles.CardMarker.Bold(true)
	}
	return style.Render(text)
}

// Value is the selection as config.Set stores it.
func (c clientsEditor) Value() string { return c.sel.String() }

func (c clientsEditor) Keys() [][2]string {
	if c.view.Locked {
		return [][2]string{{"↑↓←→", i18n.T("ui.hint.move")}, {"esc", i18n.T("ui.hint.back")}}
	}
	// The arrows are left out to fit 80 columns, and save comes before none, which gives way first.
	return [][2]string{
		{"space", i18n.T("ui.hint.toggle")}, {"enter", i18n.T("ui.hint.save")}, {"n", i18n.T("ui.clients_editor.none_hint")},
		{"esc", i18n.T("ui.hint.cancel")},
	}
}

// clientName is the readable name of client.
func clientName(client aiclient.Client) string {
	return i18n.T("ui.clients_editor.client." + string(client))
}

// clientsSummary is a selection in plain words: auto first, (user) after a user file, none.
func clientsSummary(value string) string {
	sel, err := aiclient.Parse(value)
	if err != nil {
		return value
	}
	parts := make([]string, 0, len(sel.Targets)+1)
	if sel.Detect {
		parts = append(parts, aiclient.Auto)
	}
	for _, target := range sel.Targets {
		if target.Scope == aiclient.ScopeUser {
			parts = append(parts, i18n.Format("ui.clients_editor.user_target", string(target.Client)))
		} else {
			parts = append(parts, string(target.Client))
		}
	}
	if len(parts) == 0 {
		return i18n.T("ui.none")
	}
	return strings.Join(parts, ", ")
}
