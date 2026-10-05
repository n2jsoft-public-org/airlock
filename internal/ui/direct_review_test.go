package ui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// directKeychainRunner remembers choices in memory and keeps the password of mcp_ro on legacy-billing.
type directKeychainRunner struct{ *prefsRunner }

func (*directKeychainRunner) LoadKeychainPassword(_ context.Context, options jumphost.Options) (string, bool) {
	if options.ResourceType == "direct" && options.Name == "legacy-billing" && options.DBUser == "mcp_ro" {
		return "from-keychain", true
	}
	return "", false
}

// directReview opens the review of the direct database name from the list, its remembered choices
// loaded.
func directReview(t *testing.T, runner JumphostDiscovery, store *fakeDirectStore, name string) jumphostWizardModel {
	t.Helper()
	m := newDirectWizard(t, runner, store)
	m, _ = wizardUpdate(t, m, key("4"))
	m.directList.selectName(name)
	m, cmd := wizardUpdate(t, m, key("enter"))
	m, _ = wizardUpdate(t, m, findMsg[directChoicesLoadedMsg](t, cmd))
	if m.step != jumphostStepReview || m.directDB.Name != name || !m.directChoicesLoaded {
		t.Fatalf("step = %v db = %q loaded = %v, want the review of %s", m.step, m.directDB.Name, m.directChoicesLoaded, name)
	}
	return m
}

// historyWizard opens a wizard on the start screen of saved, with store wired.
func historyWizard(t *testing.T, runner historyRunner, store DirectDatabaseStore) jumphostWizardModel {
	t.Helper()
	m := newJumphostWizardModel(context.Background(), runner, jumphost.Options{})
	m.databases = store
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	return runChain(t, m, m.loadSavedCmd(), 1)
}

func TestDirectReviewShowsDefinition(t *testing.T) {
	m := directReview(t, fakeSessionRunner{}, &fakeDirectStore{dbs: testDirectDatabases()}, "legacy-billing")

	view := wizardView(m)
	for _, want := range []string{"DIRECT", "sqlserver", "legacy-billing", "PROD", "billing.db.internal:1433", "TLS require",
		"the login's default", "mcp_ro", "Password", "Remember", "MCP"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"Sign-in", "AWS ", "Port "} {
		if strings.Contains(view, unwanted) {
			t.Fatalf("view shows %q, which a direct database does not have:\n%s", unwanted, view)
		}
	}
	if !slices.Equal(m.reviewFields(), []int{reviewFieldPassword, reviewFieldRemember, reviewFieldMCPWrites}) {
		t.Fatalf("fields = %v, want password, remember and MCP writes", m.reviewFields())
	}
}

func TestDirectReviewWarnsSQLServerReadOnly(t *testing.T) {
	store := &fakeDirectStore{dbs: testDirectDatabases()}
	m := directReview(t, fakeSessionRunner{}, store, "legacy-billing")
	if view := wizardView(m); !strings.Contains(view, "Read-only rests on mcp_ro's permissions") {
		t.Fatalf("view lacks the SQL Server read-only warning:\n%s", view)
	}

	m, _ = wizardUpdate(t, m, key("tab"))
	m, _ = wizardUpdate(t, m, key("tab"))
	m, _ = wizardUpdate(t, m, key(" "))
	if view := wizardView(m); !m.mcpWrites || strings.Contains(view, "Read-only rests on") {
		t.Fatalf("writes = %v, want no read-only warning once writes are allowed:\n%s", m.mcpWrites, view)
	}

	pg := directReview(t, fakeSessionRunner{}, store, "analytics")
	if view := wizardView(pg); strings.Contains(view, "Read-only rests on") || !strings.Contains(view, "events") {
		t.Fatalf("a PostgreSQL database shows the warning, or not its database:\n%s", view)
	}
}

