package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/connections"
	"github.com/n2jsoft-public-org/airlock/internal/directdb"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
	"github.com/n2jsoft-public-org/airlock/internal/keychain"
)

// testDatabases points the config directory to a temporary one and the keychain to memory, and
// returns the direct databases of the saved-connections.json the commands use.
func testDatabases(t *testing.T) (*connections.Databases, *keychain.MemoryStore) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv(noUpdateCheckEnv, "1")
	memory := keychain.NewMemoryStore()
	previous := newKeychain
	newKeychain = func() keychain.Store { return memory }
	t.Cleanup(func() { newKeychain = previous })
	return connections.NewStore(filepath.Join(root, "airlock", "saved-connections.json")).Databases(), memory
}

// testConnections is the prefs store of the saved-connections.json of testDatabases.
func testConnections(t *testing.T) *jumphost.PrefsStore {
	t.Helper()
	return jumphost.NewPrefsStore(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "airlock", "saved-connections.json"))
}

func putDatabase(t *testing.T, store *connections.Databases, db *directdb.Database) {
	t.Helper()
	if err := store.Put("", *db); err != nil {
		t.Fatal(err)
	}
}

func getStored(t *testing.T, store *connections.Databases, name string) (directdb.Database, bool) {
	t.Helper()
	db, found, err := store.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return db, found
}

// rememberDirectPassword saves the prefs and keychain entry a dashboard connection of name leaves behind.
func rememberDirectPassword(t *testing.T, memory *keychain.MemoryStore, name, user string) {
	t.Helper()
	prefs := &jumphost.ConnectionPrefs{ResourceType: "direct", DBUser: user, SignIn: jumphost.SignInPassword, PasswordInKeychain: true, UpdatedAt: time.Now()}
	if err := testConnections(t).Save(jumphost.DirectTargetRef(name), prefs); err != nil {
		t.Fatal(err)
	}
	if err := memory.Set("local/local/direct/"+name+"/"+user, "s3cret"); err != nil {
		t.Fatal(err)
	}
}

var billing = directdb.Database{Name: "legacy-billing", Engine: directdb.EngineSQLServer, Host: "billing.db.internal", User: "mcp_ro"}

func TestConnectionAddWritesTheDatabase(t *testing.T) {
	store, _ := testDatabases(t)
	out, err := runAirlock(t, "db", "add", "legacy-billing", "--engine", "sqlserver", "--host", "billing.db.internal",
		"--user", "mcp_ro", "--database", "Billing", "--tls", "verify", "--production")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Added legacy-billing (sqlserver, billing.db.internal:1433) to "+store.Path()) {
		t.Fatalf("out = %q", out)
	}
	db, found := getStored(t, store, "legacy-billing")
	want := directdb.Database{Name: "legacy-billing", Engine: directdb.EngineSQLServer, Host: "billing.db.internal",
		Database: "Billing", User: "mcp_ro", TLS: directdb.TLSVerify, Environment: "prod"}
	if !found || db != want {
		t.Fatalf("stored = %+v (found %v), want %+v", db, found, want)
	}
}

func TestConnectionAddMySQLDefaultsToPort3306(t *testing.T) {
	store, _ := testDatabases(t)
	for _, engine := range []string{"mysql", "mariadb"} {
		out, err := runAirlock(t, "db", "add", engine+"-shop", "--engine", engine, "--host", "shop.db.internal", "--user", "reader")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "Added "+engine+"-shop ("+engine+", shop.db.internal:3306)") {
			t.Fatalf("out = %q", out)
		}
		if db, found := getStored(t, store, engine+"-shop"); !found || db.Engine != directdb.Engine(engine) || db.Database != "" {
			t.Fatalf("stored = %+v (found %v)", db, found)
		}
	}
}

func TestConnectionAddRefusesAnExistingName(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &billing)
	_, err := runAirlock(t, "db", "add", "legacy-billing", "--engine", "postgres", "--host", "other.internal", "--user", "app")
	if !errors.Is(err, directdb.ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if db, _ := getStored(t, store, "legacy-billing"); db.Host != "billing.db.internal" {
		t.Fatalf("existing database changed: %+v", db)
	}
}

