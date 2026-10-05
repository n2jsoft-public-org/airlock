package ui

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// palette is the set of 256-color codes every screen uses, with darker variants for terminals
// with a light background, where the light ones would not be readable.
type palette struct {
	accent, warm, muted, faint, border, text, surface, success, warning, danger color.Color
}

func newPalette(dark bool) palette {
	pick := lipgloss.LightDark(dark)
	return palette{
		accent:  pick(lipgloss.Color("31"), lipgloss.Color("81")),
		warm:    pick(lipgloss.Color("130"), lipgloss.Color("223")),
		muted:   pick(lipgloss.Color("242"), lipgloss.Color("244")),
		faint:   pick(lipgloss.Color("247"), lipgloss.Color("240")),
		border:  pick(lipgloss.Color("250"), lipgloss.Color("238")),
		text:    pick(lipgloss.Color("235"), lipgloss.Color("252")),
		surface: pick(lipgloss.Color("254"), lipgloss.Color("236")),
		success: pick(lipgloss.Color("28"), lipgloss.Color("42")),
		warning: pick(lipgloss.Color("166"), lipgloss.Color("214")),
		danger:  pick(lipgloss.Color("160"), lipgloss.Color("203")),
	}
}

// colorOnBadge is the text of the colored badges and buttons, white on both backgrounds.
var colorOnBadge = lipgloss.Color("231")

// Styles groups shared lipgloss styles used by the TUI.
type Styles struct {
	// Dark tells which palette the styles use; components with their own styles (lists) follow it.
	Dark bool
	// AccentColor, WarningColor and DangerColor are palette colors views apply to borders and labels directly.
	AccentColor, WarningColor, DangerColor color.Color

	Container lipgloss.Style
	Title     lipgloss.Style
	Subtitle  lipgloss.Style
	Muted     lipgloss.Style
	Faint     lipgloss.Style
	Text      lipgloss.Style
	Success   lipgloss.Style
	Warning   lipgloss.Style
	Danger    lipgloss.Style

	// Panel is a rounded card; ModalPanel highlights dialogs.
	Panel      lipgloss.Style
	ModalPanel lipgloss.Style
	PanelTitle lipgloss.Style
	Rule       lipgloss.Style

	// SelectedCard marks the selected session in the sidebar.
	SelectedCard lipgloss.Style
	CardMarker   lipgloss.Style
	FieldLabel   lipgloss.Style

	// Card and CardFocus frame the wizard choices; the focused one gets the accent border.
	Card      lipgloss.Style
	CardFocus lipgloss.Style
	// StepDone, StepCurrent and StepPending render the wizard breadcrumb.
	StepDone    lipgloss.Style
	StepCurrent lipgloss.Style
	StepPending lipgloss.Style

	Badge       lipgloss.Style
	BadgeRDS    color.Color
	BadgeEKS    color.Color
	BadgeCache  color.Color
	BadgeOther  color.Color
	KeyCap      lipgloss.Style
	KeyDesc     lipgloss.Style
	Button      lipgloss.Style
	ButtonFocus lipgloss.Style
}

func NewStyles(dark bool) Styles {
	p := newPalette(dark)
	return Styles{
		Dark: dark, AccentColor: p.accent, WarningColor: p.warning, DangerColor: p.danger,
		Container: lipgloss.NewStyle().
			Padding(1, 2),
		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.accent),
		Subtitle: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.warm),
		Muted: lipgloss.NewStyle().
			Foreground(p.muted),
		Faint: lipgloss.NewStyle().
			Foreground(p.faint),
		Text: lipgloss.NewStyle().
			Foreground(p.text),
		Success: lipgloss.NewStyle().
			Foreground(p.success),
		Warning: lipgloss.NewStyle().
			Foreground(p.warning),
		Danger: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.danger),

		Panel: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(p.border).
			Padding(0, 1),
		ModalPanel: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(p.accent).
			Padding(1, 3),
		PanelTitle: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.accent),
		Rule: lipgloss.NewStyle().
			Foreground(p.border),

		SelectedCard: lipgloss.NewStyle().
			Background(p.surface),
		CardMarker: lipgloss.NewStyle().
			Foreground(p.accent),
		FieldLabel: lipgloss.NewStyle().
			Foreground(p.muted).
			Width(9),

		Card: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(p.border).
			Padding(0, 1),
		CardFocus: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(p.accent).
			Padding(0, 1),
		StepDone: lipgloss.NewStyle().
			Foreground(p.success),
		StepCurrent: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.accent),
		StepPending: lipgloss.NewStyle().
			Foreground(p.faint),

		Badge: lipgloss.NewStyle().
			Bold(true).
			Padding(0, 1).
			Foreground(colorOnBadge),
		BadgeRDS:   lipgloss.Color("25"),
		BadgeEKS:   lipgloss.Color("97"),
		BadgeCache: lipgloss.Color("160"),
		BadgeOther: lipgloss.Color("240"),
		KeyCap: lipgloss.NewStyle().
			Bold(true).
			Padding(0, 1).
			Foreground(p.text).
			Background(p.surface),
		KeyDesc: lipgloss.NewStyle().
			Foreground(p.muted),
		Button: lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(p.muted).
			Background(p.surface),
		ButtonFocus: lipgloss.NewStyle().
			Bold(true).
			Padding(0, 2).
			Foreground(colorOnBadge).
			Background(lipgloss.Color("25")),
	}
}

