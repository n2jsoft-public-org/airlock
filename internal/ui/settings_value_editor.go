package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/n2jsoft/airlock/internal/i18n"
)

// editorOutcome is what the user asked of an open value editor.
type editorOutcome int

const (
	editorStay editorOutcome = iota
	// editorCancel closes the editor without writing.
	editorCancel
	// editorSave writes Value.
	editorSave
)

// valueEditor edits the value of one setting in the right pane of the Settings screen. A locked
// setting opens read-only: it moves but never saves.
type valueEditor interface {
	Update(tea.Msg) (valueEditor, editorOutcome, tea.Cmd)
	View(styles Styles, width, height int) string
	// Value is what a save writes.
	Value() string
	// Keys are the key hints of the editor.
	Keys() [][2]string
}

// organizationOverrideLines warn, in an editor that may save an empty value, that saving it empty
// overrides the value the organization gives.
func organizationOverrideLines(styles Styles, view SettingView, width int) []string {
	if !view.Optional || !view.FromOrganization || view.Locked {
		return nil
	}
	return indentedLines(styles.Warning, i18n.T("ui.settings.empty_overrides_organization"), width)
}
