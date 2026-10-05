package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// key builds the message a terminal sends for one key press: name is a key as String() names it
// ("esc", "enter", "ctrl+u", "shift+tab"), " " or "space" for the space bar, or the text the key
// types ("q", "Q", "eu-west"; several characters arrive as one message, like a paste).
func key(name string) tea.KeyPressMsg {
	switch name {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	case " ", "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	if runes := []rune(name); len(runes) == 1 {
		return tea.KeyPressMsg{Code: runes[0], Text: name}
	}
	return tea.KeyPressMsg{Code: tea.KeyExtended, Text: name}
}

func TestKeyNamesItsKey(t *testing.T) {
	for name, want := range map[string]string{
		" ": "space", "esc": "esc", "enter": "enter", "tab": "tab", "shift+tab": "shift+tab", "down": "down",
		"backspace": "backspace", "ctrl+u": "ctrl+u", "ctrl+c": "ctrl+c", "Q": "Q", "/": "/", "eu-west": "eu-west",
	} {
		if got := key(name).String(); got != want {
			t.Errorf("key(%q).String() = %q, want %q", name, got, want)
		}
	}
}

// Review Focus 3: uppercase and symbol shortcuts reach the dashboard.
func TestShortcutsSurviveTheUpgrade(t *testing.T) {
	m, _ := newSessionsModel(t, "orders-db")
	if m, _ = update(t, m, key("?")); m.mode != ssmModeHelp {
		t.Fatalf("? gives mode %v, want help", m.mode)
	}
	d := newDaemonView(t, newFakeService(testSession(1, "archive-db")))
	if d, _ = update(t, d, key("Q")); d.mode != ssmModeConfirmQuit {
		t.Fatalf("Q gives mode %v, want the stop-all confirmation", d.mode)
	}
}
