package ui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft-public-org/airlock/internal/directdb"
	"github.com/n2jsoft-public-org/airlock/internal/environment"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
	"github.com/n2jsoft-public-org/airlock/internal/sessions"
)

// fakeDirectStore is a DirectDatabaseStore in memory; listErr makes List fail, putErr Put.
type fakeDirectStore struct {
	listErr error
	putErr  error
	dbs     []directdb.Database
	deleted []string
}

func (s *fakeDirectStore) List() ([]directdb.Database, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]directdb.Database{}, s.dbs...), nil
}

func (s *fakeDirectStore) Get(name string) (directdb.Database, bool, error) {
	for _, db := range s.dbs {
		if db.Name == name {
			return db, true, nil
		}
	}
	return directdb.Database{}, false, nil
}

func (s *fakeDirectStore) Put(previousName string, db directdb.Database) error {
	if s.putErr != nil {
		return s.putErr
	}
	s.dbs = slices.DeleteFunc(s.dbs, func(existing directdb.Database) bool {
		return existing.Name == previousName || existing.Name == db.Name
	})
	s.dbs = append(s.dbs, db)
	slices.SortFunc(s.dbs, func(a, b directdb.Database) int { return strings.Compare(a.Name, b.Name) })
	return nil
}

func (s *fakeDirectStore) Delete(name string) error {
	s.deleted = append(s.deleted, name)
	s.dbs = slices.DeleteFunc(s.dbs, func(db directdb.Database) bool { return db.Name == name })
	return nil
}

// testDirectDatabases are two definitions, sorted by name as the store lists them.
func testDirectDatabases() []directdb.Database {
	return []directdb.Database{
		{Name: "analytics", Engine: directdb.EnginePostgres, Host: "analytics.db.internal", Database: "events", User: "reader"},
		{Name: "legacy-billing", Engine: directdb.EngineSQLServer, Host: "billing.db.internal", User: "mcp_ro", Environment: "prod"},
	}
}

// forgetRecorder is a session runner remembering the connections it forgets.
type forgetRecorder struct {
	fakeSessionRunner
	forgotten *[]jumphost.TargetRef
	// store, when set, records in definedAtForget whether each forgotten name was still defined.
	store           *fakeDirectStore
	definedAtForget *[]bool
	err             error
}

func (r forgetRecorder) ForgetConnection(_ context.Context, ref jumphost.TargetRef) error {
	*r.forgotten = append(*r.forgotten, ref)
	if r.store != nil {
		_, found, err := r.store.Get(ref.Name)
		if err != nil {
			return err
		}
		*r.definedAtForget = append(*r.definedAtForget, found)
	}
	return r.err
}

// newDirectWizard opens a wizard on the Type step of a new connection, with store wired and no
// --profile, so the Profile step would normally be asked.
func newDirectWizard(t *testing.T, runner JumphostDiscovery, store DirectDatabaseStore) jumphostWizardModel {
	t.Helper()
	m := newJumphostWizardModel(context.Background(), runner, jumphost.Options{})
	m.databases = store
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	return pastStart(t, m)
}

func wizardView(m jumphostWizardModel) string {
	return ansi.Strip(m.render())
}

func TestWizardOffersDirectCardOnlyWithStore(t *testing.T) {
	without := newTestWizard(t, fakeSessionRunner{}, jumphost.Options{})
	if len(without.typeOptions()) != 6 || strings.Contains(wizardView(without), "Direct database") {
		t.Fatalf("options = %d, want no Direct card without store:\n%s", len(without.typeOptions()), wizardView(without))
	}

	m := newDirectWizard(t, fakeSessionRunner{}, &fakeDirectStore{dbs: testDirectDatabases()})
	if view := wizardView(m); !strings.Contains(view, "Direct database") || !strings.Contains(view, "1-7") {
		t.Fatalf("view lacks the seventh card:\n%s", view)
	}
}

