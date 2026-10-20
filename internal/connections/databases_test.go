package connections

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/directdb"
)

func testDatabase(name string) directdb.Database {
	return directdb.Database{Name: name, Engine: directdb.EnginePostgres, Host: name + ".db.internal", User: "reader"}
}

func TestDatabasesPutGetListRoundTrip(t *testing.T) {
	store := newTestStore(t)
	dbs := store.Databases()
	billing := directdb.Database{Name: "legacy-billing", Engine: directdb.EngineSQLServer, Host: "billing.db.internal", User: "mcp_ro", Environment: "prod"}
	for _, db := range []directdb.Database{billing, testDatabase("archive")} {
		if err := dbs.Put("", db); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RememberJumphost(billingTarget, Choices{}); err != nil {
		t.Fatal(err)
	}

	list, err := dbs.List()
	if err != nil || !reflect.DeepEqual(list, []directdb.Database{testDatabase("archive"), billing}) {
		t.Fatalf("List = %+v, %v; want archive then legacy-billing, without the jumphost entry", list, err)
	}
	got, found, err := dbs.Get("legacy-billing")
	if err != nil || !found || got != billing {
		t.Fatalf("Get = %+v, %v, %v", got, found, err)
	}
	if _, found, err := dbs.Get(billingKey); err != nil || found {
		t.Fatalf("a jumphost key is no direct database: found %v, err %v", found, err)
	}
	if dbs.Path() != store.Path() || dbs.Store() != store {
		t.Fatal("the view must work on its store")
	}
	data := readStoreFile(t, store)
	for _, want := range []string{`"legacy-billing": {`, `"engine": "sqlserver"`, `"host": "billing.db.internal"`, `"user": "mcp_ro"`, `"environment": "prod"`} {
		if !strings.Contains(data, want) {
			t.Fatalf("file lacks %s:\n%s", want, data)
		}
	}
}

func TestDatabasesPutNormalizesAndValidates(t *testing.T) {
	dbs := newTestStore(t).Databases()
	if err := dbs.Put("", directdb.Database{Name: " archive ", Engine: "POSTGRES", Host: " db ", User: " reader "}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := dbs.Get("archive"); err != nil {
		t.Fatal(err)
	} else if got.Engine != directdb.EnginePostgres || got.Host != "db" || got.User != "reader" {
		t.Fatalf("stored = %+v, want it normalized", got)
	}
	var fieldErr *directdb.FieldError
	if err := dbs.Put("", directdb.Database{Name: "bad", Engine: "oracle", Host: "db", User: "u"}); !errors.As(err, &fieldErr) || fieldErr.Field != "engine" {
		t.Fatalf("err = %v, want an engine field error", err)
	}
}

func TestDatabasesPutKeepsTheChoicesOnlyWhileTheConnectionIsTheSame(t *testing.T) {
	store := newTestStore(t)
	dbs := store.Databases()
	if err := dbs.Put("", testDatabase("analytics")); err != nil {
		t.Fatal(err)
	}
	choices := Choices{SignIn: "password", PasswordInKeychain: true}
	if err := store.RememberDirect("analytics", choices); err != nil {
		t.Fatal(err)
	}

	production := testDatabase("analytics")
	production.Environment = "prod"
	if err := dbs.Put("analytics", production); err != nil {
		t.Fatal(err)
	}
	if entry, _, err := store.Get("analytics"); err != nil {
		t.Fatal(err)
	} else if withoutDate(&entry.Choices) != choices || entry.Direct.Environment != "prod" {
		t.Fatalf("entry = %+v, want the choices kept on a production-only change", entry)
	}

	moved := production
	moved.Host = "elsewhere.db.internal"
	if err := dbs.Put("analytics", moved); err != nil {
		t.Fatal(err)
	}
	if entry, _, err := store.Get("analytics"); err != nil {
		t.Fatal(err)
	} else if entry.Choices != (Choices{}) || entry.Direct.Host != "elsewhere.db.internal" {
		t.Fatalf("entry = %+v, want no choices once the host changed", entry)
	}

	if err := store.RememberDirect("analytics", choices); err != nil {
		t.Fatal(err)
	}
	renamed := moved
	renamed.Name = "events"
	if err := dbs.Put("analytics", renamed); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get("analytics"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("the former name must be gone")
	}
	if entry, _, err := store.Get("events"); err != nil {
		t.Fatal(err)
	} else if entry.Choices != (Choices{}) {
		t.Fatalf("events = %+v, want no choices: a rename changes the connection", entry)
	}
}

func TestDatabasesPutRefusesTakenOrUnknownNames(t *testing.T) {
	store := newTestStore(t)
	writeStoreFile(t, store, `{"version": 1, "connections": {"typo-db": {"hots": "x"}}}`)
	dbs := store.Databases()
	for _, name := range []string{"billing", "archive"} {
		if err := dbs.Put("", testDatabase(name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := dbs.Put("", testDatabase("billing")); !errors.Is(err, directdb.ErrExists) {
		t.Fatalf("add over billing: err = %v, want ErrExists", err)
	}
	if err := dbs.Put("billing", testDatabase("archive")); !errors.Is(err, directdb.ErrExists) {
		t.Fatalf("rename onto archive: err = %v, want ErrExists", err)
	}
	if err := dbs.Put("", testDatabase("typo-db")); !errors.Is(err, directdb.ErrExists) {
		t.Fatalf("add over an invalid entry: err = %v, want ErrExists", err)
	}
	if err := dbs.Put("nope", testDatabase("nope")); !errors.Is(err, directdb.ErrNotFound) {
		t.Fatalf("unknown previous name: err = %v, want ErrNotFound", err)
	}
}

func TestDatabasesGetReportsAnInvalidEntry(t *testing.T) {
	store := newTestStore(t)
	writeStoreFile(t, store, `{"version": 1, "connections": {"typo-db": {"engine": "postgres", "hots": "x", "user": "u"}}}`)
	dbs := store.Databases()
	if _, found, err := dbs.Get("typo-db"); found || err == nil || !strings.Contains(err.Error(), "hots") {
		t.Fatalf("Get = found %v, err %v; want the entry's error", found, err)
	}
	if list, err := dbs.List(); err != nil || len(list) != 0 {
		t.Fatalf("List = %+v, %v; want the invalid entry left out", list, err)
	}
}

func TestDatabasesPutAllKeepsChoicesAndOtherEntries(t *testing.T) {
	store := newTestStore(t)
	dbs := store.Databases()
	for _, name := range []string{"analytics", "archive"} {
		if err := dbs.Put("", testDatabase(name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RememberDirect("analytics", Choices{SignIn: "password"}); err != nil {
		t.Fatal(err)
	}
	changed := testDatabase("analytics")
	changed.Environment = "prod"
	if err := dbs.PutAll([]directdb.Database{changed, testDatabase("events")}); err != nil {
		t.Fatal(err)
	}

	list, err := dbs.List()
	if err != nil || len(list) != 3 || list[0].Environment != "prod" || list[1].Name != "archive" || list[2].Name != "events" {
		t.Fatalf("List = %+v, %v; want analytics changed, archive kept, events added", list, err)
	}
	if entry, _, err := store.Get("analytics"); err != nil {
		t.Fatal(err)
	} else if entry.Choices.SignIn != "password" {
		t.Fatalf("analytics choices = %+v, want them kept on a production-only change", entry.Choices)
	}
	if err := dbs.PutAll([]directdb.Database{{Name: "bad", Engine: "oracle"}}); err == nil || !strings.Contains(err.Error(), `database "bad"`) {
		t.Fatalf("err = %v, want the invalid database named", err)
	}
}

func TestDatabasesPutAllDropsTheChoicesOfAChangedConnection(t *testing.T) {
	store := newTestStore(t)
	dbs := store.Databases()
	if err := dbs.Put("", testDatabase("analytics")); err != nil {
		t.Fatal(err)
	}
	if err := store.RememberDirect("analytics", Choices{SignIn: "password", PasswordInKeychain: true}); err != nil {
		t.Fatal(err)
	}
	moved := testDatabase("analytics")
	moved.Host = "evil.example"
	if err := dbs.PutAll([]directdb.Database{moved}); err != nil {
		t.Fatal(err)
	}
	if entry, _, err := store.Get("analytics"); err != nil {
		t.Fatal(err)
	} else if entry.Choices != (Choices{}) || entry.Direct.Host != "evil.example" {
		t.Fatalf("analytics = %+v, want the new host without the choices of the former one", entry)
	}
}

func TestDatabasesDeleteRemovesOnlyDirectEntries(t *testing.T) {
	store := newTestStore(t)
	dbs := store.Databases()
	if err := dbs.Put("", testDatabase("analytics")); err != nil {
		t.Fatal(err)
	}
	if err := store.RememberJumphost(billingTarget, Choices{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"analytics", "analytics", billingKey} {
		if err := dbs.Delete(name); err != nil {
			t.Fatalf("Delete(%s) = %v", name, err)
		}
	}
	entries, err := store.List()
	if err != nil || len(entries) != 1 || entries[0].Key != billingKey {
		t.Fatalf("entries = %+v, %v; want only the jumphost entry", entries, err)
	}
}

func TestDatabasesDeleteRemovesAnInvalidEntry(t *testing.T) {
	store := newTestStore(t)
	writeStoreFile(t, store, `{"version": 1, "connections": {"typo-db": {"engine": "postgres", "tls": "requir", "user": "u"}}}`)
	if err := store.Databases().Delete("typo-db"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get("typo-db"); err != nil || found {
		t.Fatalf("found %v, err %v; want the invalid entry removed", found, err)
	}
}

func TestEntryDirectUser(t *testing.T) {
	store := newTestStore(t)
	writeStoreFile(t, store, `{"version": 1, "connections": {
		"typo-db": {"engine": "postgres", "host": "db", "tls": "requir", "user": " trace ", "password_in_keychain": true},
		"no-flag": {"engine": "postgres", "hots": "db", "user": "reader"},
		"bad-user": {"engine": "postgres", "user": 5, "password_in_keychain": true},
		"bad-flag": {"engine": "postgres", "tls": "x", "user": "reader", "password_in_keychain": "yes"},
		"not-object": [1],
		"null-entry": null,
		"jumphost-ish": {"type": "rds", "target": "x", "jumphost": {"account": "1", "region": "r"}, "user": "app", "password_in_keychain": true},
		"valid": {"engine": "postgres", "host": "db", "user": "app", "password_in_keychain": true}
	}}`)
	if err := store.RememberJumphost(billingTarget, Choices{User: "admin", PasswordInKeychain: true}); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		user    string
		flagged bool
	}{
		"typo-db": {"trace", true}, "no-flag": {"reader", false}, "bad-user": {"", false}, "bad-flag": {"reader", false},
		"not-object": {"", false}, "null-entry": {"", false}, "jumphost-ish": {"", false}, "valid": {"app", true}, billingKey: {"", false},
	}
	entries, err := store.List()
	if err != nil || len(entries) != len(want) {
		t.Fatalf("entries = %d, %v; want %d", len(entries), err, len(want))
	}
	for idx := range entries {
		entry := &entries[idx]
		user, flagged := entry.DirectUser()
		if expected := want[entry.Key]; user != expected.user || flagged != expected.flagged {
			t.Errorf("%s: DirectUser = %q, %v; want %q, %v", entry.Key, user, flagged, expected.user, expected.flagged)
		}
	}
}
