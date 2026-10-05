package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

func TestPaletteAdaptsToLightTerminals(t *testing.T) {
	s := NewStyles()
	foregrounds := map[string]lipgloss.Style{
		"Text": s.Text, "Muted": s.Muted, "Faint": s.Faint, "Title": s.Title, "Subtitle": s.Subtitle,
		"Success": s.Success, "Warning": s.Warning, "Danger": s.Danger, "Rule": s.Rule,
	}
	for name, style := range foregrounds {
		if color, ok := style.GetForeground().(lipgloss.AdaptiveColor); !ok || color.Light == "" || color.Light == color.Dark {
			t.Errorf("%s foreground = %#v, want a distinct color for light terminals", name, style.GetForeground())
		}
	}
	for name, style := range map[string]lipgloss.Style{"SelectedCard": s.SelectedCard, "KeyCap": s.KeyCap, "Button": s.Button} {
		if color, ok := style.GetBackground().(lipgloss.AdaptiveColor); !ok || color.Light == "" || color.Light == color.Dark {
			t.Errorf("%s background = %#v, want a distinct color for light terminals", name, style.GetBackground())
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
	if view := ansi.Strip(w.View()); !helpComplete(view) {
		t.Fatalf("the wizard help is cut on a 24-line terminal:\n%s", view)
	}
}

// helpComplete reports that view shows the help down to its closing hint and bottom border.
func helpComplete(view string) bool {
	hint := strings.Index(view, "any key closes this help")
	return hint >= 0 && strings.LastIndex(view, "╰") > hint
}
