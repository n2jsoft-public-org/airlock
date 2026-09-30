package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

func TestKeyHintsKeepPinnedKeysWhenShort(t *testing.T) {
	keys := [][2]string{{"n", "new"}, {"x", "stop"}, {"tab", "switch"}, {"1-9", "jump"}, {"↑↓", "scroll"}, {"?", "help"}, {"q", "quit"}}
	line := ansi.Strip(NewStyles().KeyHintsPinned(keys, 2, 40))
	if lipgloss.Width(line) > 40 {
		t.Fatalf("hints %q exceed 40 cells", line)
	}
	for _, want := range []string{"n  new", "…", "?  help", "q  quit"} {
		if !strings.Contains(line, want) {
			t.Fatalf("hints %q lack %q", line, want)
		}
	}
	if full := ansi.Strip(NewStyles().KeyHintsPinned(keys, 2, 200)); strings.Contains(full, "…") {
		t.Fatalf("hints %q elide keys although they fit", full)
	}
}

func TestDashboardFooterKeepsHelpAndQuitOnNarrowTerminals(t *testing.T) {
	m := newTestSSMModel(t, "db")
	m, _ = update(t, m, tea.WindowSizeMsg{Width: 60, Height: 30})
	view := plainView(m)
	for _, want := range []string{"?  help", "q  quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("footer lacks %q:\n%s", want, view)
		}
	}
}

func TestDashboardHelpListsEveryKey(t *testing.T) {
	m := newTestSSMModel(t, "db")

	m, _ = update(t, m, key("?"))
	if m.mode != ssmModeHelp {
		t.Fatalf("mode = %v, want the help", m.mode)
	}
	view := plainView(m)
	for _, want := range []string{"Sessions", "Logs", "Restart an ended session", "Back to the live tail", "Stop all sessions and quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help lacks %q:\n%s", want, view)
		}
	}
	if got := lipgloss.Height(m.View()); got > 40 {
		t.Fatalf("help height %d exceeds the terminal", got)
	}

	m, cmd := update(t, m, key("q"))
	if m.mode != ssmModeSessions || isQuit(cmd) {
		t.Fatalf("mode = %v quit = %v, want q to close the help only", m.mode, isQuit(cmd))
	}
}

func TestWizardHelpShowsStepKeys(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})

	m, _ = wizardUpdate(t, m, key("?"))
	if view := ansi.Strip(m.View()); !strings.Contains(view, "Keys") || !strings.Contains(view, "region") {
		t.Fatalf("the help does not list the Type step keys:\n%s", view)
	}
	m, cmd := wizardUpdate(t, m, key("q"))
	if strings.Contains(ansi.Strip(m.View()), "Keys") || cmd != nil {
		t.Fatal("a key did not just close the help")
	}
}

func TestWizardQuestionMarkIsTextWhileFiltering(t *testing.T) {
	m := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})
	m, _ = wizardUpdate(t, m, key("enter")) // rds
	m, _ = wizardUpdate(t, m, jumphostTargetsLoadedMsg{targets: []jumphost.ResourceSummary{{Name: "db"}}})

	m, _ = wizardUpdate(t, m, key("/"))
	m, _ = wizardUpdate(t, m, key("?"))
	if m.showHelp || m.targetList.FilterValue() != "?" {
		t.Fatalf("help = %v filter = %q, want ? typed in the filter", m.showHelp, m.targetList.FilterValue())
	}
}