func TestWizardDirectCardSkipsProfileAndRegion(t *testing.T) {
	m := newDirectWizard(t, fakeSessionRunner{}, &fakeDirectStore{dbs: testDirectDatabases()})

	m, _ = wizardUpdate(t, m, key("7"))
	if m.step != jumphostStepTarget || m.options.ResourceType != "direct" || m.loading || m.pickingRegion {
		t.Fatalf("step = %v type = %q loading = %v region picker = %v, want the database list at once",
			m.step, m.options.ResourceType, m.loading, m.pickingRegion)
	}
	if steps, current := m.breadcrumb(); !slices.Equal(steps, []string{"Type", "Target", "Connect"}) || current != 1 {
		t.Fatalf("breadcrumb = %v (%d), want no Profile step", steps, current)
	}
	if view := wizardView(m); strings.Contains(view, "profile ") || strings.Contains(view, "region ") {
		t.Fatalf("a direct database shows AWS context chips:\n%s", view)
	}
}

func TestDirectListShowsDatabases(t *testing.T) {
	m := newDirectWizard(t, fakeSessionRunner{}, &fakeDirectStore{dbs: testDirectDatabases()})
	m, _ = wizardUpdate(t, m, key("7"))

	view := wizardView(m)
	for _, want := range []string{
		"Choose a direct database", "2 databases",
		"analytics", "postgres · analytics.db.internal:5432 · events",
		"legacy-billing", "sqlserver · billing.db.internal:1433", "PROD",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
}

func TestDirectListEmptyState(t *testing.T) {
	m := newDirectWizard(t, fakeSessionRunner{}, &fakeDirectStore{})
	m, _ = wizardUpdate(t, m, key("7"))

	if view := wizardView(m); !strings.Contains(view, "No direct database yet — press a to add one") {
		t.Fatalf("view lacks the empty state:\n%s", view)
	}
	m, _ = wizardUpdate(t, m, key("enter"))
	if m.step != jumphostStepTarget {
		t.Fatalf("step = %v, enter on an empty list must not go anywhere", m.step)
	}
}

func TestDirectListShowsStoreError(t *testing.T) {
	store := &fakeDirectStore{listErr: errors.New("invalid /home/dev/.config/airlock/databases.json: legacy-billing: host: must not be empty")}
	m := newDirectWizard(t, fakeSessionRunner{}, store)
	m, _ = wizardUpdate(t, m, key("7"))

	view := wizardView(m)
	if !strings.Contains(view, "databases.json") || !strings.Contains(view, "host: must not be empty") {
		t.Fatalf("view lacks the store error:\n%s", view)
	}
	if strings.Contains(view, "No direct database yet") {
		t.Fatalf("an unreadable file is not an empty one:\n%s", view)
	}
}

func TestDirectListEnterOpensReview(t *testing.T) {
	m := newDirectWizard(t, fakeSessionRunner{}, &fakeDirectStore{dbs: testDirectDatabases()})
	m, _ = wizardUpdate(t, m, key("7"))

	m, _ = wizardUpdate(t, m, key("down"))
	m, _ = wizardUpdate(t, m, key("enter"))
	if m.step != jumphostStepReview || m.options.ResourceType != "direct" || m.options.Name != "legacy-billing" ||
		m.options.DBUser != "mcp_ro" || m.directDB.Host != "billing.db.internal" {
		t.Fatalf("step = %v options = %+v directDB = %+v, want the review of legacy-billing", m.step, m.options, m.directDB)
	}

	m, _ = wizardUpdate(t, m, key("esc"))
	if m.step != jumphostStepTarget {
		t.Fatalf("step = %v, esc on the review must return to the database list", m.step)
	}
}

func TestDirectListFiltersWithoutQuitting(t *testing.T) {
	m := newDirectWizard(t, fakeSessionRunner{}, &fakeDirectStore{dbs: testDirectDatabases()})
	m, _ = wizardUpdate(t, m, key("7"))

	for _, k := range []string{"/", "q", "b"} {
		m, _ = wizardUpdate(t, m, key(k))
	}
	if m.canceled {
		t.Fatal("q typed in the filter quit the wizard")
	}
	if !m.typingText() {
		t.Fatal("typingText = false while the filter is being typed")
	}
}

func TestDirectListDeleteForgetsConnection(t *testing.T) {
	store := &fakeDirectStore{dbs: testDirectDatabases()}
	forgotten := &[]jumphost.TargetRef{}
	m := newDirectWizard(t, forgetRecorder{forgotten: forgotten}, store)
	m, _ = wizardUpdate(t, m, key("7"))

	m, _ = wizardUpdate(t, m, key("x"))
	if view := wizardView(m); !strings.Contains(view, "Delete analytics? y/n") {
		t.Fatalf("view lacks the confirmation:\n%s", view)
	}
	m, cmd := wizardUpdate(t, m, key("y"))
	m = runCmd(t, m, cmd)

	if !slices.Equal(store.deleted, []string{"analytics"}) || !slices.Equal(*forgotten, []jumphost.TargetRef{jumphost.DirectTargetRef("analytics")}) {
		t.Fatalf("deleted = %v forgotten = %v, want the definition, its prefs and password gone", store.deleted, *forgotten)
	}
	if m.loading || m.directList.count() != 1 || strings.Contains(wizardView(m), "analytics") {
		t.Fatalf("loading = %v count = %d, want the list reloaded without analytics:\n%s", m.loading, m.directList.count(), wizardView(m))
	}
}

func TestDirectListDeleteKeepsOnOtherKey(t *testing.T) {
	store := &fakeDirectStore{dbs: testDirectDatabases()}
	m := newDirectWizard(t, fakeSessionRunner{}, store)
	m, _ = wizardUpdate(t, m, key("7"))

	m, _ = wizardUpdate(t, m, key("x"))
	m, cmd := wizardUpdate(t, m, key("n"))
	if cmd != nil || len(store.deleted) != 0 || m.directList.confirmingDelete() {
		t.Fatalf("deleted = %v confirming = %v, want the database kept", store.deleted, m.directList.confirmingDelete())
	}
}

func TestDirectListEscGoesBackToType(t *testing.T) {
	m := newDirectWizard(t, fakeSessionRunner{}, &fakeDirectStore{dbs: testDirectDatabases()})
	m, _ = wizardUpdate(t, m, key("7"))

	m, _ = wizardUpdate(t, m, key("esc"))
	if m.step != jumphostStepType {
		t.Fatalf("step = %v, want the Type step", m.step)
	}
}

func TestDirectListConnectedEntrySwitchesToSession(t *testing.T) {
	m := newDirectWizard(t, fakeSessionRunner{}, &fakeDirectStore{dbs: testDirectDatabases()})
	m.embedded = true
	m.active = map[string]bool{directConnectionKey("analytics"): true}
	m, _ = wizardUpdate(t, m, key("7"))

	if view := wizardView(m); !strings.Contains(view, "analytics  ● connected") {
		t.Fatalf("view lacks the connected marker:\n%s", view)
	}
	m, cmd := wizardUpdate(t, m, key("enter"))
	done, ok := cmd().(jumphostWizardDoneMsg)
	if !ok || !done.canceled || done.switchTo != directConnectionKey("analytics") {
		t.Fatalf("done = %+v, want the running session shown instead of a second one", done)
	}
}

func TestDirectListReportsAddAndEdit(t *testing.T) {
	p := newDirectList(NewStyles(true), testDirectDatabases(), nil, nil, environment.Set{})
	p.SetSize(80, 20)
	if _, outcome, _ := p.Update(key("a")); outcome != directListAdd {
		t.Fatalf("a = %v, want directListAdd", outcome)
	}
	if _, outcome, _ := p.Update(key("e")); outcome != directListEdit {
		t.Fatalf("e = %v, want directListEdit", outcome)
	}

	empty := newDirectList(NewStyles(true), nil, nil, nil, environment.Set{})
	empty.SetSize(80, 20)
	if _, outcome, _ := empty.Update(key("e")); outcome != directListOpen {
		t.Fatalf("e on an empty list = %v, want nothing", outcome)
	}
	if _, outcome, _ := empty.Update(key("a")); outcome != directListAdd {
		t.Fatalf("a on an empty list = %v, want directListAdd", outcome)
	}
}

func TestDirectListSelectName(t *testing.T) {
	p := newDirectList(NewStyles(true), testDirectDatabases(), nil, nil, environment.Set{})
	p.SetSize(80, 20)
	p.selectName("legacy-billing")
	if db, ok := p.selected(); !ok || db.Name != "legacy-billing" {
		t.Fatalf("selected = %+v, want legacy-billing", db)
	}
}

func TestDashboardPassesDatabasesToWizard(t *testing.T) {
	store := &fakeDirectStore{}
	m, _ := newSessionsModel(t)
	m.databases = store
	m = m.showWizard(jumphost.Options{})
	if m.wizard.databases != store {
		t.Fatal("the wizard did not get the dashboard's direct databases")
	}
}

func TestSearchAfterDirectListPicksAWSTarget(t *testing.T) {
	runner := searchRunner{prefsRunner: newPrefsRunner(), targets: everyTypeTargets}
	m := newTestWizard(t, runner, jumphost.Options{AwsRegion: "eu-west-3"})
	m.databases = &fakeDirectStore{dbs: testDirectDatabases()}
	m, _ = wizardUpdate(t, m, key("7"))
	m, _ = wizardUpdate(t, m, key("esc"))
	m, cmd := wizardUpdate(t, m, key("s"))
	m = runCmd(t, m, cmd)

	if m.step != jumphostStepTarget || m.isDirect() || strings.Contains(wizardView(m), "analytics") {
		t.Fatalf("step = %v direct = %v, want the search targets:\n%s", m.step, m.isDirect(), wizardView(m))
	}
	m, cmd = wizardUpdate(t, m, key("enter"))
	m = runCmd(t, m, cmd)
	if m.step != jumphostStepReview || m.options.ResourceType == sessions.DirectResourceType {
		t.Fatalf("step = %v type = %q, want the review of an AWS target", m.step, m.options.ResourceType)
	}
}

func TestDirectListForgetsBeforeDeleting(t *testing.T) {
	store := &fakeDirectStore{dbs: testDirectDatabases()}
	forgotten, defined := &[]jumphost.TargetRef{}, &[]bool{}
	m := newDirectWizard(t, forgetRecorder{forgotten: forgotten, store: store, definedAtForget: defined}, store)
	m, _ = wizardUpdate(t, m, key("7"))
	m, _ = wizardUpdate(t, m, key("x"))
	m, cmd := wizardUpdate(t, m, key("y"))
	runCmd(t, m, cmd)

	if !slices.Equal(*defined, []bool{true}) || !slices.Equal(store.deleted, []string{"analytics"}) {
		t.Fatalf("defined at forget = %v, deleted = %v; want the connection forgotten while analytics was still defined", *defined, store.deleted)
	}
}

func TestDirectListKeepsTheDatabaseWhenForgettingFails(t *testing.T) {
	store := &fakeDirectStore{dbs: testDirectDatabases()}
	m := newDirectWizard(t, forgetRecorder{forgotten: &[]jumphost.TargetRef{}, err: errors.New("keychain locked")}, store)
	m, _ = wizardUpdate(t, m, key("7"))
	m, _ = wizardUpdate(t, m, key("x"))
	m, cmd := wizardUpdate(t, m, key("y"))
	m = runCmd(t, m, cmd)

	if len(store.deleted) != 0 || !strings.Contains(m.errorMsg, "Could not delete analytics") || !strings.Contains(m.errorMsg, "keychain locked") {
		t.Fatalf("deleted = %v error = %q, want analytics kept and the error shown", store.deleted, m.errorMsg)
	}
}
