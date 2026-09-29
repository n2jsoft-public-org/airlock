package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func rootUpdate(t *testing.T, m model, msg tea.Msg) (model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(msg)
	result, ok := updated.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", updated)
	}
	return result, cmd
}

func TestRootPickerFinishesAfterProfileWithoutRegionStep(t *testing.T) {
	m := newModel()
	m, _ = rootUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	m, _ = rootUpdate(t, m, key("enter")) // jumphost
	m, _ = rootUpdate(t, m, profileLoadedMsg{profiles: []profileSummary{{name: "dev"}, {name: "prod"}}})
	if m.step != stepProfile || m.loading {
		t.Fatalf("step = %v loading = %v, want the profile list", m.step, m.loading)
	}

	m, _ = rootUpdate(t, m, tea.KeyMsg{Type: tea.KeyDown})
	m, cmd := rootUpdate(t, m, key("enter"))
	if !isQuit(cmd) {
		t.Fatal("picker should hand over to the jumphost wizard right after the profile")
	}
	if m.selectedCmd != "jumphost" || m.selectedProfile != "prod" {
		t.Fatalf("selection = %s/%s, want jumphost/prod", m.selectedCmd, m.selectedProfile)
	}
}

func TestRootPickerTypesQIntoProfileFilter(t *testing.T) {
	m := newModel()
	m, _ = rootUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ = rootUpdate(t, m, key("enter"))
	m, _ = rootUpdate(t, m, profileLoadedMsg{profiles: []profileSummary{{name: "qa"}}})

	m, _ = rootUpdate(t, m, key("/"))
	m, cmd := rootUpdate(t, m, key("q"))
	if isQuit(cmd) || m.selectedCmd == "" {
		t.Fatal("q typed in the profile filter quit the picker")
	}
}