func TestDirectReviewPrefillsKeychainPasswordAndWrites(t *testing.T) {
	runner := &directKeychainRunner{prefsRunner: newPrefsRunner()}
	runner.saved["/legacy-billing"] = jumphost.ConnectionPrefs{DBUser: "mcp_ro", SignIn: jumphost.SignInPassword, PasswordInKeychain: true, MCPAllowWrites: true}

	m := directReview(t, runner, &fakeDirectStore{dbs: testDirectDatabases()}, "legacy-billing")
	if m.passwordInput.Value() != "from-keychain" || !m.remember || !m.mcpWrites {
		t.Fatalf("password = %q remember = %v writes = %v, want the remembered choices", m.passwordInput.Value(), m.remember, m.mcpWrites)
	}
	if strings.Contains(wizardView(m), "from-keychain") {
		t.Fatal("the prefilled password must stay masked")
	}
}

func TestDirectReviewConnectsWithPassword(t *testing.T) {
	runner := newPrefsRunner()
	m := directReview(t, runner, &fakeDirectStore{dbs: testDirectDatabases()}, "legacy-billing")

	m = typeText(t, m, "hunter2")
	m, _ = wizardUpdate(t, m, key("tab"))
	m, _ = wizardUpdate(t, m, key(" ")) // Remember on
	m, cmd := wizardUpdate(t, m, key("enter"))

	if !isQuit(cmd) {
		t.Fatal("enter did not connect")
	}
	got := m.options
	if got.ResourceType != "direct" || got.Name != "legacy-billing" || got.DBUser != "mcp_ro" || got.DBPassword != "hunter2" ||
		got.SignIn != jumphost.SignInPassword || !got.RememberPassword || got.MCPAllowWrites || got.DBSecretARN != "" {
		t.Fatalf("options = %+v, want a password connection to legacy-billing, remembered, read-only", got)
	}
	if runner.lastSave.Name != "legacy-billing" || runner.lastSave.ResourceType != "direct" || !runner.lastSave.RememberPassword {
		t.Fatalf("saved = %+v, want the choices of legacy-billing saved", *runner.lastSave)
	}
}

func TestDirectReviewRequiresPassword(t *testing.T) {
	m := directReview(t, fakeSessionRunner{}, &fakeDirectStore{dbs: testDirectDatabases()}, "legacy-billing")
	m, _ = wizardUpdate(t, m, key("tab"))

	m, cmd := wizardUpdate(t, m, key("enter"))
	if cmd != nil || m.reviewFocus != reviewFieldPassword || m.errorMsg != "Enter the password of mcp_ro" {
		t.Fatalf("cmd = %v focus = %d error = %q, want the password asked", cmd != nil, m.reviewFocus, m.errorMsg)
	}
}

func TestDirectReviewWithoutKeychainHidesRemember(t *testing.T) {
	runner := &noKeychainRunner{prefsRunner: newPrefsRunner()}
	m := directReview(t, runner, &fakeDirectStore{dbs: testDirectDatabases()}, "analytics")

	if slices.Contains(m.reviewFields(), reviewFieldRemember) || !strings.Contains(wizardView(m), "No OS keychain available") {
		t.Fatalf("fields = %v, want no Remember toggle:\n%s", m.reviewFields(), wizardView(m))
	}
}

func TestDirectReviewIgnoresChoicesOfAnotherDatabase(t *testing.T) {
	m := directReview(t, fakeSessionRunner{}, &fakeDirectStore{dbs: testDirectDatabases()}, "legacy-billing")

	m, _ = wizardUpdate(t, m, directChoicesLoadedMsg{name: "analytics", keychainPassword: "other", saved: true, keychainAvailable: true})
	if m.passwordInput.Value() != "" || !m.directChoicesLoaded || m.directDB.Name != "legacy-billing" {
		t.Fatalf("password = %q loaded = %v db = %q, want the choices of analytics ignored on the loaded review of legacy-billing",
			m.passwordInput.Value(), m.directChoicesLoaded, m.directDB.Name)
	}
}

