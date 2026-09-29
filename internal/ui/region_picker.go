package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type regionPickerOutcome int

const (
	regionPickerOpen regionPickerOutcome = iota
	regionPickerCanceled
	regionPickerChosen
)

const (
	regionPickerMaxRows  = 10
	regionPickerMaxWidth = 60
	regionCodeWidth      = len("ap-southeast-7") + 3
)

// regionPicker is the searchable AWS region switcher shown as a modal over the wizard.
type regionPicker struct {
	matches []awsRegion
	current string
	input   textinput.Model
	cursor  int
}

func newRegionPicker(current string) regionPicker {
	input := textinput.New()
	input.Placeholder = "Search: paris, eu-west, tokyo..."
	input.Prompt = "⌕ "
	input.CharLimit = 40
	input.Focus()

	p := regionPicker{input: input, current: current, matches: filterAWSRegions(awsRegions, "")}
	for idx, region := range p.matches {
		if region.code == current {
			p.cursor = idx
		}
	}
	return p
}

// Update handles one message and reports whether the picker stays open, was canceled or a region was chosen.
func (p regionPicker) Update(msg tea.Msg) (regionPicker, regionPickerOutcome, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc":
			return p, regionPickerCanceled, nil
		case "enter":
			if _, ok := p.choice(); ok {
				return p, regionPickerChosen, nil
			}
			return p, regionPickerOpen, nil
		case "up", "ctrl+p":
			return p.move(-1), regionPickerOpen, nil
		case "down", "ctrl+n":
			return p.move(1), regionPickerOpen, nil
		case "pgup":
			return p.move(-regionPickerMaxRows), regionPickerOpen, nil
		case "pgdown":
			return p.move(regionPickerMaxRows), regionPickerOpen, nil
		}
	}

	before := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != before {
		p.matches = filterAWSRegions(awsRegions, p.input.Value())
		p.cursor = 0
	}
	return p, regionPickerOpen, cmd
}

func (p regionPicker) move(delta int) regionPicker {
	if len(p.matches) == 0 {
		return p
	}
	p.cursor = min(max(p.cursor+delta, 0), len(p.matches)-1)
	return p
}

// choice returns the highlighted region code, or the typed query when it is a region code missing from the list.
func (p regionPicker) choice() (string, bool) {
	if len(p.matches) > 0 {
		return p.matches[p.cursor].code, true
	}
	query := strings.ToLower(strings.TrimSpace(p.input.Value()))
	if looksLikeRegionCode(query) {
		return query, true
	}
	return "", false
}

// View renders the modal centered in a width x height area.
func (p regionPicker) View(styles Styles, width, height int) string {
	// Modal border (2) and padding (6) around the content.
	panelWidth := min(width, regionPickerMaxWidth)
	inner := maxInt(panelWidth-8, 20)
	// Modal border and padding (4), title, search, rule, footer and gaps (6).
	rows := min(regionPickerMaxRows, maxInt(height-10, 1))

	title := spread(
		styles.Text.Bold(true).Render("Switch AWS region"),
		styles.Faint.Render(fmt.Sprintf("%d/%d", len(p.matches), len(awsRegions))),
		inner,
	)
	p.input.Width = maxInt(inner-lipgloss.Width(p.input.Prompt)-1, 5)

	lines := []string{title, "", p.input.View(), styles.Rule.Render(strings.Repeat("─", inner))}
	lines = append(lines, p.renderRows(styles, inner, rows)...)
	lines = append(lines, "", styles.KeyHints([][2]string{{"↑↓", "move"}, {"enter", "switch"}, {"esc", "close"}}, inner))

	panel := styles.ModalPanel.Width(inner + 6).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, panel)
}

func (p regionPicker) renderRows(styles Styles, width, rows int) []string {
	if len(p.matches) == 0 {
		out := []string{styles.Muted.Render("No region matches.")}
		if code, ok := p.choice(); ok {
			out = append(out, styles.Faint.Render("enter uses "+code+" as typed"))
		}
		for len(out) < rows {
			out = append(out, "")
		}
		return out
	}

	// Keep the cursor inside a window of rows entries.
	first := min(max(p.cursor-rows/2, 0), maxInt(len(p.matches)-rows, 0))
	last := min(first+rows, len(p.matches))

	out := make([]string, 0, rows)
	for idx := first; idx < last; idx++ {
		region := p.matches[idx]
		selected := idx == p.cursor

		marker := "  "
		if selected {
			marker = styles.CardMarker.Render("▌ ")
		}
		current := "  "
		if region.code == p.current {
			current = styles.Success.Render("● ")
		}
		code := styles.Text.Render(fmt.Sprintf("%-*s", regionCodeWidth, region.code))
		if selected {
			code = styles.Text.Bold(true).Render(fmt.Sprintf("%-*s", regionCodeWidth, region.code))
		}
		row := truncate(marker+current+code+styles.Muted.Render(region.name), width)
		if selected {
			row = styles.SelectedCard.Width(width).Render(row)
		}
		out = append(out, row)
	}
	for len(out) < rows {
		out = append(out, "")
	}
	return out
}
