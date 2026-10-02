package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// typeText types text into the focused field, one key per character.
func typeText(t *testing.T, m jumphostWizardModel, text string) jumphostWizardModel {
	t.Helper()
	for _, r := range text {
		m, _ = wizardUpdate(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

// focusFormField presses tab until field has the focus of the form.
func focusFormField(t *testing.T, m jumphostWizardModel, field int) jumphostWizardModel {
	t.Helper()
	for range formFieldCount {
		if m.directForm.focus == field {
			return m
		}
		m, _ = wizardUpdate(t, m, key("tab"))
	}
	t.Fatalf("field %d never got the focus", field)
	return m
}

// directFormWizard opens the form from the direct database list: of a new database when previous
// is empty, else of previous.
func directFormWizard(t *testing.T, runner JumphostDiscovery, store *fakeDirectStore, previous string) jumphostWizardModel {
	t.Helper()
	m := newDirectWizard(t, runner, store)
	m, _ = wizardUpdate(t, m, key("4"))
	if previous == "" {
		m, _ = wizardUpdate(t, m, key("a"))
	} else {
		m.directList.selectName(previous)
		m, _ = wizardUpdate(t, m, key("e"))
	}
	if m.step != jumphostStepDirectForm {
		t.Fatalf("step = %v, want the form", m.step)
	}
	return m
}

// saveForm presses ctrl+s and runs the save it starts.
func saveForm(t *testing.T, m jumphostWizardModel) jumphostWizardModel {
	t.Helper()
	m, cmd := wizardUpdate(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	return runCmd(t, m, cmd)
}

// fillValidForm types a valid new database: name, host and user.
func fillValidForm(t *testing.T, m jumphostWizardModel, name string) jumphostWizardModel {
	t.Helper()
	m = typeText(t, m, name)
	m = focusFormField(t, m, formFieldHost)
	m = typeText(t, m, "reports.db.internal")
	m = focusFormField(t, m, formFieldUser)
	return typeText(t, m, "reader")
}

func TestDirectFormAddsDatabase(t *testing.T) {
	store := &fakeDirectStore{dbs: testDirectDatabases()}
	m := directFormWizard(t, fakeSessionRunner{}, store, "")
	if view := wizardView(m); !strings.Contains(view, "Add a direct database") {
		t.Fatalf("view lacks the heading:\n%s", view)
	}

	m = fillValidForm(t, m, "reports")
	m = focusFormField(t, m, formFieldProduction)
	m, _ = wizardUpdate(t, m, key(" "))
	m = saveForm(t, m)

	db, found, err := store.Get("reports")
	if err != nil || !found || db.Engine != directdb.EnginePostgres || db.Host != "reports.db.internal" || db.User != "reader" ||
		db.Port != 0 || db.TLS != "" || !db.Production {
		t.Fatalf("saved = %+v (found %v), want a production postgres database with the defaults left out", db, found)
	}
	if m.step != jumphostStepTarget || m.loading {
		t.Fatalf("step = %v loading = %v, want the list again", m.step, m.loading)
	}
	if selected, ok := m.directList.selected(); !ok || selected.Name != "reports" {
		t.Fatalf("selected = %+v, want the saved database", selected)
	}
}

func TestDirectFormShowsFieldErrorUnderItsField(t *testing.T) {
	store := &fakeDirectStore{}
	m := directFormWizard(t, fakeSessionRunner{}, store, "")
	m = typeText(t, m, "reports")
	m, cmd := wizardUpdate(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})

	if cmd != nil || m.step != jumphostStepDirectForm || m.directForm.focus != formFieldHost || len(store.dbs) != 0 {
		t.Fatalf("cmd = %v step = %v focus = %d saved = %v, want the form kept on host, nothing saved",
			cmd != nil, m.step, m.directForm.focus, store.dbs)
	}
	if view := wizardView(m); !strings.Contains(view, "host: must not be empty") {
		t.Fatalf("view lacks the host error:\n%s", view)
	}

	// Editing the invalid field clears its error.
	m = typeText(t, m, "r")
	if view := wizardView(m); strings.Contains(view, "host: must not be empty") {
		t.Fatalf("the error outlived the edit:\n%s", view)
	}
}

func TestDirectFormRejectsNonNumericPort(t *testing.T) {
	store := &fakeDirectStore{}
	m := directFormWizard(t, fakeSessionRunner{}, store, "")
	m = fillValidForm(t, m, "reports")
	m = focusFormField(t, m, formFieldPort)
	m = typeText(t, m, "abc")
	m = saveForm(t, m)

	if m.directForm.focus != formFieldPort || len(store.dbs) != 0 || !strings.Contains(wizardView(m), "port: must be a number") {
		t.Fatalf("focus = %d saved = %v, want the port error:\n%s", m.directForm.focus, store.dbs, wizardView(m))
	}
}

func TestDirectFormEngineSetsPortPlaceholder(t *testing.T) {
	m := directFormWizard(t, fakeSessionRunner{}, &fakeDirectStore{}, "")
	if view := wizardView(m); !strings.Contains(view, "5432 (default)") || !strings.Contains(view, "postgres (default)") {
		t.Fatalf("view lacks the PostgreSQL defaults:\n%s", view)
	}

	m = focusFormField(t, m, formFieldEngine)
	m, _ = wizardUpdate(t, m, key(" "))
	view := wizardView(m)
	if m.directForm.engine != directdb.EngineSQLServer || !strings.Contains(view, "1433 (default)") || !strings.Contains(view, "login default") {
		t.Fatalf("engine = %q, want SQL Server and its defaults:\n%s", m.directForm.engine, view)
	}
}

func TestDirectFormCyclesTLS(t *testing.T) {
	m := directFormWizard(t, fakeSessionRunner{}, &fakeDirectStore{}, "")
	m = focusFormField(t, m, formFieldTLS)

	var seen []directdb.TLSMode
	for range 3 {
		m, _ = wizardUpdate(t, m, key(" "))
		seen = append(seen, m.directForm.effectiveTLS())
	}
	if !slices.Equal(seen, []directdb.TLSMode{directdb.TLSVerify, directdb.TLSDisable, directdb.TLSRequire}) {
		t.Fatalf("TLS cycle = %v, want verify, disable, require", seen)
	}
}

func TestDirectFormEditPrefillsDefinition(t *testing.T) {
	m := directFormWizard(t, fakeSessionRunner{}, &fakeDirectStore{dbs: testDirectDatabases()}, "legacy-billing")

	f := m.directForm
	if f.previousName != "legacy-billing" || f.name.Value() != "legacy-billing" || f.engine != directdb.EngineSQLServer ||
		f.host.Value() != "billing.db.internal" || f.user.Value() != "mcp_ro" || !f.production {
		t.Fatalf("form = %+v, want the definition of legacy-billing", f)
	}
	if view := wizardView(m); !strings.Contains(view, "Edit legacy-billing") {
		t.Fatalf("view lacks the heading:\n%s", view)
	}
}

func TestDirectFormRenameForgetsOldConnection(t *testing.T) {
	store := &fakeDirectStore{dbs: testDirectDatabases()}
	forgotten := &[]jumphost.TargetRef{}
	m := directFormWizard(t, forgetRecorder{forgotten: forgotten}, store, "analytics")

	m, _ = wizardUpdate(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = typeText(t, m, "events")
	m = saveForm(t, m)

	if _, found, err := store.Get("analytics"); err != nil || found {
		t.Fatalf("the former name is still defined (found %v, err %v)", found, err)
	}
	if db, found, err := store.Get("events"); err != nil || !found || db.Host != "analytics.db.internal" || db.Database != "events" {
		t.Fatalf("events = %+v (found %v), want analytics renamed", db, found)
	}
	if !slices.Equal(*forgotten, []jumphost.TargetRef{jumphost.DirectTargetRef("analytics")}) {
		t.Fatalf("forgotten = %v, want the prefs and password of the former name", *forgotten)
	}
	if selected, ok := m.directList.selected(); !ok || selected.Name != "events" {
		t.Fatalf("selected = %+v, want the renamed database", selected)
	}
}

func TestDirectFormUserChangeForgetsConnection(t *testing.T) {
	store := &fakeDirectStore{dbs: testDirectDatabases()}
	forgotten := &[]jumphost.TargetRef{}
	m := directFormWizard(t, forgetRecorder{forgotten: forgotten}, store, "analytics")

	m = focusFormField(t, m, formFieldUser)
	m, _ = wizardUpdate(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = typeText(t, m, "writer")
	_ = saveForm(t, m)

	if !slices.Equal(*forgotten, []jumphost.TargetRef{jumphost.DirectTargetRef("analytics")}) {
		t.Fatalf("forgotten = %v, want the password of the former user forgotten", *forgotten)
	}
}

func TestDirectFormHostChangeForgetsConnection(t *testing.T) {
	store := &fakeDirectStore{dbs: testDirectDatabases()}
	forgotten := &[]jumphost.TargetRef{}
	m := directFormWizard(t, forgetRecorder{forgotten: forgotten}, store, "analytics")

	m = focusFormField(t, m, formFieldHost)
	m, _ = wizardUpdate(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	m = typeText(t, m, "other.db.internal")
	_ = saveForm(t, m)

	db, _, err := store.Get("analytics")
	if err != nil || db.Host != "other.db.internal" || !slices.Equal(*forgotten, []jumphost.TargetRef{jumphost.DirectTargetRef("analytics")}) {
		t.Fatalf("analytics = %+v forgotten = %v, want the host changed and the remembered password forgotten", db, *forgotten)
	}
}

func TestDirectFormProductionOnlyForgetsNothing(t *testing.T) {
	store := &fakeDirectStore{dbs: testDirectDatabases()}
	forgotten := &[]jumphost.TargetRef{}
	m := directFormWizard(t, forgetRecorder{forgotten: forgotten}, store, "analytics")

	m = focusFormField(t, m, formFieldProduction)
	m, _ = wizardUpdate(t, m, key(" "))
	_ = saveForm(t, m)

	db, _, err := store.Get("analytics")
	if err != nil || !db.Production || len(*forgotten) != 0 {
		t.Fatalf("analytics = %+v forgotten = %v, want the production flag changed and nothing forgotten", db, *forgotten)
	}
}

func TestDirectFormExistingNameShownUnderName(t *testing.T) {
	store := &fakeDirectStore{dbs: testDirectDatabases(), putErr: fmt.Errorf("database %q %w", "analytics", directdb.ErrExists)}
	m := directFormWizard(t, fakeSessionRunner{}, store, "")
	m = fillValidForm(t, m, "analytics")
	m = saveForm(t, m)

	if m.step != jumphostStepDirectForm || m.directForm.focus != formFieldName || m.errorMsg != "" {
		t.Fatalf("step = %v focus = %d error = %q, want the form kept on name", m.step, m.directForm.focus, m.errorMsg)
	}
	if view := wizardView(m); !strings.Contains(view, "name: a database named analytics already exists") {
		t.Fatalf("view lacks the name error:\n%s", view)
	}
}

func TestDirectFormStoreFailureShownAtTop(t *testing.T) {
	store := &fakeDirectStore{putErr: errors.New("failed to write databases.json: permission denied")}
	m := directFormWizard(t, fakeSessionRunner{}, store, "")
	m = fillValidForm(t, m, "reports")
	m = saveForm(t, m)

	if m.step != jumphostStepDirectForm || !strings.Contains(m.errorMsg, "Could not save reports: failed to write databases.json: permission denied") {
		t.Fatalf("step = %v error = %q, want the failure above the form", m.step, m.errorMsg)
	}
}

func TestDirectFormEscReturnsToList(t *testing.T) {
	store := &fakeDirectStore{dbs: testDirectDatabases()}
	m := directFormWizard(t, fakeSessionRunner{}, store, "")
	m = typeText(t, m, "reports")

	m, _ = wizardUpdate(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.step != jumphostStepTarget || len(store.dbs) != 2 {
		t.Fatalf("step = %v dbs = %d, want the list, nothing saved", m.step, len(store.dbs))
	}
}

func TestDirectFormEnterMovesThenSubmitsOnLastField(t *testing.T) {
	m := directFormWizard(t, fakeSessionRunner{}, &fakeDirectStore{}, "")

	m, _ = wizardUpdate(t, m, key("enter"))
	if m.directForm.focus != formFieldEngine {
		t.Fatalf("focus = %d, want enter to move to the engine", m.directForm.focus)
	}
	m = focusFormField(t, m, formFieldProduction)
	m, _ = wizardUpdate(t, m, key("enter"))
	if m.directForm.focus != formFieldName || !strings.Contains(wizardView(m), "name: must not be empty") {
		t.Fatalf("focus = %d, want enter on the last field to submit the empty form:\n%s", m.directForm.focus, wizardView(m))
	}
}

func TestDirectFormTypesQWithoutQuitting(t *testing.T) {
	m := directFormWizard(t, fakeSessionRunner{}, &fakeDirectStore{}, "")
	m = typeText(t, m, "q?")
	if m.canceled || m.showHelp || m.directForm.name.Value() != "q?" {
		t.Fatalf("canceled = %v help = %v name = %q, want q and ? typed", m.canceled, m.showHelp, m.directForm.name.Value())
	}
}

func TestDirectFormBreadcrumbStaysOnTarget(t *testing.T) {
	m := directFormWizard(t, fakeSessionRunner{}, &fakeDirectStore{}, "")
	if steps, current := m.breadcrumb(); !slices.Equal(steps, []string{"Type", "Target", "Connect"}) || current != 1 {
		t.Fatalf("breadcrumb = %v (%d), want the form shown as the Target step", steps, current)
	}
}

func TestDirectListKeysOfferAddAndEdit(t *testing.T) {
	m := newDirectWizard(t, fakeSessionRunner{}, &fakeDirectStore{dbs: testDirectDatabases()})
	m, _ = wizardUpdate(t, m, key("4"))

	keys := m.directListKeys([2]string{"?", "help"}, [2]string{"q", "quit"})
	if !slices.Contains(keys, [2]string{"a", "add"}) || !slices.Contains(keys, [2]string{"e", "edit"}) {
		t.Fatalf("keys = %v, want add and edit", keys)
	}

	empty := newDirectWizard(t, fakeSessionRunner{}, &fakeDirectStore{})
	empty, _ = wizardUpdate(t, empty, key("4"))
	keys = empty.directListKeys([2]string{"?", "help"}, [2]string{"q", "quit"})
	if !slices.Contains(keys, [2]string{"a", "add"}) || slices.Contains(keys, [2]string{"e", "edit"}) {
		t.Fatalf("keys = %v, want add without edit on an empty list", keys)
	}
}