func TestRecentDirectEntries(t *testing.T) {
	billing := testDirectDatabases()[1]
	connected := jumphost.SavedConnection{Ref: jumphost.DirectTargetRef("legacy-billing"), Prefs: jumphost.ConnectionPrefs{DBUser: "mcp_ro"}}
	gone := jumphost.SavedConnection{Ref: jumphost.DirectTargetRef("old-crm"), TargetMissing: true}
	unknown := jumphost.SavedConnection{Ref: jumphost.DirectTargetRef("reports")}
	p := newRecentPicker(NewStyles(true), []jumphost.SavedConnection{connected, gone, unknown},
		map[string]bool{directConnectionKey("legacy-billing"): true}, productionMatcher{"*"},
		map[string]directdb.Database{"legacy-billing": billing}, time.Now())

	items := p.list.Items()
	if len(items) != 3 {
		t.Fatalf("items = %d, want the three entries", len(items))
	}
	first := recentItemOf(t, items[0])
	if first.Description() != "direct · billing.db.internal:1433" || !first.connected || !first.isProduction() || first.isDimmed() {
		t.Fatalf("first = %q connected = %v prod = %v, want the connected production server", first.Description(), first.connected, first.isProduction())
	}
	second := recentItemOf(t, items[1])
	if second.Description() != "direct · definition removed" || !second.isDimmed() || second.isProduction() {
		t.Fatalf("second = %q dimmed = %v prod = %v, want a faded entry, never matched by profile patterns",
			second.Description(), second.isDimmed(), second.isProduction())
	}
	third := recentItemOf(t, items[2])
	if third.Description() != "direct" {
		t.Fatalf("third = %q, want just direct without definition read", third.Description())
	}
}

// recentItemOf is item as a start screen entry.
func recentItemOf(t *testing.T, item list.Item) recentItem {
	t.Helper()
	entry, ok := item.(recentItem)
	if !ok {
		t.Fatalf("item = %T, want a recentItem", item)
	}
	return entry
}

func TestWizardReopensRecentDirectDatabase(t *testing.T) {
	runner := newHistoryRunner(jumphost.SavedConnection{Ref: jumphost.DirectTargetRef("legacy-billing"), Prefs: jumphost.ConnectionPrefs{DBUser: "mcp_ro"}})
	m := historyWizard(t, runner, &fakeDirectStore{dbs: testDirectDatabases()})
	if view := wizardView(m); !strings.Contains(view, "direct · billing.db.internal:1433") {
		t.Fatalf("start screen lacks the direct entry:\n%s", view)
	}

	m, _ = wizardUpdate(t, m, key("enter"))
	if m.step != jumphostStepReview || m.flow != flowRecent || m.directDB.Name != "legacy-billing" || m.options.ResourceType != "direct" {
		t.Fatalf("step = %v flow = %v db = %q, want the review of legacy-billing", m.step, m.flow, m.directDB.Name)
	}
	if len(*runner.lookups) != 0 {
		t.Fatalf("lookups = %v, a direct database is never looked up in AWS", *runner.lookups)
	}

	m, _ = wizardUpdate(t, m, key("esc"))
	if m.step != jumphostStepStart {
		t.Fatalf("step = %v, want the start screen again", m.step)
	}
}

func TestWizardRecentDirectEntryWithoutDefinition(t *testing.T) {
	runner := newHistoryRunner(jumphost.SavedConnection{Ref: jumphost.DirectTargetRef("old-crm"), TargetMissing: true})
	m := historyWizard(t, runner, &fakeDirectStore{})

	m, _ = wizardUpdate(t, m, key("enter"))
	if m.step != jumphostStepStart || !strings.Contains(m.errorMsg, "no direct database named old-crm (see airlock connection list)") {
		t.Fatalf("step = %v error = %q, want the start screen with the error", m.step, m.errorMsg)
	}

	m, _ = wizardUpdate(t, m, key("d"))
	m, cmd := wizardUpdate(t, m, key("y"))
	runChain(t, m, cmd, 2)
	if len(*runner.saved) != 0 {
		t.Fatalf("saved = %v, want the entry forgotten", *runner.saved)
	}
}

