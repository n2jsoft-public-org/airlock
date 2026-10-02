package directdb

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) (store *Store, path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "airlock", "databases.json")
	return NewStore(path), path
}

func writeDatabasesFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "databases.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStoreMissingFileHasNoDatabase(t *testing.T) {
	store, path := newTestStore(t)
	if store.Path() != path {
		t.Fatalf("Path() = %q, want %q", store.Path(), path)
	}
	dbs, err := store.List()
	if err != nil || len(dbs) != 0 {
		t.Fatalf("List() = %v, %v; want no database", dbs, err)
	}
	if _, found, err := store.Get("billing"); found || err != nil {
		t.Fatalf("Get() found = %v, err = %v", found, err)
	}
}

func TestStoreReadsEmptyFileAsNoDatabase(t *testing.T) {
	for name, content := range map[string]string{"zero bytes": "", "blank": " \n\t\n"} {
		dbs, err := NewStore(writeDatabasesFile(t, content)).List()
		if err != nil || len(dbs) != 0 {
			t.Fatalf("%s: List() = %v, %v; want no database", name, dbs, err)
		}
	}
}

func TestStorePutWritesTheFileLayout(t *testing.T) {
	store, path := newTestStore(t)
	db := Database{Name: "legacy-billing", Engine: EngineSQLServer, Host: "billing.db.internal", User: "mcp_ro", Production: true}
	if err := store.Put("", db); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- test file under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "version": 1,
  "databases": {
    "legacy-billing": {
      "engine": "sqlserver",
      "host": "billing.db.internal",
      "user": "mcp_ro",
      "production": true
    }
  }
}
`
	if string(raw) != want {
		t.Fatalf("databases.json =\n%s\nwant\n%s", raw, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v; want 0600", info.Mode().Perm())
		}
	}
}

func TestStorePutGetListRoundTrip(t *testing.T) {
	store, _ := newTestStore(t)
	billing := testDatabase("billing")
	billing.Port, billing.Database, billing.TLS = 6432, "billing", TLSVerify
	for _, db := range []Database{testDatabase("zeta"), billing} {
		if err := store.Put("", db); err != nil {
			t.Fatal(err)
		}
	}
	dbs, err := store.List()
	if err != nil || len(dbs) != 2 || dbs[0].Name != "billing" || dbs[1].Name != "zeta" {
		t.Fatalf("List() = %+v, %v; want billing then zeta", dbs, err)
	}
	got, found, err := store.Get("billing")
	if err != nil || !found || got != billing {
		t.Fatalf("Get(billing) = %+v, %v, %v; want %+v", got, found, err, billing)
	}
}

func TestStorePutNormalizesBeforeWriting(t *testing.T) {
	store, _ := newTestStore(t)
	db := Database{Name: " billing ", Engine: " Postgres ", Host: " db.internal ", User: " app "}
	if err := store.Put("", db); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Get("billing")
	if err != nil || !found || got != testDatabase("billing") {
		t.Fatalf("Get(billing) = %+v, %v, %v", got, found, err)
	}
}

func TestStorePutValidatesBeforeWriting(t *testing.T) {
	store, path := newTestStore(t)
	db := testDatabase("billing")
	db.Engine = "mysql"
	var fieldErr *FieldError
	if err := store.Put("", db); !errors.As(err, &fieldErr) || fieldErr.Field != "engine" {
		t.Fatalf("Put() = %v, want an engine field error", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an invalid definition must not create the file: %v", err)
	}
}

func TestStorePutRefusesAnExistingName(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Put("", testDatabase("billing")); err != nil {
		t.Fatal(err)
	}
	err := store.Put("", testDatabase("billing"))
	if !errors.Is(err, ErrExists) || err.Error() != `database "billing" already exists` {
		t.Fatalf("Put() = %v, want database \"billing\" already exists", err)
	}
}

func TestStorePutUpdatesInPlace(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Put("", testDatabase("billing")); err != nil {
		t.Fatal(err)
	}
	updated := testDatabase("billing")
	updated.Host = "db2.internal"
	if err := store.Put("billing", updated); err != nil {
		t.Fatal(err)
	}
	got, _, err := store.Get("billing")
	if err != nil || got.Host != "db2.internal" {
		t.Fatalf("Get(billing) = %+v, %v", got, err)
	}
}

func TestStorePutRenames(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Put("", testDatabase("billing")); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("billing", testDatabase("legacy-billing")); err != nil {
		t.Fatal(err)
	}
	dbs, err := store.List()
	if err != nil || len(dbs) != 1 || dbs[0].Name != "legacy-billing" {
		t.Fatalf("List() = %+v, %v; want legacy-billing only", dbs, err)
	}
}

func TestStorePutRenameOntoAnotherNameFails(t *testing.T) {
	store, _ := newTestStore(t)
	for _, name := range []string{"billing", "archive"} {
		if err := store.Put("", testDatabase(name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Put("billing", testDatabase("archive")); !errors.Is(err, ErrExists) {
		t.Fatalf("Put() = %v, want ErrExists", err)
	}
	if _, found, err := store.Get("billing"); err != nil || !found {
		t.Fatalf("a refused rename must keep the database: found = %v, err = %v", found, err)
	}
}

func TestStorePutUnknownPreviousNameFails(t *testing.T) {
	store, _ := newTestStore(t)
	err := store.Put("billing", testDatabase("billing"))
	if !errors.Is(err, ErrNotFound) || err.Error() != `database "billing" not found` {
		t.Fatalf("Put() = %v, want database \"billing\" not found", err)
	}
}

func TestStoreDelete(t *testing.T) {
	store, _ := newTestStore(t)
	if err := store.Delete("billing"); err != nil {
		t.Fatalf("deleting a missing database must not fail: %v", err)
	}
	for _, name := range []string{"billing", "archive"} {
		if err := store.Put("", testDatabase(name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Delete("billing"); err != nil {
		t.Fatal(err)
	}
	dbs, err := store.List()
	if err != nil || len(dbs) != 1 || dbs[0].Name != "archive" {
		t.Fatalf("List() = %+v, %v; want archive only", dbs, err)
	}
}

func TestStoreRereadsTheFileBeforeEachWrite(t *testing.T) {
	_, path := newTestStore(t)
	first, second := NewStore(path), NewStore(path)
	if err := first.Put("", testDatabase("billing")); err != nil {
		t.Fatal(err)
	}
	if err := second.Put("", testDatabase("archive")); err != nil {
		t.Fatal(err)
	}
	dbs, err := first.List()
	if err != nil || len(dbs) != 2 {
		t.Fatalf("List() = %+v, %v; want the databases of both stores", dbs, err)
	}
}

func TestStoreRejectsInvalidFiles(t *testing.T) {
	for name, tc := range map[string]struct {
		content string
		want    []string
	}{
		"not json":         {`{`, []string{"invalid"}},
		"unknown key":      {`{"version": 1, "databases": {}, "owner": "me"}`, []string{"owner"}},
		"unknown field":    {`{"version": 1, "databases": {"billing": {"engine": "postgres", "host": "h", "user": "u", "password": "p"}}}`, []string{"password"}},
		"unsupported":      {`{"version": 2, "databases": {}}`, []string{"version 2", "update airlock"}},
		"missing version":  {`{"databases": {}}`, []string{"version 0"}},
		"invalid database": {`{"version": 1, "databases": {"billing": {"engine": "mysql", "host": "h", "user": "u"}}}`, []string{`database "billing"`, "engine: must be postgres or sqlserver"}},
		"invalid name":     {`{"version": 1, "databases": {"Billing": {"engine": "postgres", "host": "h", "user": "u"}}}`, []string{`database "Billing"`, "name:"}},
	} {
		path := writeDatabasesFile(t, tc.content)
		_, err := NewStore(path).List()
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("%s: err = %v, want the file named", name, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s: err = %v, want it to mention %q", name, err, want)
			}
		}
		if err := NewStore(path).Put("", testDatabase("archive")); err == nil {
			t.Fatalf("%s: Put must not overwrite an invalid file", name)
		}
	}
}

func TestStoreWithoutPathCannotWrite(t *testing.T) {
	store := NewStore("")
	if dbs, err := store.List(); err != nil || len(dbs) != 0 {
		t.Fatalf("List() = %v, %v; want no database", dbs, err)
	}
	if err := store.Put("", testDatabase("billing")); err == nil {
		t.Fatal("Put without a path must fail")
	}
}

func TestNotFoundError(t *testing.T) {
	err := NotFoundError("billing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("NotFoundError() = %v, want it to match ErrNotFound", err)
	}
	if want := "no direct database named billing (see airlock db list)"; err.Error() != want {
		t.Fatalf("NotFoundError() = %q, want %q", err.Error(), want)
	}
}

func TestStoreRejectsContentAfterTheJSON(t *testing.T) {
	path := writeDatabasesFile(t, `{"version": 1, "databases": {}} garbage`)
	if _, err := NewStore(path).List(); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v, want an error naming the file", err)
	}
	path = writeDatabasesFile(t, `{"version": 1, "databases": {}}`+"\n\n")
	if _, err := NewStore(path).List(); err != nil {
		t.Fatalf("trailing whitespace: %v", err)
	}
}
