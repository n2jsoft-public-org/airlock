package ui

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/i18n"
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

	// Dark tells which palette the styles use; components with their own styles (lists) follow it.
	Dark bool
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
			Width(labelWidth(9, 1, reviewLabelKeys)),

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
	case "dynamodb":
		return s.Badge.Background(s.BadgeOther).Render("DYNAMODB")
	case "docker", "docker-cache":
		return s.Badge.Background(s.BadgeOther).Render("DOCKER")
	default:
		return s.Badge.Background(s.BadgeOther).Render(strings.ToUpper(resourceType))
	}
}

// Badge colors of the environments: production keeps the PROD orange whatever its name; the other
// configured environments take the palette in config order (never orange or red, which flag
// production and writes, nor the resource and agent badge colors); a name the setting lacks is gray.
var (
	colorProductionBadge    = lipgloss.Color("166")
	colorUnknownEnvironment = lipgloss.Color("240")
	environmentPalette      = []string{"30", "71", "67", "132", "101", "37"}
)

// envColor is the color of env in its badge and its pill: production's orange, the palette color
// of a configured environment, gray for a name the setting lacks.
func envColor(env environment.Env) color.Color {
	switch {
	case env.Production:
		return colorProductionBadge
	case env.Configured:
		return lipgloss.Color(environmentPalette[env.Index%len(environmentPalette)])
	default:
		return colorUnknownEnvironment
	}
}

// EnvBadge renders the badge of env, its name upper-cased; "" for no environment.
func (s Styles) EnvBadge(env environment.Env) string {
	if env.IsZero() {
		return ""
	}
	return s.Badge.Background(envColor(env)).Render(strings.ToUpper(env.Name))
}

// EnvPillStyle is the bold text style of env in the compact pills, its badge color; production
// keeps the Warning style.
func (s Styles) EnvPillStyle(env environment.Env) lipgloss.Style {
	if env.Production {
		return s.Warning.Bold(true)
	}
	return s.Text.Bold(true).Foreground(envColor(env))
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

// inputView renders the placeholder of an empty input as Bubbles v1 did. Without a width, v2 cuts
// it to its first character, so the input gets a width that just fits it; with a width, v2 cuts a
// placeholder too long mid-word, so it is shortened with "…" to the width v1 gave it.
func inputView(in textinput.Model) string {
	if in.Value() != "" || in.Placeholder == "" {
		return in.View()
	}
	if in.Width() == 0 {
		in.SetWidth(max(1, lipgloss.Width(in.Placeholder)-1))
	} else if limit := in.Width() - lipgloss.Width(in.Prompt); limit > 0 && lipgloss.Width(in.Placeholder) > limit {
		in.Placeholder = ansi.Truncate(in.Placeholder, limit, "…")
	}
	return in.View()
}

// reviewLabelKeys are the labels FieldLabel renders in the review panels.
var reviewLabelKeys = []string{
	"ui.jumphost_flow_view.type", "ui.jumphost_flow_view.target", "ui.jumphost_flow_view.aws",
	"ui.direct_review.server", "ui.direct_form.database", "ui.jumphost_flow_view.port",
	"ui.jumphost_flow_view.sign_in", "ui.jumphost_flow_view.user", "ui.jumphost_flow_view.password",
	"ui.jumphost_flow_view.remember", "ui.jumphost_flow_view.secret", "ui.jumphost_flow_view.mcp",
}

// labelWidth is a label column fitting the longest of keys in the current language plus gap
// spaces, at least minWidth (what English needs).
func labelWidth(minWidth, gap int, keys []string) int {
	for _, key := range keys {
		minWidth = max(minWidth, lipgloss.Width(i18n.T(key))+gap)
	}
	return minWidth
}