func TestWizardOpenDirectStartsOnReview(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
	m.databases = &fakeDirectStore{dbs: testDirectDatabases()}
	m.openDirect = "legacy-billing"
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = pastStart(t, m)

	if m.step != jumphostStepReview || m.flow != flowNew || m.directDB.Name != "legacy-billing" || m.openDirect != "" {
		t.Fatalf("step = %v flow = %v db = %q open = %q, want the review of legacy-billing, once", m.step, m.flow, m.directDB.Name, m.openDirect)
	}
	if steps, current := m.breadcrumb(); !slices.Equal(steps, []string{"Type", "Target", "Connect"}) || current != 2 {
		t.Fatalf("breadcrumb = %v (%d), want Connect of a new direct connection", steps, current)
	}

	m, _ = wizardUpdate(t, m, key("esc"))
	if db, ok := m.directList.selected(); m.step != jumphostStepTarget || !ok || db.Name != "legacy-billing" {
		t.Fatalf("step = %v selected = %+v, want the list on legacy-billing", m.step, db)
	}
}

func TestWizardOpenDirectUnknownNameShowsError(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
	m.databases = &fakeDirectStore{dbs: testDirectDatabases()}
	m.openDirect = "nope"
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = wizardUpdate(t, m, jumphostSavedLoadedMsg{connections: []jumphost.SavedConnection{savedArchive}})

	if m.step != jumphostStepStart || m.errorMsg != "no direct database named nope (see airlock connection list)" {
		t.Fatalf("step = %v error = %q, want the start screen with the error", m.step, m.errorMsg)
	}
}

func TestDashboardOpensDirectOnlyAtStartup(t *testing.T) {
	m := newSSMModel(context.Background(), newFakeService(), fakeSessionRunner{}, jumphost.Options{})
	m.openDirect = "legacy-billing"

	m = m.startupWizard()
	if m.wizard.openDirect != "legacy-billing" {
		t.Fatalf("openDirect = %q, want the startup wizard on legacy-billing", m.wizard.openDirect)
	}
	m = m.showWizard(m.wizardDefaults())
	if m.wizard.openDirect != "" {
		t.Fatal("a later wizard (n) must start as usual")
	}
}

// connectDirectReview loads the remembered choices cmd brings, types password and connects.
func connectDirectReview(t *testing.T, m jumphostWizardModel, cmd tea.Cmd, password string) jumphostWizardModel {
	t.Helper()
	m, _ = wizardUpdate(t, m, findMsg[directChoicesLoadedMsg](t, cmd))
	if m.reviewFocus != reviewFieldPassword || !strings.Contains(wizardView(m), "Password") {
		t.Fatalf("focus = %d, want the password field of a direct review:\n%s", m.reviewFocus, wizardView(m))
	}
	m = typeText(t, m, password)
	m, cmd = wizardUpdate(t, m, key("enter"))
	if !isQuit(cmd) {
		t.Fatalf("enter did not connect: error = %q", m.errorMsg)
	}
	return m
}

func TestWizardRecentDirectReviewConnects(t *testing.T) {
	runner := newHistoryRunner(jumphost.SavedConnection{Ref: jumphost.DirectTargetRef("legacy-billing"), Prefs: jumphost.ConnectionPrefs{DBUser: "mcp_ro"}})
	m := historyWizard(t, runner, &fakeDirectStore{dbs: testDirectDatabases()})

	m, cmd := wizardUpdate(t, m, key("enter"))
	if view := wizardView(m); !strings.Contains(view, "billing.db.internal:1433") || strings.Contains(view, "Port ") {
		t.Fatalf("the recent entry does not show a direct review:\n%s", view)
	}
	m = connectDirectReview(t, m, cmd, "hunter2")
	if got := m.options; got.ResourceType != "direct" || got.Name != "legacy-billing" || got.DBUser != "mcp_ro" ||
		got.DBPassword != "hunter2" || got.SignIn != jumphost.SignInPassword {
		t.Fatalf("options = %+v, want a password connection to legacy-billing", got)
	}
}

func TestWizardOpenDirectReviewConnects(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
	m.databases = &fakeDirectStore{dbs: testDirectDatabases()}
	m.openDirect = "analytics"
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, cmd := wizardUpdate(t, m, jumphostSavedLoadedMsg{})

	m = connectDirectReview(t, m, cmd, "s3cret")
	if got := m.options; got.ResourceType != "direct" || got.Name != "analytics" || got.DBUser != "reader" || got.DBPassword != "s3cret" {
		t.Fatalf("options = %+v, want a password connection to analytics", got)
	}
}

