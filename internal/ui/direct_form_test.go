package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// typeText types text into the focused field, one key per character.
func typeText(t *testing.T, m jumphostWizardModel, text string) jumphostWizardModel {
	t.Helper()
	for _, r := range text {
		m, _ = wizardUpdate(t, m, key(string(r)))
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
	m, _ = wizardUpdate(t, m, key("5"))
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
	m, cmd := wizardUpdate(t, m, key("ctrl+s"))
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
	m = focusFormField(t, m, formFieldEnvironment)
	m, _ = wizardUpdate(t, m, key(" "))
	m = saveForm(t, m)

	db, found, err := store.Get("reports")
	if err != nil || !found || db.Engine != directdb.EnginePostgres || db.Host != "reports.db.internal" || db.User != "reader" ||
		db.Port != 0 || db.TLS != "" || db.Environment != "prod" {
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
	m, cmd := wizardUpdate(t, m, key("ctrl+s"))

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
		f.host.Value() != "billing.db.internal" || f.user.Value() != "mcp_ro" || f.environment != "prod" {
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

	m, _ = wizardUpdate(t, m, key("ctrl+u"))
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
	m, _ = wizardUpdate(t, m, key("ctrl+u"))
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
	m, _ = wizardUpdate(t, m, key("ctrl+u"))
	m = typeText(t, m, "other.db.internal")
	_ = saveForm(t, m)

	db, _, err := store.Get("analytics")
	if err != nil || db.Host != "other.db.internal" || !slices.Equal(*forgotten, []jumphost.TargetRef{jumphost.DirectTargetRef("analytics")}) {
		t.Fatalf("analytics = %+v forgotten = %v, want the host changed and the remembered password forgotten", db, *forgotten)
	}
}

func TestDirectFormEnvironmentOnlyForgetsNothing(t *testing.T) {
	store := &fakeDirectStore{dbs: testDirectDatabases()}
	forgotten := &[]jumphost.TargetRef{}
	m := directFormWizard(t, forgetRecorder{forgotten: forgotten}, store, "analytics")

	m = focusFormField(t, m, formFieldEnvironment)
	m, _ = wizardUpdate(t, m, key(" "))
	_ = saveForm(t, m)

	db, _, err := store.Get("analytics")
	if err != nil || db.Environment != "prod" || len(*forgotten) != 0 {
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

	m, _ = wizardUpdate(t, m, key("esc"))
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
	m = focusFormField(t, m, formFieldEnvironment)
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
	m, _ = wizardUpdate(t, m, key("5"))

	keys := m.directListKeys([2]string{"?", "help"}, [2]string{"q", "quit"})
	if !slices.Contains(keys, [2]string{"a", "add"}) || !slices.Contains(keys, [2]string{"e", "edit"}) {
		t.Fatalf("keys = %v, want add and edit", keys)
	}

	empty := newDirectWizard(t, fakeSessionRunner{}, &fakeDirectStore{})
	empty, _ = wizardUpdate(t, empty, key("5"))
	keys = empty.directListKeys([2]string{"?", "help"}, [2]string{"q", "quit"})
	if !slices.Contains(keys, [2]string{"a", "add"}) || slices.Contains(keys, [2]string{"e", "edit"}) {
		t.Fatalf("keys = %v, want add without edit on an empty list", keys)
	}
}

func TestDirectFormForgetsBeforeSaving(t *testing.T) {
	store := &fakeDirectStore{dbs: testDirectDatabases()}
	forgotten, defined := &[]jumphost.TargetRef{}, &[]bool{}
	m := directFormWizard(t, forgetRecorder{forgotten: forgotten, store: store, definedAtForget: defined}, store, "analytics")

	m, _ = wizardUpdate(t, m, key("ctrl+u"))
	m = typeText(t, m, "events")
	saveForm(t, m)

	if !slices.Equal(*defined, []bool{true}) {
		t.Fatalf("defined at forget = %v, want analytics forgotten before the rename", *defined)
	}
}

func TestDirectFormKeepsTheDefinitionWhenForgettingFails(t *testing.T) {
	store := &fakeDirectStore{dbs: testDirectDatabases()}
	m := directFormWizard(t, forgetRecorder{forgotten: &[]jumphost.TargetRef{}, err: errors.New("keychain locked")}, store, "analytics")

	m, _ = wizardUpdate(t, m, key("ctrl+u"))
	m = typeText(t, m, "events")
	m = saveForm(t, m)

	if _, found, err := store.Get("analytics"); err != nil || !found || !strings.Contains(m.errorMsg, "keychain locked") {
		t.Fatalf("analytics found %v, error %q; want it unchanged and the error shown", found, m.errorMsg)
	}
}

func TestDirectFormRenameOntoExistingNameForgetsNothing(t *testing.T) {
	store := &fakeDirectStore{dbs: testDirectDatabases()}
	forgotten := &[]jumphost.TargetRef{}
	m := directFormWizard(t, forgetRecorder{forgotten: forgotten}, store, "analytics")

	m, _ = wizardUpdate(t, m, key("ctrl+u"))
	m = typeText(t, m, "legacy-billing")
	m = saveForm(t, m)

	if _, found, err := store.Get("analytics"); err != nil || !found || len(*forgotten) != 0 {
		t.Fatalf("analytics found %v, forgotten %v; want the source kept and nothing forgotten", found, *forgotten)
	}
	if m.step != jumphostStepDirectForm || m.directForm.focus != formFieldName {
		t.Fatalf("step = %v focus = %d, want the form kept on name", m.step, m.directForm.focus)
	}
	if view := wizardView(m); !strings.Contains(view, "name: a database named legacy-billing already exists") {
		t.Fatalf("view lacks the name error:\n%s", view)
	}
}

func TestDirectFormCyclesTheEnvironments(t *testing.T) {
	envs, err := environment.Parse("live!=*-live; staging=*stg*")
	if err != nil {
		t.Fatal(err)
	}
	// database() returns the zero value for an invalid form, so start from a valid one.
	previous := directdb.Database{Name: "billing", Engine: directdb.EnginePostgres, Host: "h", User: "u"}
	f := newDirectForm(&previous, envs).focusField(formFieldEnvironment)
	var seen []string
	for range 4 {
		db, _ := f.database() //nolint:errcheck // only the environment matters
		seen = append(seen, db.Environment)
		f, _, _ = f.Update(key("right"))
	}
	// The production is saved under its alias, not its own name, so a teammate's setting matches it.
	if strings.Join(seen, ",") != ",prod,staging," {
		t.Fatalf("environments = %q, want none, prod, staging, then none again", seen)
	}
	f, _, _ = f.Update(key("left"))
	f, _, _ = f.Update(key("left"))
	if db, _ := f.database(); db.Environment != "staging" { //nolint:errcheck // only the environment matters
		t.Fatalf("left twice = %q, want staging", db.Environment)
	}
}

func TestDirectFormShowsTheProductionUnderItsOwnNameAndSavesTheAlias(t *testing.T) {
	envs, err := environment.Parse("live!=*-live")
	if err != nil {
		t.Fatal(err)
	}
	previous := directdb.Database{Name: "billing", Engine: directdb.EnginePostgres, Host: "h", User: "u", Environment: "live"}
	f := newDirectForm(&previous, envs).focusField(formFieldEnvironment)
	if !strings.Contains(ansi.Strip(f.View(NewStyles(true), 100)), "LIVE") {
		t.Fatal("the production is not shown as LIVE")
	}
	f, _, _ = f.Update(key("right"))
	f, _, _ = f.Update(key("left"))
	if db, _ := f.database(); db.Environment != "prod" { //nolint:errcheck // only the environment matters
		t.Fatalf("right then left = %q, want the production back under its alias", db.Environment)
	}
}

func TestDirectFormKeepsAnEnvironmentTheSettingLacks(t *testing.T) {
	previous := directdb.Database{Name: "billing", Engine: directdb.EnginePostgres, Host: "h", User: "u", Environment: "qa"}
	f := newDirectForm(&previous, environment.Default())
	db, err := f.database()
	if err != nil {
		t.Fatal(err)
	}
	if db.Environment != "qa" {
		t.Fatalf("Environment = %q; want the unknown environment kept until changed", db.Environment)
	}
	if !strings.Contains(ansi.Strip(f.View(NewStyles(true), 100)), "QA") {
		t.Fatal("the unknown environment is not shown")
	}
	f, _, _ = f.focusField(formFieldEnvironment).Update(key("right"))
	if db, _ := f.database(); db.Environment != "" { //nolint:errcheck // only the environment matters
		t.Fatalf("right = %q, want the cycle to leave the unknown name for none", db.Environment)
	}
}

func TestDirectFormSavesAnUntouchedProductionNameAsTheAlias(t *testing.T) {
	envs, err := environment.Parse("live!=*-live")
	if err != nil {
		t.Fatal(err)
	}
	previous := directdb.Database{Name: "billing", Engine: directdb.EnginePostgres, Host: "h", User: "u", Environment: "live"}
	f := newDirectForm(&previous, envs)
	db, err := f.database()
	if err != nil {
		t.Fatal(err)
	}
	if db.Environment != "prod" {
		t.Fatalf("Environment = %q; want the hand-edited production name saved as prod", db.Environment)
	}
}
