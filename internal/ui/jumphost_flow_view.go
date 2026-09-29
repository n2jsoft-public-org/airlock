package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	resourceCardHeight = 4
	resourceCardWidth  = 72
	reviewPanelWidth   = 72
)

func (m jumphostWizardModel) View() string {
	bodyWidth, bodyHeight := wizardBodySize(m.width, m.height)
	frame := wizardFrame{
		title:    "airlock",
		tagline:  "SSM port forwarding",
		chips:    m.contextChips(),
		errorMsg: m.errorMsg,
	}
	frame.steps, frame.current = m.breadcrumb()

	switch {
	case m.loading:
		frame.heading = "Working on it"
		frame.body = m.loadingUI.Body(m.styles)
		frame.keys = [][2]string{{"q", m.quitLabel()}}
	case m.pickingRegion:
		frame.heading = m.stepHeading()
		frame.body = m.regionPicker.View(m.styles, bodyWidth, bodyHeight)
		frame.keys = [][2]string{{"ctrl+c", m.quitLabel()}}
	default:
		frame.heading = m.stepHeading()
		frame.hint = m.stepHint()
		frame.body = m.stepBody(bodyWidth, bodyHeight)
		frame.keys = m.stepKeys()
	}

	return m.styles.renderWizardFrame(frame, m.width, m.height)
}

func (m jumphostWizardModel) contextChips() []string {
	region := m.styles.contextChip("region", "…") + m.styles.Faint.Render(" resolving")
	if code := m.currentRegion(); code != "" {
		region = m.styles.regionChip(code)
	} else if m.regionResolved {
		region = m.styles.contextChip("region", "none") + m.styles.Faint.Render(" · press r")
	}
	return []string{m.styles.contextChip("profile", m.options.AwsProfile), region}
}

// breadcrumb lists the steps of the current flow and the index of the current one; the start
// screen has none.
func (m jumphostWizardModel) breadcrumb() (steps []string, current int) {
	if m.flow == flowStart {
		return nil, 0
	}
	for idx, step := range m.flowSteps() {
		steps = append(steps, m.stepLabel(step))
		if step == m.step {
			current = idx
		}
	}
	return steps, current
}

func (m jumphostWizardModel) stepLabel(step jumphostStep) string {
	switch step {
	case jumphostStepProfile:
		return "Profile"
	case jumphostStepType:
		return "Type"
	case jumphostStepTarget:
		return "Target"
	case jumphostStepReview:
		return "Connect"
	default:
		return ""
	}
}

// canGoBack reports whether esc leads somewhere from the current step.
func (m jumphostWizardModel) canGoBack() bool {
	_, ok := m.previousStep(m.step)
	return ok
}

func (m jumphostWizardModel) stepHeading() string {
	switch m.step {
	case jumphostStepProfile:
		return "Choose an AWS profile"
	case jumphostStepType:
		return "What do you want to reach?"
	case jumphostStepTarget:
		if m.flow == flowSearch {
			return "Search a target"
		}
		return fmt.Sprintf("Choose the %s target", strings.ToUpper(m.options.ResourceType))
	case jumphostStepReview:
		return "Review and connect"
	default:
		return ""
	}
}

func (m jumphostWizardModel) stepHint() string {
	switch m.step {
	case jumphostStepProfile:
		return plural(len(m.profileList.Items()), "profile")
	case jumphostStepTarget:
		hint := plural(len(m.targetList.Items()), "target") + " in " + m.currentRegion()
		if m.searchWarnings != "" {
			hint += " · ⚠ " + m.searchWarnings
		}
		return hint
	case jumphostStepReview:
		return "edit any field, then connect"
	default:
		return ""
	}
}

func (m jumphostWizardModel) stepBody(width, height int) string {
	switch m.step {
	case jumphostStepProfile:
		return m.profileList.View()
	case jumphostStepType:
		return m.renderResourceCards(width, height)
	case jumphostStepTarget:
		return m.targetList.View()
	case jumphostStepReview:
		return m.renderReview(width, height)
	default:
		return ""
	}
}