func TestWizardOpenDirectWithHistoryGoesBackThroughNewConnection(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
	m.databases = &fakeDirectStore{dbs: testDirectDatabases()}
	m.openDirect = "legacy-billing"
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = wizardUpdate(t, m, jumphostSavedLoadedMsg{connections: []jumphost.SavedConnection{savedArchive}})
	if m.step != jumphostStepReview || !m.isDirect() {
		t.Fatalf("step = %v direct = %v, want the review of legacy-billing", m.step, m.isDirect())
	}

	for _, want := range []jumphostStep{jumphostStepTarget, jumphostStepType, jumphostStepStart} {
		m, _ = wizardUpdate(t, m, key("esc"))
		if m.step != want {
			t.Fatalf("step = %v, want %v: esc walks back the steps of a new direct connection", m.step, want)
		}
	}
}

func TestWizardRecentDirectEntryWithoutStore(t *testing.T) {
	runner := newHistoryRunner(jumphost.SavedConnection{Ref: jumphost.DirectTargetRef("legacy-billing")})
	m := historyWizard(t, runner, nil)

	m, cmd := wizardUpdate(t, m, key("enter"))
	if cmd != nil || m.step != jumphostStepStart || m.errorMsg != "direct databases are not available" {
		t.Fatalf("step = %v error = %q, want the start screen with the error", m.step, m.errorMsg)
	}
}

func TestWizardOpenDirectWithoutStore(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{})
	m.openDirect = "legacy-billing"
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = wizardUpdate(t, m, jumphostSavedLoadedMsg{connections: []jumphost.SavedConnection{savedArchive}})

	if m.step != jumphostStepStart || m.errorMsg != "direct databases are not available" {
		t.Fatalf("step = %v error = %q, want the start screen with the error", m.step, m.errorMsg)
	}
}

func TestDirectReviewUserDoesNotLeakIntoNextRDSReview(t *testing.T) {
	m := newJumphostWizardModel(context.Background(), fakeSessionRunner{}, jumphost.Options{AwsProfile: "dev"})
	m.databases = &fakeDirectStore{dbs: testDirectDatabases()}
	m, _ = wizardUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = pastStart(t, m)
	m, _ = wizardUpdate(t, m, key("4"))
	m.directList.selectName("legacy-billing")
	m, _ = wizardUpdate(t, m, key("enter"))
	if m.step != jumphostStepReview || m.options.DBUser != "mcp_ro" {
		t.Fatalf("step = %v user = %q, want the review of legacy-billing", m.step, m.options.DBUser)
	}

	m, _ = wizardUpdate(t, m, key("esc"))
	m, _ = wizardUpdate(t, m, key("esc"))
	if m.step != jumphostStepType {
		t.Fatalf("step = %v, want the type step", m.step)
	}
	m, _ = wizardUpdate(t, m, key("1")) // rds
	if m.awaitingRegion {
		m, _ = wizardUpdate(t, m, jumphostRegionResolvedMsg{profile: "dev", region: "eu-west-3"})
	}
	m, _ = wizardUpdate(t, m, jumphostTargetsLoadedMsg{targets: []jumphost.ResourceSummary{{Name: "archive", Engine: "postgres"}}})
	m, _ = wizardUpdate(t, m, key("enter"))
	m, _ = wizardUpdate(t, m, jumphostDefaultPortLoadedMsg{port: 5432, saved: true, prefs: jumphost.ConnectionPrefs{DBUser: "default_ro"}})

	if m.step != jumphostStepReview || m.userInput.Value() != "default_ro" {
		t.Fatalf("step = %v user = %q, want the user saved for the RDS target, not the direct database's", m.step, m.userInput.Value())
	}
	if m.options.DBUser != "" || m.options.DBPassword != "" || m.options.RememberPassword {
		t.Fatalf("options = %+v, want no direct-only option left over", m.options)
	}
}
