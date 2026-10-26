package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
)

type secretPickerOutcome int

const (
	secretPickerOpen secretPickerOutcome = iota
	secretPickerCanceled
	secretPickerChosen
)

const (
	secretPickerMaxRows  = 10
	secretPickerMaxWidth = 80
)

// secretPicker is the searchable list of Secrets Manager secrets shown as a modal over the review.
type secretPicker struct {
	region  string
	errMsg  string
	all     []jumphost.SecretSummary
	matches []jumphost.SecretSummary
	input   textinput.Model
	cursor  int
	loading bool
}

func newSecretPicker(region string) secretPicker {
	input := textinput.New()
	input.SetStyles(textInputStyles())
	input.Placeholder = i18n.T("ui.secret_picker.search_app_user_prod")
	input.Prompt = "⌕ "
	input.CharLimit = 80
	input.Focus()
	return secretPicker{input: input, region: region, loading: true}
}

// loaded shows the listed secrets, or err.
func (p secretPicker) loaded(secrets []jumphost.SecretSummary, err error) secretPicker {
	p.loading = false
	if err != nil {
		p.errMsg = i18n.Localize(err)
		return p
	}
	p.all = secrets
	p.matches = filterSecrets(secrets, p.input.Value())
	p.cursor = 0
	return p
}

// Update handles one message and reports whether the picker stays open, was canceled or a secret was chosen.
func (p secretPicker) Update(msg tea.Msg) (secretPicker, secretPickerOutcome, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "esc":
			return p, secretPickerCanceled, nil
		case "enter":
			if _, ok := p.choice(); ok {
				return p, secretPickerChosen, nil
			}
			return p, secretPickerOpen, nil
		case "up", "ctrl+p":
			return p.move(-1), secretPickerOpen, nil
		case "down", "ctrl+n":
			return p.move(1), secretPickerOpen, nil
		case "pgup":
			return p.move(-secretPickerMaxRows), secretPickerOpen, nil
		case "pgdown":
			return p.move(secretPickerMaxRows), secretPickerOpen, nil
		}
	}

	before := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != before {
		p.matches = filterSecrets(p.all, p.input.Value())
		p.cursor = 0
	}
	return p, secretPickerOpen, cmd
}

func (p secretPicker) move(delta int) secretPicker {
	if len(p.matches) == 0 {
		return p
	}
	p.cursor = min(max(p.cursor+delta, 0), len(p.matches)-1)
	return p
}

// choice returns the highlighted secret; false while loading, on error or without match.
func (p secretPicker) choice() (jumphost.SecretSummary, bool) {
	if p.loading || p.errMsg != "" || len(p.matches) == 0 {
		return jumphost.SecretSummary{}, false
	}
	return p.matches[p.cursor], true
}

// filterSecrets keeps the secrets whose name or description contains every word of query, in order.
func filterSecrets(secrets []jumphost.SecretSummary, query string) []jumphost.SecretSummary {
	words := strings.Fields(strings.ToLower(query))
	matches := make([]jumphost.SecretSummary, 0, len(secrets))
	for _, secret := range secrets {
		haystack := strings.ToLower(secret.Name + " " + secret.Description)
		keep := true
		for _, word := range words {
			if !strings.Contains(haystack, word) {
				keep = false
				break
			}
		}
		if keep {
			matches = append(matches, secret)
		}
	}
	return matches
}

// View renders the modal centered in a width x height area.
func (p secretPicker) View(styles Styles, width, height int) string {
	panelWidth := min(width, secretPickerMaxWidth)
	inner := max(panelWidth-8, 20)
	rows := min(secretPickerMaxRows, max(height-10, 1))

	title := spread(
		styles.Text.Bold(true).Render(i18n.T("ui.secret_picker.pick_password_secret")),
		styles.Faint.Render(fmt.Sprintf("%d/%d · %s", len(p.matches), len(p.all), p.region)),
		inner,
	)
	p.input.SetWidth(max(inner-lipgloss.Width(p.input.Prompt)-1, 5))

	lines := []string{title, "", inputView(p.input), styles.Rule.Render(strings.Repeat("─", inner))}
	lines = append(lines, p.renderRows(styles, inner, rows)...)
	lines = append(lines, "", styles.KeyHints([][2]string{{"↑↓", i18n.T("ui.hint.move")}, {"enter", i18n.T("ui.hint.pick")}, {"esc", i18n.T("ui.hint.close")}}, inner))

	panel := styles.ModalPanel.Width(inner + 8).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, panel)
}

func (p secretPicker) renderRows(styles Styles, width, rows int) []string {
	var out []string
	switch {
	case p.loading:
		out = []string{styles.Muted.Render(i18n.T("ui.secret_picker.loading_secrets"))}
	case p.errMsg != "":
		out = []string{styles.Warning.Render(truncate(p.errMsg, width))}
	case len(p.matches) == 0:
		out = []string{styles.Muted.Render(i18n.T("ui.secret_picker.no_secret_matches"))}
	default:
		first := min(max(p.cursor-rows/2, 0), max(len(p.matches)-rows, 0))
		last := min(first+rows, len(p.matches))
		for idx := first; idx < last; idx++ {
			out = append(out, p.renderRow(styles, p.matches[idx], idx == p.cursor, width))
		}
	}
	for len(out) < rows {
		out = append(out, "")
	}
	return out
}

func (p secretPicker) renderRow(styles Styles, secret jumphost.SecretSummary, selected bool, width int) string {
	marker := "  "
	if selected {
		marker = styles.CardMarker.Render("▌ ")
	}
	badge := "  "
	if secret.Linked {
		badge = styles.Success.Render("● ")
	}
	name := styles.Text.Render(secret.Name)
	if selected {
		name = styles.Text.Bold(true).Render(secret.Name)
	}
	detail := secret.Description
	if secret.Linked {
		detail = strings.TrimSpace(i18n.T("ui.secret_picker.linked") + detail)
	}
	row := truncate(marker+badge+name+"  "+styles.Muted.Render(detail), width)
	if selected {
		row = styles.SelectedCard.Width(width).Render(row)
	}
	return row
}