// ResourceBadge renders the colored badge of a jumphost resource type.
func (s Styles) ResourceBadge(resourceType string) string {
	switch strings.ToLower(resourceType) {
	case "rds":
		return s.Badge.Background(s.BadgeRDS).Render("RDS")
	case "eks":
		return s.Badge.Background(s.BadgeEKS).Render("EKS")
	case "elasticache":
		return s.Badge.Background(s.BadgeCache).Render("CACHE")
	default:
		return s.Badge.Background(s.BadgeOther).Render(strings.ToUpper(resourceType))
	}
}

// ProdBadge flags a production AWS profile.
func (s Styles) ProdBadge() string {
	return s.Badge.Background(lipgloss.Color("166")).Render("PROD")
}

// WritesBadge flags a session whose MCP server lets AI assistants modify data.
func (s Styles) WritesBadge() string {
	return s.Badge.Background(s.DangerColor).Render("WRITES")
}

// AgentBadge flags a session an AI agent opened through the MCP gateway.
func (s Styles) AgentBadge(agent string) string {
	return s.Badge.Background(lipgloss.Color("61")).Render("AGENT · " + agent)
}

// KeyHints renders "key description" pairs as key caps within width, always showing the last one.
func (s Styles) KeyHints(keys [][2]string, width int) string {
	return s.KeyHintsPinned(keys, 1, width)
}

// KeyHintsPinned renders "key description" pairs as key caps within width. When they do not fit,
// the last pinned ones (help, quit) stay and the ones before them give way to "…".
func (s Styles) KeyHintsPinned(keys [][2]string, pinned, width int) string {
	hints := make([]string, 0, len(keys))
	for _, k := range keys {
		hints = append(hints, s.KeyCap.Render(k[0])+" "+s.KeyDesc.Render(k[1]))
	}
	line := strings.Join(hints, "  ")
	if lipgloss.Width(line) <= width || pinned <= 0 || pinned >= len(hints) {
		return truncate(line, width)
	}

	tail := s.Faint.Render("…") + "  " + strings.Join(hints[len(hints)-pinned:], "  ")
	head := hints[:len(hints)-pinned]
	for n := len(head); n > 0; n-- {
		if line = strings.Join(head[:n], "  ") + "  " + tail; lipgloss.Width(line) <= width {
			return line
		}
	}
	return truncate(tail, width)
}

// textInputStyles keeps the look text inputs had with Bubbles v1: dim placeholder and suggestion,
// unstyled prompt and text, a blinking block cursor in the terminal's own color.
func textInputStyles() textinput.Styles {
	state := textinput.StyleState{
		Placeholder: lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		Suggestion:  lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		Prompt:      lipgloss.NewStyle(),
		Text:        lipgloss.NewStyle(),
	}
	return textinput.Styles{Focused: state, Blurred: state, Cursor: textinput.CursorStyle{Shape: tea.CursorBlock, Blink: true}}
}

// inputView renders in as Bubbles v1 did without a width: v2 cuts the placeholder of an input with
// no width to its first character, so an empty one gets a width that just fits its placeholder.
func inputView(in textinput.Model) string {
	if in.Width() == 0 && in.Value() == "" && in.Placeholder != "" {
		in.SetWidth(max(1, lipgloss.Width(in.Placeholder)-1))
	}
	return in.View()
}
