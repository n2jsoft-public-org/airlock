package dbmcp

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// staticCreds is a CredentialProvider with a fixed default database and optional database list.
type staticCreds struct {
	fallback  string
	databases []string
}

func (s staticCreds) Credentials(_ context.Context, database string) (Credentials, error) {
	if database == "" {
		database = s.fallback
	}
	return Credentials{User: "u", Password: "p", Database: database}, nil
}

func (s staticCreds) Databases(context.Context) ([]string, error) { return s.databases, nil }

func newTestQuerier(d *dialect, creds CredentialProvider) (*sqlQuerier, *fakeDB, *[]string) {
	db := newFakeDB()
	opened := &[]string{}
	return newSQLQuerier(d, creds, 15432, db.opener(opened)), db, opened
}

func TestQueryRunsInRolledBackReadOnlyTransaction(t *testing.T) {
	q, db, opened := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})
	db.answer("SELECT id", []string{"id", "name"}, []driver.Value{int64(1), []byte("a")})

	result, err := q.Query(context.Background(), "", "SELECT id, name FROM users", 10)
	if err != nil {
		t.Fatal(err)
	}
	want := Result{Columns: []string{"id", "name"}, Rows: [][]any{{int64(1), "a"}}, RowCount: 1}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	wantLog := []string{"BEGIN READ ONLY", "SET LOCAL standard_conforming_strings = on", "SET LOCAL statement_timeout = '30s'", "SELECT id, name FROM users", "ROLLBACK"}
	if got := db.statements(); !reflect.DeepEqual(got, wantLog) {
		t.Fatalf("statements = %q, want %q", got, wantLog)
	}
	if !reflect.DeepEqual(*opened, []string{"app"}) {
		t.Fatalf("opened = %v, want the default database", *opened)
	}
}

func TestQueryOnSQLServerSetsLockTimeout(t *testing.T) {
	q, db, _ := newTestQuerier(&sqlServerDialect, staticCreds{fallback: "Archive"})

	if _, err := q.Query(context.Background(), "", "SELECT 1", 10); err != nil {
		t.Fatal(err)
	}
	wantLog := []string{"BEGIN", "SET LOCK_TIMEOUT 5000", "SELECT 1", "ROLLBACK"}
	if got := db.statements(); !reflect.DeepEqual(got, wantLog) {
		t.Fatalf("statements = %q, want %q", got, wantLog)
	}
}

func TestQueryCapsRows(t *testing.T) {
	q, db, _ := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})
	db.answer("SELECT n", []string{"n"}, []driver.Value{int64(1)}, []driver.Value{int64(2)}, []driver.Value{int64(3)})

	result, err := q.Query(context.Background(), "", "SELECT n FROM t", 2)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 2 || !result.Truncated {
		t.Fatalf("row_count = %d truncated = %v, want 2 and true", result.RowCount, result.Truncated)
	}
}

func TestQueryRejectedByGuardNeverReachesDatabase(t *testing.T) {
	q, db, opened := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})

	if _, err := q.Query(context.Background(), "", "DELETE FROM users", 10); err == nil {
		t.Fatal("expected guard error")
	}
	if len(db.statements()) != 0 || len(*opened) != 0 {
		t.Fatalf("database was touched: %v %v", db.statements(), *opened)
	}
}

func TestQueryUsesOnePoolPerDatabase(t *testing.T) {
	q, _, opened := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})

	for _, database := range []string{"", "app", "other", "other"} {
		if _, err := q.Query(context.Background(), database, "SELECT 1", 1); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(*opened, []string{"app", "other"}) {
		t.Fatalf("opened = %v", *opened)
	}
}

func TestQueryExplainsRefusedConnection(t *testing.T) {
	q, db, _ := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})
	db.connectErr = errors.New("dial tcp 127.0.0.1:15432: connect: connection refused")

	_, err := q.Query(context.Background(), "", "SELECT 1", 1)
	if err == nil || !strings.Contains(err.Error(), "tunnel to localhost:15432 is not accepting connections yet") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecReturnsRowsAffectedWithoutGuard(t *testing.T) {
	q, db, _ := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})

	affected, err := q.Exec(context.Background(), "", "DELETE FROM users WHERE id = 1")
	if err != nil {
		t.Fatal(err)
	}
	if affected != 3 {
		t.Fatalf("affected = %d, want 3", affected)
	}
	if got := db.statements(); !reflect.DeepEqual(got, []string{"DELETE FROM users WHERE id = 1"}) {
		t.Fatalf("statements = %q", got)
	}
}