func TestConnectionAddRejectsInvalidValues(t *testing.T) {
	testDatabases(t)
	if _, err := runAirlock(t, "db", "add", "legacy-billing", "--engine", "oracle", "--host", "h", "--user", "u"); err == nil || !strings.HasPrefix(err.Error(), "engine:") {
		t.Fatalf("err = %v, want an engine field error", err)
	}
	if _, err := runAirlock(t, "db", "add", "Legacy_Billing", "--engine", "postgres", "--host", "h", "--user", "u"); err == nil || !strings.HasPrefix(err.Error(), "name:") {
		t.Fatalf("err = %v, want a name field error", err)
	}
}

func TestConnectionAddRequiresEngineHostAndUser(t *testing.T) {
	testDatabases(t)
	_, err := runAirlock(t, "db", "add", "legacy-billing")
	if err == nil || !strings.Contains(err.Error(), "required flag(s)") {
		t.Fatalf("err = %v, want the required flags", err)
	}
}

func TestConnectionEditChangesOnlyTheGivenFlags(t *testing.T) {
	store, _ := testDatabases(t)
	original := billing
	original.Port, original.Database, original.Environment = 14330, "Billing", "prod"
	putDatabase(t, store, &original)

	out, err := runAirlock(t, "db", "edit", "legacy-billing", "--host", "billing2.db.internal")
	if err != nil || !strings.Contains(out, "Updated legacy-billing in "+store.Path()) {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	want := original
	want.Host = "billing2.db.internal"
	if db, _ := getStored(t, store, "legacy-billing"); db != want {
		t.Fatalf("stored = %+v, want %+v", db, want)
	}
}

func TestConnectionEditRestoresTheDefaults(t *testing.T) {
	store, _ := testDatabases(t)
	original := billing
	original.Port, original.Database, original.Environment = 14330, "Billing", "prod"
	putDatabase(t, store, &original)

	if _, err := runAirlock(t, "db", "edit", "legacy-billing", "--port", "0", "--database", "", "--production=false"); err != nil {
		t.Fatal(err)
	}
	if db, _ := getStored(t, store, "legacy-billing"); db != billing {
		t.Fatalf("stored = %+v, want the defaults back: %+v", db, billing)
	}
}

func TestConnectionEditWithoutFlagsHasNothingToChange(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &billing)
	if _, err := runAirlock(t, "db", "edit", "legacy-billing"); err == nil || !strings.HasPrefix(err.Error(), "nothing to change") {
		t.Fatalf("err = %v, want nothing to change", err)
	}
}

func TestConnectionEditUnknownName(t *testing.T) {
	testDatabases(t)
	_, err := runAirlock(t, "db", "edit", "nope", "--host", "h")
	if err == nil || err.Error() != "no direct database named nope (see airlock connection list)" {
		t.Fatalf("err = %v", err)
	}
}

func TestConnectionEditRejectsAnInvalidValue(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &billing)
	if _, err := runAirlock(t, "db", "edit", "legacy-billing", "--tls", "maybe"); err == nil || !strings.HasPrefix(err.Error(), "tls:") {
		t.Fatalf("err = %v, want a tls field error", err)
	}
	if db, _ := getStored(t, store, "legacy-billing"); db != billing {
		t.Fatalf("stored = %+v, want it unchanged", db)
	}
}

func TestConnectionEditRenameForgetsOldConnection(t *testing.T) {
	store, memory := testDatabases(t)
	putDatabase(t, store, &billing)
	rememberDirectPassword(t, memory, "legacy-billing", "mcp_ro")

	out, err := runAirlock(t, "db", "edit", "legacy-billing", "--name", "billing")
	if err != nil || !strings.Contains(out, "Renamed legacy-billing to billing in "+store.Path()) {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if _, found := getStored(t, store, "legacy-billing"); found {
		t.Fatal("the old name must be gone")
	}
	if db, found := getStored(t, store, "billing"); !found || db.Host != "billing.db.internal" {
		t.Fatalf("renamed = %+v (found %v)", db, found)
	}
	if _, found := testConnections(t).Load(jumphost.DirectTargetRef("legacy-billing")); found {
		t.Fatal("the prefs of the old name must be forgotten")
	}
	if _, err := memory.Get("local/local/direct/legacy-billing/mcp_ro"); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry of the old name: err = %v, want ErrNotFound", err)
	}
}

