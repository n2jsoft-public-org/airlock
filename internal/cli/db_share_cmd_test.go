package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/keychain"
)

var (
	archive   = directdb.Database{Name: "archive", Engine: directdb.EnginePostgres, Host: "archive.internal", User: "reader"}
	reporting = directdb.Database{Name: "reporting", Engine: directdb.EnginePostgres, Host: "reporting.internal", Port: 6432, User: "bi"}
)

func exportString(t *testing.T, dbs ...directdb.Database) string {
	t.Helper()
	shared, err := directdb.Export(dbs)
	if err != nil {
		t.Fatal(err)
	}
	return shared
}

func TestDBExportPrintsOnlyTheString(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &archive)
	putDatabase(t, store, &billing)

	out, err := runAirlock(t, "db", "export")
	if err != nil {
		t.Fatal(err)
	}
	shared := strings.TrimSuffix(out, "\n")
	if !strings.HasPrefix(shared, "airlockdb1:") || strings.Contains(shared, "\n") {
		t.Fatalf("out = %q, want a single database string", out)
	}
	plan, err := directdb.PlanImport(nil, shared)
	if err != nil || len(plan.Changes) != 2 {
		t.Fatalf("plan = %+v, %v; want both databases", plan, err)
	}
}

func TestDBExportNamedDatabases(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &archive)
	putDatabase(t, store, &billing)

	out, err := runAirlock(t, "db", "export", "legacy-billing")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := directdb.PlanImport(nil, strings.TrimSpace(out))
	if err != nil || len(plan.Changes) != 1 || plan.Changes[0].Database.Name != "legacy-billing" {
		t.Fatalf("plan = %+v, %v; want legacy-billing only", plan, err)
	}
}

func TestDBExportUnknownName(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &archive)
	if _, err := runAirlock(t, "db", "export", "archive", "nope"); err == nil || err.Error() != "no direct database named nope (see airlock db list)" {
		t.Fatalf("err = %v", err)
	}
}

func TestDBImportShowsThePlanAndAbortsWithoutYes(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &archive)
	putDatabase(t, store, &billing)
	moved := billing
	moved.Host = "billing2.db.internal"
	shared := exportString(t, archive, moved, reporting)

	out, err := runAirlockWithInput(t, "n\n", "db", "import", shared)
	if err == nil || err.Error() != "not applied" {
		t.Fatalf("err = %v, want not applied", err)
	}
	for _, want := range []string{
		"Import into " + store.Path() + ":",
		"  = archive (unchanged)",
		"  ~ legacy-billing (changed): host billing.db.internal -> billing2.db.internal (remembered password will be forgotten)",
		"  + reporting (added)",
		"Apply these changes to " + store.Path() + "? [y/N] ",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("out lacks %q:\n%s", want, out)
		}
	}
	if db, _ := getStored(t, store, "legacy-billing"); db.Host != "billing.db.internal" {
		t.Fatalf("legacy-billing changed without confirmation: %+v", db)
	}
	if _, found := getStored(t, store, "reporting"); found {
		t.Fatal("reporting added without confirmation")
	}
}

func TestDBImportAppliesWithYes(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &archive)
	putDatabase(t, store, &billing)
	moved := billing
	moved.Host = "billing2.db.internal"

	out, err := runAirlock(t, "db", "import", exportString(t, moved, reporting), "--yes")
	if err != nil || strings.Contains(out, "[y/N]") || !strings.Contains(out, "Imported 2 databases into "+store.Path()) {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if db, _ := getStored(t, store, "legacy-billing"); db.Host != "billing2.db.internal" {
		t.Fatalf("legacy-billing = %+v", db)
	}
	if db, found := getStored(t, store, "reporting"); !found || db != reporting {
		t.Fatalf("reporting = %+v (found %v)", db, found)
	}
	if _, found := getStored(t, store, "archive"); !found {
		t.Fatal("an import never removes a database")
	}
}

func TestDBImportWithNothingToChange(t *testing.T) {
	store, _ := testDatabases(t)
	putDatabase(t, store, &archive)
	out, err := runAirlock(t, "db", "import", exportString(t, archive))
	if err != nil || !strings.Contains(out, "Nothing to change: "+store.Path()+" already has these databases.") {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestDBImportRefusesADamagedString(t *testing.T) {
	testDatabases(t)
	shared := exportString(t, archive)
	if _, err := runAirlock(t, "db", "import", shared[:len(shared)-3]); !errors.Is(err, directdb.ErrInvalidShare) {
		t.Fatalf("err = %v, want ErrInvalidShare", err)
	}
}

func TestDBImportConnectionChangeForgetsTheConnection(t *testing.T) {
	store, memory := testDatabases(t)
	putDatabase(t, store, &billing)
	putDatabase(t, store, &archive)
	rememberDirectPassword(t, memory, "legacy-billing", "mcp_ro")
	rememberDirectPassword(t, memory, "archive", "reader")
	moved, flagged := billing, archive
	moved.Host, moved.TLS = "evil.example", directdb.TLSDisable
	flagged.Production = true

	out, err := runAirlock(t, "db", "import", exportString(t, moved, flagged), "--yes")
	if err != nil {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if !strings.Contains(out, "  ~ archive (changed): production false -> true\n") {
		t.Fatalf("a production-only change must not announce a forgotten password:\n%s", out)
	}
	if _, found := testConnections(t).Load(jumphost.DirectTargetRef("legacy-billing")); found {
		t.Fatal("the prefs of legacy-billing must be forgotten")
	}
	if _, err := memory.Get("local/local/direct/legacy-billing/mcp_ro"); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("keychain entry of legacy-billing: err = %v, want ErrNotFound: the password must not reach the new host", err)
	}
	if password, err := memory.Get("local/local/direct/archive/reader"); err != nil || password != "s3cret" {
		t.Fatalf("keychain of archive = %q, %v; want it kept on a production-only change", password, err)
	}
}
