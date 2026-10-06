package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func newTestExportPicker() exportPicker {
	return newExportPicker(NewStyles(true), "Export direct databases", []ExportChoice{
		{Name: "archive", Detail: "postgres · archive.internal:5432 · reader"},
		{Name: "billing", Detail: "sqlserver · billing.internal:1433 · app", Production: true},
		{Name: "reporting", Detail: "postgres · reporting.internal:6432 · bi"},
	})
}

func exportKeys(t *testing.T, p exportPicker, keys ...string) (exportPicker, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keys {
		var model tea.Model
		model, cmd = p.Update(key(k))
		p = asExportPicker(t, model)
	}
	return p, cmd
}

func asExportPicker(t *testing.T, model tea.Model) exportPicker {
	t.Helper()
	p, ok := model.(exportPicker)
	if !ok {
		t.Fatalf("model = %T, want exportPicker", model)
	}
	return p
}

func TestExportPickerChecksEverythingAtStart(t *testing.T) {
	p, cmd := exportKeys(t, newTestExportPicker(), "enter")
	if !p.done || cmd == nil || !slices.Equal(p.selected(), []int{0, 1, 2}) {
		t.Fatalf("done = %v selected = %v, want every choice exported", p.done, p.selected())
	}
}

func TestExportPickerTogglesTheCurrentChoice(t *testing.T) {
	p, _ := exportKeys(t, newTestExportPicker(), "down", "space", "down", "x", "x", "enter")
	if !p.done || !slices.Equal(p.selected(), []int{0, 2}) {
		t.Fatalf("selected = %v, want billing unchecked only", p.selected())
	}
}

func TestExportPickerTogglesAll(t *testing.T) {
	p, _ := exportKeys(t, newTestExportPicker(), "a")
	if len(p.selected()) != 0 {
		t.Fatalf("selected = %v, want none after a with all checked", p.selected())
	}
	if p, _ = exportKeys(t, p, "space", "a"); len(p.selected()) != 3 {
		t.Fatalf("selected = %v, want all after a with some checked", p.selected())
	}
}

func TestExportPickerRefusesAnEmptyExport(t *testing.T) {
	p, cmd := exportKeys(t, newTestExportPicker(), "a", "enter")
	if p.done || cmd != nil {
		t.Fatal("enter with nothing checked left the picker")
	}
	if view := ansi.Strip(p.render()); !strings.Contains(view, "Select at least one item") || !strings.Contains(view, "0/3 selected") {
		t.Fatalf("the picker does not tell why it stays:\n%s", view)
	}
}

func TestExportPickerCancels(t *testing.T) {
	for _, k := range []string{"esc", "q", "ctrl+c"} {
		p, cmd := exportKeys(t, newTestExportPicker(), k)
		if !p.canceled || p.done || cmd == nil {
			t.Fatalf("%s: canceled = %v done = %v", k, p.canceled, p.done)
		}
		if p.View().Content != "" {
			t.Fatalf("%s: the picker stays on screen once left", k)
		}
	}
}

func TestExportPickerShowsTheChoices(t *testing.T) {
	p, _ := exportKeys(t, newTestExportPicker(), "down", "space")
	view := ansi.Strip(p.render())
	for _, want := range []string{"Export direct databases", "2/3 selected", "[x] archive", "[ ] billing", "PROD", "reporting.internal:6432"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
}

func TestExportPickerScrollsToTheCursor(t *testing.T) {
	choices := make([]ExportChoice, 30)
	for idx := range choices {
		choices[idx] = ExportChoice{Name: "db-" + string(rune('a'+idx%26)) + strings.Repeat("x", idx/26)}
	}
	model, _ := newExportPicker(NewStyles(true), "Export", choices).Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	p, _ := exportKeys(t, asExportPicker(t, model), "end")
	view := ansi.Strip(p.render())
	if strings.Contains(view, "db-a ") || !strings.Contains(view, "db-dx") {
		t.Fatalf("the last choice is not in view:\n%s", view)
	}
}