func TestConnectionEditUserChangeForgetsOldConnection(t *testing.T) {
	store, memory := testDatabases(t)
	putDatabase(t, store, &billing)
	rememberDirectPassword(t, memory, "legacy-billing", "mcp_ro")

	out, err := runAirlock(t, "db", "edit", "legacy-billing", "--user", "other")
	if err != nil || !strings.Contains(out, "Updated legacy-billing in "+store.Path()) {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if db, _ := getStored(t, store, "legacy-billing"); db.User != "other" {
		t.Fatalf("stored = %+v, want the new user", db)
	}
	if _, found := testConnections(t).Load(jumphost.DirectTargetRef("legacy-billing")); found {
		t.Fatal("the prefs of the former user must be forgotten")
	}
	if _, err := memory.Get("local/local/direct/legacy-billing/mcp_ro"); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry of the former user: err = %v, want ErrNotFound", err)
	}
}

func TestConnectionEditConnectionChangeForgetsTheConnection(t *testing.T) {
	for name, flags := range map[string][]string{
		"host":     {"--host", "billing2.db.internal"},
		"port":     {"--port", "1444"},
		"tls":      {"--tls", "disable"},
		"engine":   {"--engine", "postgres"},
		"database": {"--database", "billing"},
	} {
		store, memory := testDatabases(t)
		putDatabase(t, store, &billing)
		rememberDirectPassword(t, memory, "legacy-billing", "mcp_ro")

		out, err := runAirlock(t, append([]string{"db", "edit", "legacy-billing"}, flags...)...)
		if err != nil || !strings.Contains(out, "Updated legacy-billing in "+store.Path()) {
			t.Fatalf("%s: out = %q, err = %v", name, out, err)
		}
		if _, found := testConnections(t).Load(jumphost.DirectTargetRef("legacy-billing")); found {
			t.Fatalf("%s: the prefs must be forgotten", name)
		}
		if _, err := memory.Get("local/local/direct/legacy-billing/mcp_ro"); !errors.Is(err, keychain.ErrNotFound) {
			t.Fatalf("%s: keychain entry: err = %v, want ErrNotFound: the password must not reach the new server", name, err)
		}
	}
}

func TestConnectionEditProductionOnlyKeepsTheConnection(t *testing.T) {
	store, memory := testDatabases(t)
	putDatabase(t, store, &billing)
	rememberDirectPassword(t, memory, "legacy-billing", "mcp_ro")

	if _, err := runAirlock(t, "db", "edit", "legacy-billing", "--production"); err != nil {
		t.Fatal(err)
	}
	if _, found := testConnections(t).Load(jumphost.DirectTargetRef("legacy-billing")); !found {
		t.Fatal("the prefs must be kept")
	}
	if password, err := memory.Get("local/local/direct/legacy-billing/mcp_ro"); err != nil || password != "s3cret" {
		t.Fatalf("keychain = %q, %v; want the password kept", password, err)
	}
}

// rememberJumphost saves a connection of user app_ro to target through a jumphost, as a dashboard connection does.
func rememberJumphost(t *testing.T, store *connections.Databases, target *connections.JumphostTarget) {
	t.Helper()
	if err := store.Store().RememberJumphost(*target, connections.Choices{User: "app_ro"}); err != nil {
		t.Fatal(err)
	}
}

var billingRDS = connections.JumphostTarget{Account: "123456789012", Region: "eu-west-3", Type: "rds", Target: "billing-db", Profile: "prod"}

func TestConnectionListShowsEveryConnection(t *testing.T) {
	store, _ := testDatabases(t)
	writeEnvironments(t, "prod!=prod*; staging=*stg*")
	putDatabase(t, store, &billing)
	putDatabase(t, store, &directdb.Database{Name: "archive", Engine: directdb.EnginePostgres, Host: "fd00::12", Port: 6432,
		User: "reader", Environment: "prod"})
	rememberJumphost(t, store, &billingRDS)

	out, err := runAirlock(t, "connection", "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("out = %q, want a header and three rows", out)
	}
	if got := strings.Join(strings.Fields(lines[0]), " "); got != "NAME VIA TYPE TARGET USER LAST USED ENV" {
		t.Fatalf("header = %q", got)
	}
	if got := strings.Fields(lines[1]); strings.Join(got[:6], " ") != "rds/billing-db jumphost rds billing-db @ 123456789012/eu-west-3" || got[6] != "app_ro" || got[9] != "prod" {
		t.Fatalf("first row = %q, want the used jumphost connection first", lines[1])
	}
	if got := strings.Join(strings.Fields(lines[2]), " "); got != "archive direct postgres [fd00::12]:6432 reader never prod" {
		t.Fatalf("archive row = %q", got)
	}
	if got := strings.Join(strings.Fields(lines[3]), " "); got != "legacy-billing direct sqlserver billing.db.internal:1433 mcp_ro never" {
		t.Fatalf("legacy-billing row = %q", got)
	}

	direct, err := runAirlock(t, "connection", "list", "--direct")
	if err != nil || strings.Contains(direct, "billing-db @") || !strings.Contains(direct, "legacy-billing") {
		t.Fatalf("--direct = %q, %v", direct, err)
	}
	jumphostOnly, err := runAirlock(t, "db", "list", "--jumphost")
	if err != nil || strings.Contains(jumphostOnly, "legacy-billing") || !strings.Contains(jumphostOnly, "rds/billing-db") {
		t.Fatalf("db list --jumphost = %q, %v", jumphostOnly, err)
	}
}