func (m jumphostWizardModel) stepKeys() [][2]string {
	quit := [2]string{"q", m.quitLabel()}
	switch m.step {
	case jumphostStepProfile:
		if m.profileList.SettingFilter() {
			return [][2]string{{"enter", "select"}, {"esc", "clear filter"}}
		}
		keys := [][2]string{{"↑↓", "move"}, {"/", "filter"}, {"enter", "select"}}
		if m.canGoBack() {
			keys = append(keys, [2]string{"esc", "back"})
		}
		return append(keys, quit)
	case jumphostStepType:
		keys := [][2]string{{"↑↓", "move"}, {"1-3", "pick"}, {"enter", "select"}, {"r", "region"}}
		if m.canGoBack() {
			keys = append(keys, [2]string{"esc", "back"})
		}
		return append(keys, quit)
	case jumphostStepTarget:
		if m.targetList.SettingFilter() {
			return [][2]string{{"enter", "select"}, {"esc", "clear filter"}}
		}
		return [][2]string{{"↑↓", "move"}, {"/", "filter"}, {"enter", "select"}, {"r", "region"}, {"esc", "back"}, quit}
	case jumphostStepReview:
		keys := [][2]string{{"enter", "connect"}}
		if len(m.reviewFields()) > 1 {
			keys = append(keys, [2]string{"tab", "next field"})
		}
		if m.reviewFocus == reviewFieldPasswordless || m.reviewFocus == reviewFieldMCPWrites {
			keys = append(keys, [2]string{"space", "toggle"})
		}
		return append(keys, [2]string{"esc", "back"}, [2]string{"ctrl+c", m.quitLabel()})
	default:
		return [][2]string{quit}
	}
}

// renderResourceCards draws one bordered card per resource type, or single lines when height is short.
func (m jumphostWizardModel) renderResourceCards(width, height int) string {
	compact := height < len(resourceTypeOptions)*resourceCardHeight
	cardWidth := min(width, resourceCardWidth)

	rows := make([]string, 0, len(resourceTypeOptions))
	for idx, option := range resourceTypeOptions {
		selected := idx == m.resourceIndex
		title := m.styles.Text.Render(option.title)
		if selected {
			title = m.styles.Text.Bold(true).Render(option.title)
		}
		badge := m.styles.ResourceBadge(option.name)

		if compact {
			marker := "  "
			if selected {
				marker = m.styles.CardMarker.Render("▌ ")
			}
			rows = append(rows, truncate(marker+badge+" "+title, width))
			continue
		}

		// Border (2) and padding (2) around the card content.
		inner := cardWidth - 4
		header := spread(badge+"  "+title, m.styles.Faint.Render(strconv.Itoa(idx+1)), inner)
		desc := m.styles.Muted.Render(truncate(option.desc, inner))
		style := m.styles.Card
		if selected {
			style = m.styles.CardFocus
		}
		rows = append(rows, style.Width(cardWidth-2).Render(header+"\n"+desc))
	}
	return strings.Join(rows, "\n")
}

// renderReview shows the chosen connection with its editable fields and the connect button.
// Short terminals get a compact card: the header already shows profile and region.
func (m jumphostWizardModel) renderReview(width, height int) string {
	panel := m.renderReviewPanel(width, false)
	if lipgloss.Height(panel) > height {
		return m.renderReviewPanel(width, true)
	}

	button := m.styles.ButtonFocus.Render("Connect  ⏎")
	if lipgloss.Height(panel)+2 > height {
		return panel
	}
	return panel + "\n\n" + button
}

