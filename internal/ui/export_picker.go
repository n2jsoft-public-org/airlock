package ui

import (
	"errors"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/i18n"
)

// ErrExportCanceled is returned by PickExports when the user leaves without exporting.
var ErrExportCanceled = errors.New("not exported")

// ExportChoice is one item PickExports offers.
type ExportChoice struct {
	Name        string
	Detail      string
	Environment environment.Env
	// Unchecked leaves the choice unchecked at start.
	Unchecked bool
}

const exportPickerMaxWidth = 100

// exportPicker is the checklist of the items to export (or import, its verb), checked at start
// unless Unchecked.
type exportPicker struct {
	styles   Styles
	title    string
	verb     string
	choices  []ExportChoice
	checked  []bool
	cursor   int
	width    int
	height   int
	done     bool
	canceled bool
}

func newExportPicker(styles Styles, title, verb string, choices []ExportChoice) exportPicker {
	checked := make([]bool, len(choices))
	for idx := range checked {
		checked[idx] = !choices[idx].Unchecked
	}
	return exportPicker{title: title, verb: verb, choices: choices, checked: checked, styles: styles, width: exportPickerMaxWidth}
}

// PickExports lets the user uncheck the choices not to export (or verb), drawing on out, and returns
// the indexes of the checked ones in order; ErrExportCanceled when the user leaves.
func PickExports(title, verb string, choices []ExportChoice, in, out *os.File) ([]int, error) {
	styles := NewStyles(lipgloss.HasDarkBackground(in, out))
	program := tea.NewProgram(newExportPicker(styles, title, verb, choices), tea.WithInput(in), tea.WithOutput(out))
	final, err := program.Run()
	if err != nil {
		return nil, err
	}
	picker, ok := final.(exportPicker)
	if !ok || !picker.done {
		return nil, ErrExportCanceled
	}
	return picker.selected(), nil
}

func (p exportPicker) Init() tea.Cmd { return nil }

func (p exportPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height = min(msg.Width, exportPickerMaxWidth), msg.Height
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "q", "ctrl+c":
			p.canceled = true
			return p, tea.Quit
		case "enter":
			if len(p.selected()) == 0 {
				return p, nil
			}
			p.done = true
			return p, tea.Quit
		case "up", "k", "ctrl+p":
			p.cursor = max(p.cursor-1, 0)
		case "down", "j", "ctrl+n":
			p.cursor = min(p.cursor+1, max(len(p.choices)-1, 0))
		case "home":
			p.cursor = 0
		case "end":
			p.cursor = max(len(p.choices)-1, 0)
		case "space", "x":
			if len(p.checked) > 0 {
				p.setChecked(func(idx int, on bool) bool { return on != (idx == p.cursor) })
			}
		case "a":
			all := len(p.selected()) != len(p.choices)
			p.setChecked(func(int, bool) bool { return all })
		}
	}
	return p, nil
}

// setChecked sets every check to next of it; checked is copied so that earlier models keep theirs.
func (p *exportPicker) setChecked(next func(idx int, on bool) bool) {
	checked := make([]bool, len(p.checked))
	for idx, on := range p.checked {
		checked[idx] = next(idx, on)
	}
	p.checked = checked
}

// selected are the indexes of the checked choices, in order.
func (p exportPicker) selected() []int {
	indexes := make([]int, 0, len(p.choices))
	for idx, on := range p.checked {
		if on {
			indexes = append(indexes, idx)
		}
	}
	return indexes
}

// View clears the picker once it is left, so that only the export remains on the terminal.
func (p exportPicker) View() tea.View {
	if p.done || p.canceled {
		return tea.NewView("")
	}
	return tea.NewView(p.render())
}

func (p exportPicker) render() string {
	width := max(p.width, 30)
	count := len(p.selected())
	status := p.styles.Faint.Render(i18n.Format("ui.export_picker.selected", count, len(p.choices)))
	lines := []string{spread(p.styles.Title.Render(p.title), status, width), ""}
	lines = append(lines, p.renderRows(width)...)
	lines = append(lines, "")
	if count == 0 {
		lines = append(lines, p.styles.Warning.Render(i18n.Format("ui.export_picker.select_at_least_one", p.verb)))
	}
	hints := [][2]string{{"↑↓", i18n.T("ui.hint.move")}, {"space", i18n.T("ui.hint.toggle")}, {"a", i18n.T("ui.hint.all_none")}, {"enter", p.verb}, {"esc", i18n.T("ui.hint.cancel")}}
	lines = append(lines, p.styles.KeyHintsPinned(hints, 2, width))
	return strings.Join(lines, "\n")
}

// renderRows shows the choices around the cursor, as many as the terminal height leaves room for.
func (p exportPicker) renderRows(width int) []string {
	rows := len(p.choices)
	if p.height > 0 {
		rows = min(rows, max(p.height-6, 3))
	}
	first := min(max(p.cursor-rows/2, 0), max(len(p.choices)-rows, 0))
	last := min(first+rows, len(p.choices))
	nameWidth := 0
	for idx := range p.choices {
		nameWidth = max(nameWidth, lipgloss.Width(p.choices[idx].Name))
	}
	out := make([]string, 0, last-first)
	for idx := first; idx < last; idx++ {
		out = append(out, p.renderRow(idx, nameWidth, width))
	}
	return out
}

func (p exportPicker) renderRow(idx, nameWidth, width int) string {
	choice := p.choices[idx]
	current := idx == p.cursor
	marker := "  "
	if current {
		marker = p.styles.CardMarker.Render("▌ ")
	}
	box := p.styles.Faint.Render("[ ]")
	if p.checked[idx] {
		box = p.styles.Success.Render("[x]")
	}
	name := choice.Name + strings.Repeat(" ", nameWidth-lipgloss.Width(choice.Name))
	if current {
		name = p.styles.Text.Bold(true).Render(name)
	} else {
		name = p.styles.Text.Render(name)
	}
	row := marker + box + " " + name + "  " + p.styles.Muted.Render(choice.Detail)
	if badge := p.styles.EnvBadge(choice.Environment); badge != "" {
		row += " " + badge
	}
	row = truncate(row, width)
	if current {
		row = p.styles.SelectedCard.Width(width).Render(row)
	}
	return row
}
