package ui

import "github.com/charmbracelet/lipgloss"

// Palette shared by every screen (256-color codes).
const (
	colorAccent  = lipgloss.Color("81")
	colorWarm    = lipgloss.Color("223")
	colorMuted   = lipgloss.Color("244")
	colorFaint   = lipgloss.Color("240")
	colorBorder  = lipgloss.Color("238")
	colorText    = lipgloss.Color("252")
	colorSurface = lipgloss.Color("236")
	colorSuccess = lipgloss.Color("42")
	colorWarning = lipgloss.Color("214")
	colorDanger  = lipgloss.Color("203")
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
