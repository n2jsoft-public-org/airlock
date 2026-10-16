package ui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/n2jsoft/airlock/internal/i18n"
)

// choiceEditor picks one of the options of a setting, each with its one-line explanation.
type choiceEditor struct {
	view   SettingView
	cursor int
}

// newChoiceEditor opens on the current value, the first option when it is none of them.
func newChoiceEditor(view SettingView) valueEditor {
	cursor := slices.IndexFunc(view.Options, func(option SettingOption) bool { return option.Value == view.Value })
	return choiceEditor{view: view, cursor: max(cursor, 0)}
}

func (c choiceEditor) Update(msg tea.Msg) (valueEditor, editorOutcome, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return c, editorStay, nil
	}
	count := max(len(c.view.Options), 1)
	switch key.String() {
	case "up", "k":
		c.cursor = (c.cursor - 1 + count) % count
	case "down", "j":
		c.cursor = (c.cursor + 1) % count
	case "esc":
		return c, editorCancel, nil
	case "enter":
		if !c.view.Locked && len(c.view.Options) > 0 {
			return c, editorSave, nil
		}
	}
	return c, editorStay, nil
}

func (c choiceEditor) View(styles Styles, width, _ int) string {
	lines := []string{truncate(styles.Text.Bold(true).Render(c.view.Label), width), ""}
	for idx, option := range c.view.Options {
		marker, label := "  ", styles.Text.Render(option.Label)
		if idx == c.cursor {
			marker, label = styles.CardMarker.Render("▌ "), styles.Text.Bold(true).Render(option.Label)
		}
		lines = append(lines, truncate(marker+label, width))
		if option.Help != "" {
			lines = append(lines, truncate("  "+styles.Muted.Render(option.Help), width))
		}
	}
	return strings.Join(lines, "\n")
}

func (c choiceEditor) Value() string {
	if c.cursor >= len(c.view.Options) {
		return c.view.Value
	}
	return c.view.Options[c.cursor].Value
}

func (c choiceEditor) Keys() [][2]string {
	if c.view.Locked {
		return [][2]string{{"↑↓", i18n.T("ui.hint.move")}, {"esc", i18n.T("ui.hint.back")}}
	}
	return [][2]string{{"↑↓", i18n.T("ui.hint.move")}, {"enter", i18n.T("ui.hint.pick")}, {"esc", i18n.T("ui.hint.cancel")}}
}
