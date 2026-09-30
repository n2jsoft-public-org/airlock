package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	// Used until the first WindowSizeMsg arrives.
	fallbackWidth  = 80
	fallbackHeight = 24
	// wizardChromeHeight counts the frame lines around the body: padding (2), header and rule (2),
	// breadcrumb (1), heading and its gaps (3), error (1) and key hints (1).
	wizardChromeHeight = 10
	// wizardChromeWidth is the horizontal padding of the frame.
	wizardChromeWidth = 4
)

// wizardFrame describes one full-screen wizard page. Every page shares the same layout so the
// flow feels like a single screen whose body changes.
type wizardFrame struct {
	title    string
	tagline  string
	heading  string
	hint     string
	body     string
	errorMsg string
	// chips are pre-rendered context values shown at the right of the header (profile, region).
	chips []string
	steps []string
	keys  [][2]string
	// current is the index of the active step in steps.
	current int
}

// frameBodySize returns the size left to the body of a frame showing errorMsg, which takes up
// to maxErrorLines lines.
func (s Styles) frameBodySize(width, height int, errorMsg string) (bodyWidth, bodyHeight int) {
	bodyWidth, bodyHeight = wizardBodySize(width, height)
	if errorMsg != "" {
		bodyHeight = maxInt(bodyHeight-len(s.errorLines(errorMsg, bodyWidth))+1, 1)
	}
	return bodyWidth, bodyHeight
}

// wizardBodySize returns the width and height available to a frame body in a terminal of the
// given size, with a one-line error.
func wizardBodySize(width, height int) (bodyWidth, bodyHeight int) {
	width, height = frameSize(width, height)
	return maxInt(width-wizardChromeWidth, 20), maxInt(height-wizardChromeHeight, 3)
}

func frameSize(width, height int) (frameWidth, frameHeight int) {
	if width <= 0 {
		width = fallbackWidth
	}
	if height <= 0 {
		height = fallbackHeight
	}
	return width, height
}

// renderWizardFrame lays out f to fill exactly width x height cells.
func (s Styles) renderWizardFrame(f wizardFrame, width, height int) string {
	width, height = frameSize(width, height)
	inner, bodyHeight := s.frameBodySize(width, height, f.errorMsg)

	right := strings.Join(f.chips, s.Faint.Render("   "))
	left := s.Title.Render("◆ " + f.title)
	// The tagline is the first thing to go when the context chips need the room.
	if tagline := s.Faint.Render("  " + f.tagline); f.tagline != "" && lipgloss.Width(left+tagline)+lipgloss.Width(right)+2 <= inner {
		left += tagline
	}
	right = truncate(right, maxInt(0, inner-lipgloss.Width(left)-2))

	heading := s.Text.Bold(true).Render(f.heading)
	if f.hint != "" {
		heading += s.Faint.Render("  " + f.hint)
	}

	errorLines := []string{""}
	if f.errorMsg != "" {
		errorLines = s.errorLines(f.errorMsg, inner)
	}

	body := lipgloss.NewStyle().MaxWidth(inner).Height(bodyHeight).MaxHeight(bodyHeight).Render(f.body)

	lines := []string{
		spread(left, right, inner),
		s.Rule.Render(strings.Repeat("─", inner)),
		truncate(s.breadcrumb(f.steps, f.current), inner),
		"",
		truncate(heading, inner),
		"",
		body,
		strings.Join(errorLines, "\n"),
		s.KeyHintsPinned(f.keys, 2, inner),
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(strings.Join(lines, "\n"))
}

// maxErrorLines caps the lines of a frame error; a longer one is cut with "…".
const maxErrorLines = 4

// errorLines wraps msg under a "✗" marker in at most maxErrorLines lines of width cells.
func (s Styles) errorLines(msg string, width int) []string {
	wrapped := wrapText(msg, maxInt(width-2, 1))
	if len(wrapped) > maxErrorLines {
		wrapped = wrapped[:maxErrorLines]
		wrapped[maxErrorLines-1] = ansi.Truncate(wrapped[maxErrorLines-1], maxInt(width-3, 0), "") + "…"
	}
	lines := make([]string, 0, len(wrapped))
	for idx, line := range wrapped {
		prefix := "  "
		if idx == 0 {
			prefix = "✗ "
		}
		lines = append(lines, s.Danger.Render(prefix+line))
	}
	return lines
}

func (s Styles) breadcrumb(steps []string, current int) string {
	parts := make([]string, 0, len(steps))
	for idx, step := range steps {
		switch {
		case idx < current:
			parts = append(parts, s.StepDone.Render("✓ "+step))
		case idx == current:
			parts = append(parts, s.StepCurrent.Render("● "+step))
		default:
			parts = append(parts, s.StepPending.Render("○ "+step))
		}
	}
	return strings.Join(parts, s.Faint.Render("  ›  "))
}

// contextChip renders a labelled header value such as "profile dev".
func (s Styles) contextChip(label, value string) string {
	return s.KeyDesc.Render(label+" ") + s.Text.Bold(true).Render(value)
}

// regionChip renders the region code followed by its friendly name when known.
func (s Styles) regionChip(code string) string {
	chip := s.contextChip("region", code)
	if name := awsRegionName(code); name != "" {
		chip += s.Faint.Render(" · " + name)
	}
	return chip
}

// newWizardList builds a filterable list rendered with cardDelegate. Its own quit keys are disabled:
// the wizard decides when to leave, and an embedded wizard must never quit the host program.
func newWizardList(styles Styles, items []list.Item) list.Model {
	l := list.New(items, cardDelegate{styles: styles}, 0, 0)
	// The title row doubles as the filter input: it hints at "/" until the user starts typing.
	l.Title = "⌕ / to filter"
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	l.DisableQuitKeybindings()
	l.Styles.TitleBar = lipgloss.NewStyle().Padding(0, 0, 1, 0)
	l.Styles.Title = styles.Faint
	l.FilterInput.Prompt = "⌕ "
	l.FilterInput.PromptStyle = styles.Title
	return l
}

// cardDelegate renders list items as two-line cards with an accent marker on the selected one.
type cardDelegate struct {
	styles Styles
}

func (d cardDelegate) Height() int                         { return 2 }
func (d cardDelegate) Spacing() int                        { return 1 }
func (d cardDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d cardDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	entry, ok := item.(list.DefaultItem)
	if !ok {
		return
	}

	width := maxInt(m.Width(), 10)
	selected := index == m.Index()

	marker := "  "
	title := d.styles.Text
	if selected {
		marker = d.styles.CardMarker.Render("▌ ")
		title = d.styles.Text.Bold(true)
	}
	// Items mixing resource types show theirs as a badge before the title, and items of a
	// production profile a PROD badge.
	badge := ""
	if typed, ok := item.(interface{ itemType() string }); ok && typed.itemType() != "" {
		badge = d.styles.ResourceBadge(typed.itemType()) + " "
	}
	if flagged, ok := item.(interface{ isProduction() bool }); ok && flagged.isProduction() {
		badge += d.styles.ProdBadge() + " "
	}

	rows := []string{
		marker + badge + title.Render(truncate(entry.Title(), width-2-lipgloss.Width(badge))),
		marker + d.styles.Muted.Render(truncate(entry.Description(), width-2)),
	}
	if selected {
		for idx, row := range rows {
			rows[idx] = d.styles.SelectedCard.Width(width).Render(row)
		}
	}
	_, _ = fmt.Fprint(w, strings.Join(rows, "\n"))
}
