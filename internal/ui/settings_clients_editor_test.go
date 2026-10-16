package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/n2jsoft/airlock/internal/aiclient"
)

func clientsView(value string) SettingView {
	return testSetting("mcp.clients", "mcp", KindClients, value)
}

func TestClientsEditorRoundTrips(t *testing.T) {
	for _, value := range []string{"auto", "none", "claude", "claude:user,vscode", "auto,cursor:user"} {
		sel, err := aiclient.Parse(value)
		if err != nil {
			t.Fatal(err)
		}
		if got := newClientsEditor(clientsView(value), nil).Value(); got != sel.String() {
			t.Errorf("%q: Value = %q, want %q", value, got, sel.String())
		}
	}
}

func TestClientsEditorToggles(t *testing.T) {
	ed, outcome := pressEditor(t, newClientsEditor(clientsView("auto"), nil), "down", "down", "right", "space")
	if outcome != editorStay || ed.Value() != "auto,vscode:user" {
		t.Fatalf("outcome = %v value = %q, want auto,vscode:user", outcome, ed.Value())
	}
	// Space on the same cell unchecks it, and on Auto-detect turns detection off.
	ed, _ = pressEditor(t, ed, "space", "up", "up", "space")
	if ed.Value() != "none" {
		t.Fatalf("value = %q, want none", ed.Value())
	}
	if _, outcome = pressEditor(t, ed, "enter"); outcome != editorSave {
		t.Fatalf("enter: outcome = %v, want save", outcome)
	}
}

func TestClientsEditorNone(t *testing.T) {
	if ed, _ := pressEditor(t, newClientsEditor(clientsView("auto,claude:user,vscode"), nil), "n"); ed.Value() != "none" {
		t.Fatalf("value = %q, want none", ed.Value())
	}
}

func TestClientsEditorDetectedHint(t *testing.T) {
	view := ansi.Strip(newClientsEditor(clientsView("auto"), []aiclient.Client{aiclient.Claude}).View(NewStyles(true), 57, 20))
	var claudeRow, foundLine string
	for line := range strings.SplitSeq(view, "\n") {
		switch {
		case strings.Contains(line, "found here"):
			foundLine = line
		case strings.Contains(line, "Claude Code"):
			claudeRow = line
		}
	}
	if !strings.Contains(claudeRow, "auto") || !strings.Contains(foundLine, "Claude Code") {
		t.Fatalf("no auto hint or found-here line:\n%s", view)
	}
	if strings.Contains(view, "VS Code      [ ] auto") {
		t.Fatalf("auto hint on a client not detected:\n%s", view)
	}
	// Nil is an unknown directory: no line; empty is a known one with no client.
	if view := ansi.Strip(newClientsEditor(clientsView("auto"), nil).View(NewStyles(true), 57, 20)); strings.Contains(view, "found here") {
		t.Fatalf("found-here line for an unknown directory:\n%s", view)
	}
	if view := ansi.Strip(newClientsEditor(clientsView("auto"), []aiclient.Client{}).View(NewStyles(true), 57, 20)); !strings.Contains(view, "none found here") {
		t.Fatalf("no none-found line:\n%s", view)
	}
}

func TestClientsEditorLocked(t *testing.T) {
	view := clientsView("claude:user,vscode")
	view.Locked = true
	ed, outcome := pressEditor(t, newClientsEditor(view, nil), "down", "space", "right", "space", "n", "enter")
	if outcome != editorStay || ed.Value() != "claude:user,vscode" {
		t.Fatalf("outcome = %v value = %q, want no change and no save", outcome, ed.Value())
	}
}

func TestClientsSummary(t *testing.T) {
	if got := settingSummary(clientsView("auto,claude:user,vscode")); got != "auto, claude (user), vscode" {
		t.Fatalf("summary = %q", got)
	}
	if got := settingSummary(clientsView("none")); got != "none" {
		t.Fatalf("summary = %q, want none", got)
	}
}
