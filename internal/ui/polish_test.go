package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

func TestPaletteAdaptsToLightTerminals(t *testing.T) {
	dark, light := NewStyles(true), NewStyles(false)
	foregrounds := map[string][2]lipgloss.Style{
		"Text": {dark.Text, light.Text}, "Muted": {dark.Muted, light.Muted}, "Faint": {dark.Faint, light.Faint},
		"Title": {dark.Title, light.Title}, "Subtitle": {dark.Subtitle, light.Subtitle}, "Success": {dark.Success, light.Success},
		"Warning": {dark.Warning, light.Warning}, "Danger": {dark.Danger, light.Danger}, "Rule": {dark.Rule, light.Rule},
	}
	for name, pair := range foregrounds {
		if d, l := pair[0].GetForeground(), pair[1].GetForeground(); d == nil || l == nil || d == l {
			t.Errorf("%s foreground = %v (dark) and %v (light), want a distinct color for light terminals", name, d, l)
		}
	}
	backgrounds := map[string][2]lipgloss.Style{
		"SelectedCard": {dark.SelectedCard, light.SelectedCard}, "KeyCap": {dark.KeyCap, light.KeyCap}, "Button": {dark.Button, light.Button},
	}
	for name, pair := range backgrounds {
		if d, l := pair[0].GetBackground(), pair[1].GetBackground(); d == nil || l == nil || d == l {
			t.Errorf("%s background = %v (dark) and %v (light), want a distinct color for light terminals", name, d, l)
		}
	}
}

func TestCountsArePluralized(t *testing.T) {
	m, _ := newSessionsModel(t, "db")
	m.sessions[0].Connections = 1
	if view := plainView(m); !strings.Contains(view, "1 connection") || strings.Contains(view, "connection(s)") {
		t.Fatalf("details do not say 1 connection:\n%s", view)
	}
	m.sessions[0].Connections = 2
	if view := plainView(m); !strings.Contains(view, "2 connections") {
		t.Fatalf("details do not say 2 connections:\n%s", view)
	}

	m, _ = update(t, m, key("q"))
	if view := plainView(m); !strings.Contains(view, "stops 1 active session and") {
		t.Fatalf("the quit dialog does not say 1 active session:\n%s", view)
	}
}

func TestHelpFitsAShortTerminal(t *testing.T) {
	m, _ := newSessionsModel(t, "db")
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = update(t, m, key("?"))
	if view := plainView(m); !helpComplete(view) {
		t.Fatalf("the help is cut on a 24-line terminal:\n%s", view)
	}

	w := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})
	w, _ = wizardUpdate(t, w, tea.WindowSizeMsg{Width: 80, Height: 24})
	w, _ = wizardUpdate(t, w, key("?"))
	if view := ansi.Strip(w.render()); !helpComplete(view) {
		t.Fatalf("the wizard help is cut on a 24-line terminal:\n%s", view)
	}
}

// helpComplete reports that view shows the help down to its closing hint and bottom border.
func helpComplete(view string) bool {
	hint := strings.Index(view, "any key closes this help")
	return hint >= 0 && strings.LastIndex(view, "╰") > hint
}