func TestConnectionListShowsAnInvalidEntry(t *testing.T) {
	store, _ := testDatabases(t)
	writeTestFile(t, store.Path(), `{"version": 1, "connections": {"typo-db": {"engine": "postgres", "hots": "x", "user": "u"}}}`)
	out, err := runAirlock(t, "connection", "list")
	if err != nil || !strings.Contains(out, "typo-db") || !strings.Contains(out, "invalid:") || !strings.Contains(out, "hots") {
		t.Fatalf("out = %q, err = %v; want the invalid entry with its error", out, err)
	}
}

func TestConnectionListWithoutConnection(t *testing.T) {
	testDatabases(t)
	out, err := runAirlock(t, "connection", "list")
	if err != nil || !strings.HasPrefix(out, "No saved connection: add a direct database with `airlock connection add") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestConnectionRemoveAJumphostConnection(t *testing.T) {
	store, memory := testDatabases(t)
	rememberJumphost(t, store, &billingRDS)
	other := billingRDS
	other.Target = "archive-db"
	rememberJumphost(t, store, &other)
	if err := memory.Set("123456789012/eu-west-3/rds/billing-db/app_ro", "s3cret"); err != nil {
		t.Fatal(err)
	}
	if err := store.Store().RememberJumphost(billingRDS, connections.Choices{User: "app_ro", SignIn: "password", PasswordInKeychain: true}); err != nil {
		t.Fatal(err)
	}

	out, err := runAirlockWithInput(t, "y\n", "connection", "remove", "rds/billing-db")
	if err != nil || !strings.Contains(out, "Remove rds billing-db (123456789012/eu-west-3)? [y/N] ") || !strings.Contains(out, "Removed rds/billing-db from "+store.Path()) {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if _, found, err := store.Store().Get("123456789012/eu-west-3/rds/billing-db"); err != nil || found {
		t.Fatalf("the connection must be removed: found = %v, err = %v", found, err)
	}
	if _, found, err := store.Store().Get("123456789012/eu-west-3/rds/archive-db"); err != nil || !found {
		t.Fatalf("the other connection must be kept: found = %v, err = %v", found, err)
	}
	if _, err := memory.Get("123456789012/eu-west-3/rds/billing-db/app_ro"); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry: err = %v, want ErrNotFound", err)
	}
}

func TestConnectionRemoveAmbiguousShortName(t *testing.T) {
	store, _ := testDatabases(t)
	rememberJumphost(t, store, &billingRDS)
	other := billingRDS
	other.Region = "eu-west-1"
	rememberJumphost(t, store, &other)

	_, err := runAirlock(t, "connection", "remove", "rds/billing-db", "--yes")
	if err == nil || !strings.Contains(err.Error(), "rds/billing-db matches several connections") ||
		!strings.Contains(err.Error(), "123456789012/eu-west-1/rds/billing-db") || !strings.Contains(err.Error(), "123456789012/eu-west-3/rds/billing-db") {
		t.Fatalf("err = %v, want both keys listed", err)
	}
	if out, err := runAirlock(t, "connection", "remove", "123456789012/eu-west-1/rds/billing-db", "--yes"); err != nil || !strings.Contains(out, "Removed") {
		t.Fatalf("remove by key: out = %q, err = %v", out, err)
	}
}

func TestConnectionRemoveUnknownName(t *testing.T) {
	testDatabases(t)
	if _, err := runAirlock(t, "connection", "remove", "nope", "--yes"); err == nil || err.Error() != "no saved connection named nope (see airlock connection list)" {
		t.Fatalf("err = %v", err)
	}
}

func TestConnectionRemoveAnInvalidEntry(t *testing.T) {
	store, _ := testDatabases(t)
	writeTestFile(t, store.Path(), `{"version": 1, "connections": {"typo-db": {"hots": "x"}}}`)
	if out, err := runAirlock(t, "connection", "remove", "typo-db", "--yes"); err != nil || !strings.Contains(out, "Removed typo-db") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestConnectionRemoveAnInvalidEntryForgetsItsPassword(t *testing.T) {
	store, memory := testDatabases(t)
	writeTestFile(t, store.Path(), invalidTraceDB)
	if err := memory.Set(traceDBKeychainAccount, "s3cret"); err != nil {
		t.Fatal(err)
	}
	if out, err := runAirlock(t, "connection", "remove", "trace-db", "--yes"); err != nil || !strings.Contains(out, "Removed trace-db") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if _, err := memory.Get(traceDBKeychainAccount); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry of trace-db: err = %v, want ErrNotFound: it must not wait for the next trace-db", err)
	}

	// The next trace-db, on another host with the same user, never gets the former password.
	if _, err := runAirlock(t, "connection", "add", "trace-db", "--engine", "postgres", "--host", "other.example", "--user", "tracer"); err != nil {
		t.Fatal(err)
	}
	calls := stubDirectSignIn(t, nil)
	if _, err := runAirlockWithInput(t, "typed\n", "connection", "test", "trace-db"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*calls, ",") != "trace-db:typed" {
		t.Fatalf("calls = %q, want the typed password", *calls)
	}
}

func TestConnectionRemoveKeepsAnInvalidEntryWhenItsPasswordStays(t *testing.T) {
	store, memory := testDatabases(t)
	writeTestFile(t, store.Path(), invalidTraceDB)
	if err := memory.Set(traceDBKeychainAccount, "s3cret"); err != nil {
		t.Fatal(err)
	}
	newKeychain = func() keychain.Store { return undeletableKeychain{memory} }

	if _, err := runAirlock(t, "connection", "remove", "trace-db", "--yes"); err == nil || !strings.Contains(err.Error(), "not removed") {
		t.Fatalf("err = %v, want not removed", err)
	}
	if entry, found, err := store.Store().Get("trace-db"); err != nil || !found || entry.Invalid == nil {
		t.Fatalf("trace-db = %+v (found %v, err %v), want the invalid entry kept", entry, found, err)
	}
}

func TestConnectionEditAndTestRefuseAJumphostConnection(t *testing.T) {
	store, _ := testDatabases(t)
	rememberJumphost(t, store, &billingRDS)
	for _, args := range [][]string{{"connection", "edit", "rds/billing-db", "--port", "1"}, {"connection", "test", "rds/billing-db"}, {"connection", "export", "rds/billing-db"}} {
		if _, err := runAirlock(t, args...); err == nil || err.Error() != "rds/billing-db is reached through a jumphost: it is updated at each connection" {
			t.Fatalf("%v: err = %v", args, err)
		}
	}
}

func TestConnectionRemoveKeepsTheDirectEntryWhenForgettingFails(t *testing.T) {
	store, memory := testDatabases(t)
	putDatabase(t, store, &billing)
	rememberDirectPassword(t, memory, "legacy-billing", "mcp_ro")
	newKeychain = func() keychain.Store { return undeletableKeychain{memory} }

	_, err := runAirlock(t, "connection", "remove", "legacy-billing", "--yes")
	if err == nil || !strings.Contains(err.Error(), "not removed") {
		t.Fatalf("err = %v, want not removed", err)
	}
	if _, found := getStored(t, store, "legacy-billing"); !found {
		t.Fatal("the definition must be kept")
	}
	if prefs, found := testConnections(t).Load(jumphost.DirectTargetRef("legacy-billing")); !found || !prefs.PasswordInKeychain {
		t.Fatalf("prefs = %+v (found %v), want them kept", prefs, found)
	}
}

func TestConnectionRemoveKeepsTheJumphostEntryWhenForgettingFails(t *testing.T) {
	store, memory := testDatabases(t)
	if err := store.Store().RememberJumphost(billingRDS, connections.Choices{User: "app_ro", SignIn: "password", PasswordInKeychain: true}); err != nil {
		t.Fatal(err)
	}
	if err := memory.Set("123456789012/eu-west-3/rds/billing-db/app_ro", "s3cret"); err != nil {
		t.Fatal(err)
	}
	newKeychain = func() keychain.Store { return undeletableKeychain{memory} }

	_, err := runAirlock(t, "connection", "remove", "rds/billing-db", "--yes")
	if err == nil || !strings.Contains(err.Error(), "not removed") {
		t.Fatalf("err = %v, want not removed", err)
	}
	entry, found, err := store.Store().Get("123456789012/eu-west-3/rds/billing-db")
	if err != nil || !found || !entry.Choices.PasswordInKeychain {
		t.Fatalf("entry = %+v (found %v, err %v), want it and its choices kept", entry, found, err)
	}
}

func TestDBIsAnAliasOfConnection(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &billing)
	out, err := runAirlock(t, "db", "list")
	if err != nil || !strings.Contains(out, "legacy-billing") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestConnectionRemoveAsksFirst(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &billing)
	out, err := runAirlockWithInput(t, "n\n", "db", "remove", "legacy-billing")
	if err == nil || err.Error() != "not removed" {
		t.Fatalf("err = %v, want not removed", err)
	}
	if !strings.Contains(out, "Remove direct database legacy-billing (billing.db.internal:1433)? [y/N] ") {
		t.Fatalf("out = %q, want the question", out)
	}
	if _, found := getStored(t, store, "legacy-billing"); !found {
		t.Fatal("the database must be kept")
	}
}

