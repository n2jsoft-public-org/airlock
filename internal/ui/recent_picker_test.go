package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

var pickerNow = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

func recentFixture() []jumphost.SavedConnection {
	return []jumphost.SavedConnection{
		{Ref: jumphost.TargetRef{Account: "1", Region: "eu-west-3", Type: "rds", Name: "archive"}, Prefs: jumphost.ConnectionPrefs{Profile: "dev", UpdatedAt: pickerNow.Add(-3 * time.Hour)}},
		{Ref: jumphost.TargetRef{Account: "1", Region: "eu-west-1", Type: "eks", Name: "platform"}, Prefs: jumphost.ConnectionPrefs{Profile: "dev", UpdatedAt: pickerNow.Add(-50 * time.Hour)}},
	}
}

func newTestRecentPicker(active map[string]bool) recentPicker {
	p := newRecentPicker(NewStyles(true), recentFixture(), active, nil, nil, pickerNow)
	p.SetSize(100, 20)
	return p
}

func pickerKey(t *testing.T, p recentPicker, keys ...string) (recentPicker, recentPickerOutcome) {
	t.Helper()
	outcome := recentPickerOpen
	for _, k := range keys {
		p, outcome, _ = p.Update(key(k))
	}
	return p, outcome
}

func TestRecentPickerPicksEntry(t *testing.T) {
	p, outcome := pickerKey(t, newTestRecentPicker(nil), "down", "enter")
	item, ok := p.selected()
	if outcome != recentPickerPicked || !ok || item.conn.Ref.Name != "platform" {
		t.Fatalf("outcome = %v item = %+v", outcome, item)
	}
}

func TestRecentPickerDescribesEntries(t *testing.T) {
	item, _ := newTestRecentPicker(nil).selected()
	if got := item.Description(); got != "dev · eu-west-3 · 3h ago" {
		t.Fatalf("description = %q, want the type in its badge only", got)
	}
	if view := ansi.Strip(newTestRecentPicker(nil).View(NewStyles(true), 100)); !strings.Contains(view, "RDS") || !strings.Contains(view, "EKS") {
		t.Fatalf("the recent connections do not show their type badge:\n%s", view)
	}
	for _, want := range []string{"archive", "rds", "dev", "eu-west-3"} {
		if !strings.Contains(item.FilterValue(), want) {
			t.Fatalf("filter value %q lacks %q", item.FilterValue(), want)
		}
	}
}

func TestRecentPickerMovesPastLastEntryToActions(t *testing.T) {
	p, outcome := pickerKey(t, newTestRecentPicker(nil), "down", "down")
	if outcome != recentPickerOpen || p.action != recentActionNew {
		t.Fatalf("action = %d, want New connection focused", p.action)
	}
	if _, outcome = pickerKey(t, p, "enter"); outcome != recentPickerNew {
		t.Fatalf("outcome = %v, want New connection", outcome)
	}
	p, _ = pickerKey(t, p, "down")
	if _, outcome = pickerKey(t, p, "enter"); outcome != recentPickerSearch {
		t.Fatalf("outcome = %v, want Search a target", outcome)
	}
	p, _ = pickerKey(t, p, "up", "up")
	if p.action != recentActionNone {
		t.Fatalf("action = %d, want back in the list", p.action)
	}
}

func TestRecentPickerShortcuts(t *testing.T) {
	if _, outcome := pickerKey(t, newTestRecentPicker(nil), "n"); outcome != recentPickerNew {
		t.Fatalf("n = %v", outcome)
	}
	if _, outcome := pickerKey(t, newTestRecentPicker(nil), "s"); outcome != recentPickerSearch {
		t.Fatalf("s = %v", outcome)
	}
}

func TestRecentPickerShortcutsAreTextWhileFiltering(t *testing.T) {
	p, outcome := pickerKey(t, newTestRecentPicker(nil), "/", "n", "s", "d")
	if outcome != recentPickerOpen || p.confirmingForget() || !p.typing() {
		t.Fatalf("outcome = %v confirming = %v, letters must go to the filter", outcome, p.confirmingForget())
	}
}

func TestRecentPickerForgetAsksConfirmation(t *testing.T) {
	p, outcome := pickerKey(t, newTestRecentPicker(nil), "d")
	if outcome != recentPickerOpen || !p.confirmingForget() || !strings.Contains(p.View(NewStyles(true), 100), "Forget archive? y/n") {
		t.Fatalf("outcome = %v, want a confirmation", outcome)
	}
	p, outcome = pickerKey(t, p, "n")
	if outcome != recentPickerOpen || p.confirmingForget() {
		t.Fatalf("outcome = %v, any other key keeps the entry", outcome)
	}
	p, outcome = pickerKey(t, p, "d", "y")
	item, _ := p.selected()
	if outcome != recentPickerForget || item.conn.Ref.Name != "archive" {
		t.Fatalf("outcome = %v item = %+v", outcome, item)
	}
}

