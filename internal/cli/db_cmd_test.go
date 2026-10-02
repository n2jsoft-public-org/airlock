package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/keychain"
)

// testDatabases points the config directory to a temporary one and the keychain to memory, and
// returns the store of the databases.json the commands use.
func testDatabases(t *testing.T) (*directdb.Store, *keychain.MemoryStore) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv(noUpdateCheckEnv, "1")
	memory := keychain.NewMemoryStore()
	previous := newKeychain
	newKeychain = func() keychain.Store { return memory }
	t.Cleanup(func() { newKeychain = previous })
	return directdb.NewStore(filepath.Join(root, "airlock", "databases.json")), memory
}

// testConnections is the connections.json store of the config directory of testDatabases.
func testConnections(t *testing.T) *jumphost.PrefsStore {
	t.Helper()
	return jumphost.NewPrefsStore(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "airlock", "connections.json"))
}

func putDatabase(t *testing.T, store *directdb.Store, db *directdb.Database) {
	t.Helper()
	if err := store.Put("", *db); err != nil {
		t.Fatal(err)
	}
}

func getStored(t *testing.T, store *directdb.Store, name string) (directdb.Database, bool) {
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

func TestDBAddWritesTheDatabase(t *testing.T) {
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
		Database: "Billing", User: "mcp_ro", TLS: directdb.TLSVerify, Production: true}
	if !found || db != want {
		t.Fatalf("stored = %+v (found %v), want %+v", db, found, want)
	}
}

func TestDBAddRefusesAnExistingName(t *testing.T) {
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

func TestDBAddRejectsInvalidValues(t *testing.T) {
	testDatabases(t)
	if _, err := runAirlock(t, "db", "add", "legacy-billing", "--engine", "mysql", "--host", "h", "--user", "u"); err == nil || !strings.HasPrefix(err.Error(), "engine:") {
		t.Fatalf("err = %v, want an engine field error", err)
	}
	if _, err := runAirlock(t, "db", "add", "Legacy_Billing", "--engine", "postgres", "--host", "h", "--user", "u"); err == nil || !strings.HasPrefix(err.Error(), "name:") {
		t.Fatalf("err = %v, want a name field error", err)
	}
}

func TestDBAddRequiresEngineHostAndUser(t *testing.T) {
	testDatabases(t)
	_, err := runAirlock(t, "db", "add", "legacy-billing")
	if err == nil || !strings.Contains(err.Error(), "required flag(s)") {
		t.Fatalf("err = %v, want the required flags", err)
	}
}

func TestDBEditChangesOnlyTheGivenFlags(t *testing.T) {
	store, _ := testDatabases(t)
	original := billing
	original.Port, original.Database, original.Production = 14330, "Billing", true
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

func TestDBEditRestoresTheDefaults(t *testing.T) {
	store, _ := testDatabases(t)
	original := billing
	original.Port, original.Database, original.Production = 14330, "Billing", true
	putDatabase(t, store, &original)

	if _, err := runAirlock(t, "db", "edit", "legacy-billing", "--port", "0", "--database", "", "--production=false"); err != nil {
		t.Fatal(err)
	}
	if db, _ := getStored(t, store, "legacy-billing"); db != billing {
		t.Fatalf("stored = %+v, want the defaults back: %+v", db, billing)
	}
}

func TestDBEditWithoutFlagsHasNothingToChange(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &billing)
	if _, err := runAirlock(t, "db", "edit", "legacy-billing"); err == nil || !strings.HasPrefix(err.Error(), "nothing to change") {
		t.Fatalf("err = %v, want nothing to change", err)
	}
}

func TestDBEditUnknownName(t *testing.T) {
	testDatabases(t)
	_, err := runAirlock(t, "db", "edit", "nope", "--host", "h")
	if err == nil || err.Error() != "no direct database named nope (see airlock db list)" {
		t.Fatalf("err = %v", err)
	}
}

func TestDBEditRejectsAnInvalidValue(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &billing)
	if _, err := runAirlock(t, "db", "edit", "legacy-billing", "--tls", "maybe"); err == nil || !strings.HasPrefix(err.Error(), "tls:") {
		t.Fatalf("err = %v, want a tls field error", err)
	}
	if db, _ := getStored(t, store, "legacy-billing"); db != billing {
		t.Fatalf("stored = %+v, want it unchanged", db)
	}
}

func TestDBEditRenameForgetsOldConnection(t *testing.T) {
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

func TestDBEditUserChangeForgetsOldConnection(t *testing.T) {
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

func TestDBEditConnectionChangeForgetsTheConnection(t *testing.T) {
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

func TestDBEditProductionOnlyKeepsTheConnection(t *testing.T) {
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

func TestDBListShowsTheDatabases(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &billing)
	putDatabase(t, store, &directdb.Database{Name: "archive", Engine: directdb.EnginePostgres, Host: "fd00::12", Port: 6432,
		Database: "archive", User: "reader", TLS: directdb.TLSDisable, Production: true})

	out, err := runAirlock(t, "db", "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("out = %q, want a header and two rows", out)
	}
	if got := strings.Fields(lines[0]); strings.Join(got, " ") != "NAME ENGINE ADDRESS DATABASE USER TLS PRODUCTION" {
		t.Fatalf("header = %q", lines[0])
	}
	if got := strings.Join(strings.Fields(lines[1]), " "); got != "archive postgres [fd00::12]:6432 archive reader disable yes" {
		t.Fatalf("archive row = %q", got)
	}
	if got := strings.Join(strings.Fields(lines[2]), " "); got != "legacy-billing sqlserver billing.db.internal:1433 (default) mcp_ro require" {
		t.Fatalf("legacy-billing row = %q", got)
	}
}

func TestDBListWithoutDatabase(t *testing.T) {
	testDatabases(t)
	out, err := runAirlock(t, "db", "list")
	if err != nil || !strings.HasPrefix(out, "No direct database: add one with `airlock db add") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestDBRemoveAsksFirst(t *testing.T) {
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

func TestDBRemoveForgetsTheConnection(t *testing.T) {
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

func TestDBRemoveYesSkipsTheQuestion(t *testing.T) {
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