func TestConnectionRemoveForgetsTheConnection(t *testing.T) {
	store, memory := testDatabases(t)
	putDatabase(t, store, &billing)
	rememberDirectPassword(t, memory, "legacy-billing", "mcp_ro")

	out, err := runAirlockWithInput(t, "y\n", "db", "remove", "legacy-billing")
	if err != nil || !strings.Contains(out, "Removed legacy-billing from "+store.Path()) {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if _, found := getStored(t, store, "legacy-billing"); found {
		t.Fatal("the database must be removed")
	}
	if _, found := testConnections(t).Load(jumphost.DirectTargetRef("legacy-billing")); found {
		t.Fatal("its prefs must be forgotten")
	}
	if _, err := memory.Get("local/local/direct/legacy-billing/mcp_ro"); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry: err = %v, want ErrNotFound", err)
	}
}

func TestConnectionRemoveYesSkipsTheQuestion(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &billing)
	out, err := runAirlock(t, "db", "remove", "legacy-billing", "--yes")
	if err != nil || strings.Contains(out, "[y/N]") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if _, found := getStored(t, store, "legacy-billing"); found {
		t.Fatal("the database must be removed")
	}
}

// undeletableKeychain is a keychain whose entries cannot be deleted.
type undeletableKeychain struct{ *keychain.MemoryStore }