func TestRecentPickerSwitchesToConnectedEntry(t *testing.T) {
	active := map[string]bool{connectionKey("dev", "eu-west-3", "rds", "archive"): true}
	p := newTestRecentPicker(active)
	item, _ := p.selected()
	if !strings.Contains(item.Title(), "● connected") {
		t.Fatalf("title = %q, want the connected badge", item.Title())
	}
	if _, outcome := pickerKey(t, p, "enter"); outcome != recentPickerSwitch {
		t.Fatalf("outcome = %v, want a switch to the running session rather than a second one", outcome)
	}
}

func TestRelativeAge(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second: "just now",
		5 * time.Minute:  "5m ago",
		3 * time.Hour:    "3h ago",
		50 * time.Hour:   "2d ago",
	}
	for age, want := range cases {
		if got := relativeAge(pickerNow, pickerNow.Add(-age)); got != want {
			t.Fatalf("relativeAge(%v) = %q, want %q", age, got, want)
		}
	}
	if got := relativeAge(pickerNow, time.Time{}); got != "" {
		t.Fatalf("zero time = %q", got)
	}
}

var _ tea.Msg = key("x")

func TestRecentPickerEscGoesBack(t *testing.T) {
	if _, outcome := pickerKey(t, newTestRecentPicker(nil), "esc"); outcome != recentPickerBack {
		t.Fatalf("outcome = %v, want back", outcome)
	}
}

func TestRecentPickerEscClearsFilterFirst(t *testing.T) {
	p, _ := pickerKey(t, newTestRecentPicker(nil), "/", "a", "enter")
	if !p.list.IsFiltered() {
		t.Fatal("filter not applied")
	}
	p, outcome := pickerKey(t, p, "esc")
	if outcome != recentPickerOpen || p.list.IsFiltered() {
		t.Fatalf("outcome = %v filtered = %v, want the filter cleared first", outcome, p.list.IsFiltered())
	}
}

func TestRecentPickerTypingALetterFilters(t *testing.T) {
	p, outcome := pickerKey(t, newTestRecentPicker(nil), "p")
	p, _, filter := p.Update(key("l"))
	if outcome != recentPickerOpen || !p.typing() || p.list.FilterValue() != "pl" {
		t.Fatalf("outcome = %v typing = %v filter = %q, want pl typed in the filter", outcome, p.typing(), p.list.FilterValue())
	}
	p = applyFilter(p, filter)
	items := p.list.VisibleItems()
	if len(items) != 1 {
		t.Fatalf("visible = %v, want platform only", items)
	}
	if item, ok := items[0].(recentItem); !ok || item.conn.Ref.Name != "platform" {
		t.Fatalf("visible = %v, want platform only", items)
	}
	if !strings.Contains(ansi.Strip(newTestRecentPicker(nil).View(NewStyles(true), 100)), "type to filter") {
		t.Fatal("the start screen does not say that typing filters")
	}
}

func TestRecentPickerShortcutLettersDoNotFilter(t *testing.T) {
	for _, k := range []string{"n", "s", "d", "q", "j", "k", "?"} {
		if p, _ := pickerKey(t, newTestRecentPicker(nil), k); p.typing() {
			t.Errorf("%s started the filter, it is a shortcut", k)
		}
	}
}

func TestWizardTypingOnStartScreenFilters(t *testing.T) {
	m := startWizard(t, newHistoryRunner(savedArchive))
	m, _ = wizardUpdate(t, m, key("a"))
	m, cmd := wizardUpdate(t, m, key("q"))
	if !m.recent.typing() || isQuit(cmd) || m.recent.list.FilterValue() != "aq" {
		t.Fatalf("typing = %v filter = %q, want the letters typed in the filter", m.recent.typing(), m.recent.list.FilterValue())
	}
}

// applyFilter feeds p the matches its list computes in the background after a filter change.
func applyFilter(p recentPicker, cmd tea.Cmd) recentPicker {
	msg := runWithTimeout(cmd)
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		batch = tea.BatchMsg{func() tea.Msg { return msg }}
	}
	for _, inner := range batch {
		if inner == nil {
			continue
		}
		if matches, ok := runWithTimeout(inner).(list.FilterMatchesMsg); ok {
			p, _, _ = p.Update(matches)
		}
	}
	return p
}