func (m jumphostWizardModel) renderReviewPanel(width int, compact bool) string {
	panelWidth := min(width, reviewPanelWidth)
	// Border (2) and padding (2) around the panel content.
	inner := panelWidth - 4

	var rows []string
	if compact {
		rows = append(rows, m.reviewRow("Target", m.styles.ResourceBadge(m.options.ResourceType)+" "+m.styles.Text.Bold(true).Render(m.options.Name), inner))
	} else {
		region := m.currentRegion()
		if name := awsRegionName(region); name != "" {
			region += m.styles.Faint.Render(" · " + name)
		}
		rows = append(rows,
			m.reviewRow("Type", m.styles.ResourceBadge(m.options.ResourceType), inner),
			m.reviewRow("Target", m.styles.Text.Bold(true).Render(m.options.Name), inner),
			m.reviewRow("AWS", m.options.AwsProfile+m.styles.Faint.Render(" · ")+region, inner),
			m.styles.Rule.Render(strings.Repeat("─", inner)),
		)
	}

	portHelp := "Ports already used by sessions or local apps are skipped."
	toggleHelp := "Space toggles. On, airlock generates AWS IAM tokens: nothing to type."
	userHelp := "Signs in with AWS IAM."
	if def := m.runner.ResolveDBUser(""); def != "" {
		userHelp = "Signs in with AWS IAM. Leave empty for " + def + "."
	}
	passwordHelp := "Kept in memory only, for the MCP server. The IDE asks for it."
	mcpHelp := "Space toggles. On, AI assistants write as the user above."
	if !m.targetIAMAuth {
		mcpHelp = "Space toggles. On, SQL Server uses the master user secret."
	}
	if !m.passwordless {
		userHelp = "The database user the password belongs to."
	}
	if compact {
		portHelp, toggleHelp, userHelp, passwordHelp, mcpHelp = "", "", "", "", ""
	}
	rows = append(rows, m.reviewInput(reviewFieldPort, "Port", m.portInput.View(), portHelp, inner)...)
	if m.targetIAMAuth {
		rows = append(rows, m.reviewInput(reviewFieldPasswordless, "IAM auth", m.passwordlessToggle(), toggleHelp, inner)...)
		rows = append(rows, m.reviewInput(reviewFieldUser, "User", m.userInput.View(), userHelp, inner)...)
		if !m.passwordless {
			rows = append(rows, m.reviewInput(reviewFieldPassword, "Password", m.passwordInput.View(), passwordHelp, inner)...)
		}
	}
	if m.mcpApplies() {
		rows = append(rows, m.reviewInput(reviewFieldMCPWrites, "MCP", m.mcpWritesToggle(), mcpHelp, inner)...)
	}

	return m.styles.Card.Width(panelWidth - 2).Render(strings.Join(rows, "\n"))
}

// passwordlessToggle renders the state of AWS IAM (passwordless) sign-in.
func (m jumphostWizardModel) passwordlessToggle() string {
	if m.passwordless {
		return m.styles.Success.Render("● on") + m.styles.Faint.Render("  password generated by airlock")
	}
	return m.styles.Muted.Render("○ off") + m.styles.Faint.Render("  sign in with a password")
}

// mcpWritesToggle renders whether the MCP server may modify data.
func (m jumphostWizardModel) mcpWritesToggle() string {
	if m.mcpWrites {
		return m.styles.Warning.Render("● writes allowed") + m.styles.Faint.Render("  AI assistants can modify data")
	}
	return m.styles.Muted.Render("○ read-only") + m.styles.Faint.Render("  AI assistants can only query")
}

func (m jumphostWizardModel) reviewRow(label, value string, width int) string {
	return "  " + m.styles.FieldLabel.Render(label) + truncate(value, maxInt(1, width-2-m.styles.FieldLabel.GetWidth()))
}

// reviewInput renders an editable field; the focused one gets the accent marker and its help line, if any.
func (m jumphostWizardModel) reviewInput(field int, label, input, help string, width int) []string {
	if field != m.reviewFocus {
		return []string{"  " + m.styles.FieldLabel.Render(label) + input}
	}
	rows := []string{m.styles.CardMarker.Render("▌ ") + m.styles.FieldLabel.Foreground(colorAccent).Render(label) + input}
	if help != "" {
		rows = append(rows, "  "+m.styles.Faint.Render(truncate(help, width-2)))
	}
	return rows
}

func (m jumphostWizardModel) quitLabel() string {
	if m.embedded {
		return "cancel"
	}
	return "quit"
}

func plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", count, noun)
}