func (undeletableKeychain) Delete(string) error { return errors.New("keychain locked") }

func TestConnectionEditLeavesTheDefinitionWhenTheConnectionCannotBeForgotten(t *testing.T) {
	store, memory := testDatabases(t)
	putDatabase(t, store, &billing)
	rememberDirectPassword(t, memory, "legacy-billing", "mcp_ro")
	newKeychain = func() keychain.Store { return undeletableKeychain{memory} }

	_, err := runAirlock(t, "db", "edit", "legacy-billing", "--host", "other.db.internal")
	if err == nil || !strings.Contains(err.Error(), "not updated") {
		t.Fatalf("err = %v, want not updated", err)
	}
	if db, _ := getStored(t, store, "legacy-billing"); db.Host != "billing.db.internal" {
		t.Fatalf("stored = %+v, want the definition unchanged", db)
	}
	if prefs, found := testConnections(t).Load(jumphost.DirectTargetRef("legacy-billing")); !found || !prefs.PasswordInKeychain {
		t.Fatalf("prefs = %+v (found %v), want them kept so a retry deletes the password", prefs, found)
	}
}

func TestConnectionEditRenameOntoAnExistingNameKeepsTheConnection(t *testing.T) {
	store, memory := testDatabases(t)
	putDatabase(t, store, &billing)
	other := billing
	other.Name = "billing"
	putDatabase(t, store, &other)
	rememberDirectPassword(t, memory, "legacy-billing", "mcp_ro")

	_, err := runAirlock(t, "db", "edit", "legacy-billing", "--name", "billing")
	if !errors.Is(err, directdb.ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if _, found := getStored(t, store, "legacy-billing"); !found {
		t.Fatal("the source must stay")
	}
	if prefs, found := testConnections(t).Load(jumphost.DirectTargetRef("legacy-billing")); !found || !prefs.PasswordInKeychain {
		t.Fatalf("prefs = %+v (found %v), want them kept", prefs, found)
	}
	if _, err := memory.Get("local/local/direct/legacy-billing/mcp_ro"); err != nil {
		t.Fatalf("the password must stay: %v", err)
	}
}

// writeEnvironments sets the environments of the test's config.json.
func writeEnvironments(t *testing.T, value string) {
	t.Helper()
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "airlock", "config.json")
	writeTestFile(t, path, `{"environments": "`+value+`"}`)
}