func TestDatabasesPrefersCredentialList(t *testing.T) {
	q, db, _ := newTestQuerier(&sqlServerDialect, staticCreds{databases: []string{"A", "B"}, fallback: "A"})

	names, err := q.Databases(context.Background())
	if err != nil || !reflect.DeepEqual(names, []string{"A", "B"}) {
		t.Fatalf("databases = %v, %v", names, err)
	}
	if len(db.statements()) != 0 {
		t.Fatal("catalog must not be queried when credentials list the databases")
	}
}

func TestDatabasesFallsBackToCatalog(t *testing.T) {
	q, db, _ := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})
	db.answer("SELECT datname", []string{"datname"}, []driver.Value{"app"}, []driver.Value{"reporting"})

	names, err := q.Databases(context.Background())
	if err != nil || !reflect.DeepEqual(names, []string{"app", "reporting"}) {
		t.Fatalf("databases = %v, %v", names, err)
	}
}

func TestTablesPassesSchemaFilter(t *testing.T) {
	q, db, _ := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})
	db.answer("SELECT table_schema", []string{"s", "n", "t"}, []driver.Value{"public", "users", "BASE TABLE"})

	tables, truncated, err := q.Tables(context.Background(), "", "public")
	if err != nil {
		t.Fatal(err)
	}
	if truncated || !reflect.DeepEqual(tables, []Table{{Schema: "public", Name: "users", Type: "BASE TABLE"}}) {
		t.Fatalf("tables = %v truncated = %v", tables, truncated)
	}
	if log := db.statements(); !strings.HasSuffix(log[3], " | public") {
		t.Fatalf("schema argument not passed: %q", log[3])
	}
}

func TestDescribeDefaultsSchemaAndMapsCatalog(t *testing.T) {
	q, db, _ := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})
	db.answer("SELECT column_name", []string{"c", "t", "n", "d"},
		[]driver.Value{"id", "integer", "NO", "nextval('users_id_seq')"},
		[]driver.Value{"team_id", "integer", "YES", nil},
	)
	db.answer("SELECT kcu.column_name", []string{"c"}, []driver.Value{"id"})
	db.answer("SELECT con.conname", []string{"n", "c", "rs", "rt", "rc"}, []driver.Value{"users_team_fk", "team_id", "public", "teams", "id"})
	db.answer("SELECT indexname", []string{"n", "d"}, []driver.Value{"users_pkey", "CREATE UNIQUE INDEX users_pkey ON public.users USING btree (id)"})

	description, err := q.Describe(context.Background(), "", "", "users")
	if err != nil {
		t.Fatal(err)
	}
	defaultValue := "nextval('users_id_seq')"
	want := TableDescription{
		Schema: "public", Table: "users",
		Columns: []Column{
			{Name: "id", Type: "integer", Nullable: false, Default: &defaultValue},
			{Name: "team_id", Type: "integer", Nullable: true},
		},
		PrimaryKey:  []string{"id"},
		ForeignKeys: []ForeignKey{{Name: "users_team_fk", Column: "team_id", ReferencedSchema: "public", ReferencedTable: "teams", ReferencedColumn: "id"}},
		Indexes:     []Index{{Name: "users_pkey", Definition: "CREATE UNIQUE INDEX users_pkey ON public.users USING btree (id)"}},
	}
	if !reflect.DeepEqual(description, want) {
		t.Fatalf("description = %+v\nwant %+v", description, want)
	}
}

func TestDescribeUnknownTable(t *testing.T) {
	q, _, _ := newTestQuerier(&sqlServerDialect, staticCreds{fallback: "Archive"})

	if _, err := q.Describe(context.Background(), "", "", "Nope"); err == nil || !strings.Contains(err.Error(), "dbo.Nope not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}
