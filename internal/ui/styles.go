package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Palette shared by every screen (256-color codes), with darker variants for terminals with a
// light background, where the light ones would not be readable.
var (
	colorAccent  = lipgloss.AdaptiveColor{Dark: "81", Light: "31"}
	colorWarm    = lipgloss.AdaptiveColor{Dark: "223", Light: "130"}
	colorMuted   = lipgloss.AdaptiveColor{Dark: "244", Light: "242"}
	colorFaint   = lipgloss.AdaptiveColor{Dark: "240", Light: "247"}
	colorBorder  = lipgloss.AdaptiveColor{Dark: "238", Light: "250"}
	colorText    = lipgloss.AdaptiveColor{Dark: "252", Light: "235"}
	colorSurface = lipgloss.AdaptiveColor{Dark: "236", Light: "254"}
	colorSuccess = lipgloss.AdaptiveColor{Dark: "42", Light: "28"}
	colorWarning = lipgloss.AdaptiveColor{Dark: "214", Light: "166"}
	colorDanger  = lipgloss.AdaptiveColor{Dark: "203", Light: "160"}
	// colorOnBadge is the text of the colored badges and buttons, white on both backgrounds.
	colorOnBadge = lipgloss.Color("231")
)

// Styles groups shared lipgloss styles used by the TUI.
type Styles struct {
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
	BadgeRDS    lipgloss.Color
	BadgeEKS    lipgloss.Color
	BadgeCache  lipgloss.Color
	BadgeOther  lipgloss.Color
	KeyCap      lipgloss.Style
	KeyDesc     lipgloss.Style
	Button      lipgloss.Style
	ButtonFocus lipgloss.Style
}

func NewStyles() Styles {
	return Styles{
		Container: lipgloss.NewStyle().
			Padding(1, 2),
		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent),
		Subtitle: lipgloss.NewStyle().
			Bold(true).
			Foreground(colorWarm),
		Muted: lipgloss.NewStyle().
			Foreground(colorMuted),
		Faint: lipgloss.NewStyle().
			Foreground(colorFaint),
		Text: lipgloss.NewStyle().
			Foreground(colorText),
		Success: lipgloss.NewStyle().
			Foreground(colorSuccess),
		Warning: lipgloss.NewStyle().
			Foreground(colorWarning),
		Danger: lipgloss.NewStyle().
			Bold(true).
			Foreground(colorDanger),

		Panel: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1),
		ModalPanel: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorAccent).
			Padding(1, 3),
		PanelTitle: lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent),
		Rule: lipgloss.NewStyle().
			Foreground(colorBorder),

		SelectedCard: lipgloss.NewStyle().
			Background(colorSurface),
		CardMarker: lipgloss.NewStyle().
			Foreground(colorAccent),
		FieldLabel: lipgloss.NewStyle().
			Foreground(colorMuted).
			Width(9),

		Card: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1),
		CardFocus: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorAccent).
			Padding(0, 1),
		StepDone: lipgloss.NewStyle().
			Foreground(colorSuccess),
		StepCurrent: lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent),
		StepPending: lipgloss.NewStyle().
			Foreground(colorFaint),

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
			Foreground(colorText).
			Background(colorSurface),
		KeyDesc: lipgloss.NewStyle().
			Foreground(colorMuted),
		Button: lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(colorMuted).
			Background(colorSurface),
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
	return s.Badge.Background(colorDanger).Render("WRITES")
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