func TestConnectionAddEnvironmentIgnoresCase(t *testing.T) {
	store, _ := testDatabases(t)
	writeEnvironments(t, "prod!=*prod*; staging=*stg*")
	if _, err := runAirlock(t, "db", "add", "orders", "--engine", "postgres", "--host", "h", "--user", "u", "--environment", " Staging "); err != nil {
		t.Fatal(err)
	}
	if db, _ := getStored(t, store, "orders"); db.Environment != "staging" {
		t.Fatalf("Environment = %q, want staging", db.Environment)
	}
}

func TestConnectionAddStoresTheProductionAsProd(t *testing.T) {
	store, _ := testDatabases(t)
	writeEnvironments(t, "live!=*-live; dev=*dev*")
	if _, err := runAirlock(t, "db", "add", "orders", "--engine", "postgres", "--host", "h", "--user", "u", "--environment", "LIVE"); err != nil {
		t.Fatal(err)
	}
	if db, _ := getStored(t, store, "orders"); db.Environment != "prod" {
		t.Fatalf("Environment = %q, want prod", db.Environment)
	}
}

func TestConnectionAddRefusesAnUnknownEnvironment(t *testing.T) {
	testDatabases(t)
	writeEnvironments(t, "prod!=*prod*; staging=*stg*")
	_, err := runAirlock(t, "db", "add", "orders", "--engine", "postgres", "--host", "h", "--user", "u", "--environment", "qa")
	if err == nil || !strings.Contains(err.Error(), "prod, staging") {
		t.Fatalf("err = %v, want the configured environments listed", err)
	}
}

func TestConnectionEditEnvironmentAndTheProductionAlias(t *testing.T) {
	store, _ := testDatabases(t)
	writeEnvironments(t, "live!=*-live; dev=*dev*")
	putDatabase(t, store, &billing)
	for _, step := range []struct {
		want string
		args []string
	}{
		{"prod", []string{"--environment", "prod"}},
		{"dev", []string{"--environment", "dev"}},
		{"", []string{"--environment", ""}},
		{"prod", []string{"--production"}},
		{"", []string{"--production=false"}},
	} {
		if _, err := runAirlock(t, append([]string{"db", "edit", "legacy-billing"}, step.args...)...); err != nil {
			t.Fatalf("%v: %v", step.args, err)
		}
		if db, _ := getStored(t, store, "legacy-billing"); db.Environment != step.want {
			t.Fatalf("%v: Environment = %q, want %q", step.args, db.Environment, step.want)
		}
	}
	if _, err := runAirlock(t, "db", "edit", "legacy-billing", "--environment", "dev", "--production"); err == nil {
		t.Fatal("--environment and --production together must be refused")
	}
}

func TestConnectionEditKeepsAnEnvironmentTheSettingLacks(t *testing.T) {
	store, _ := testDatabases(t)
	writeEnvironments(t, "prod!=*prod*")
	putDatabase(t, store, &directdb.Database{Name: "orders", Engine: directdb.EnginePostgres, Host: "h", User: "u", Environment: "qa"})
	if _, err := runAirlock(t, "db", "edit", "orders", "--port", "6432"); err != nil {
		t.Fatal(err)
	}
	if db, _ := getStored(t, store, "orders"); db.Environment != "qa" || db.Port != 6432 {
		t.Fatalf("Environment = %q, Port = %d, want qa and 6432", db.Environment, db.Port)
	}
	if _, err := runAirlock(t, "db", "edit", "orders", "--environment", "qa"); err == nil {
		t.Fatal("--environment qa must be refused: the setting lacks it")
	}
}
