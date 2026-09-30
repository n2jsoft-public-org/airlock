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

func TestQueryCancelsContextBeforeClosingTruncatedRows(t *testing.T) {
	q, db, _ := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})
	db.answerDraining("SELECT n", []string{"n"},
		[]driver.Value{int64(1)}, []driver.Value{int64(2)}, []driver.Value{int64(3)}, []driver.Value{int64(4)})

	result, err := q.Query(context.Background(), "", "SELECT n FROM big_table", 2)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 2 || !result.Truncated {
		t.Fatalf("row_count = %d truncated = %v, want 2 and true", result.RowCount, result.Truncated)
	}
	found := false
	for _, s := range db.statements() {
		if strings.Contains(s, "context already cancelled") {
			found = true
		}
		if strings.Contains(s, "drained ") {
			t.Fatalf("the remaining rows of a truncated result must not be drained: %q", s)
		}
	}
	if !found {
		t.Fatalf("expected the query context to be cancelled before rows.Close, statements: %v", db.statements())
	}
}

func TestQueryUntruncatedResultDrainsNormally(t *testing.T) {
	q, db, _ := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})
	db.answerDraining("SELECT n", []string{"n"}, []driver.Value{int64(1)}, []driver.Value{int64(2)})

	result, err := q.Query(context.Background(), "", "SELECT n FROM t", 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.RowCount != 2 || result.Truncated {
		t.Fatalf("row_count = %d truncated = %v, want 2 and false", result.RowCount, result.Truncated)
	}
	found := false
	for _, s := range db.statements() {
		if strings.Contains(s, "context already cancelled") {
			t.Fatalf("context must not be cancelled before Close on a non-truncated result: %q", s)
		}
		if strings.Contains(s, "drained 0 remaining") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected rows.Close to drain normally (nothing left), statements: %v", db.statements())
	}
}

func TestQueryRendersSQLServerUniqueIdentifier(t *testing.T) {
	q, db, _ := newTestQuerier(&sqlServerDialect, staticCreds{fallback: "Archive"})
	wireBytes := []byte{0x78, 0x56, 0x34, 0x12, 0xbc, 0x9a, 0xf0, 0xde, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	db.answerTyped("SELECT id", []string{"id"}, []string{"UNIQUEIDENTIFIER"}, []driver.Value{wireBytes})

	result, err := q.Query(context.Background(), "", "SELECT id FROM t", 10)
	if err != nil {
		t.Fatal(err)
	}
	want := "12345678-9ABC-DEF0-1122-334455667788"
	if got := result.Rows[0][0]; got != want {
		t.Fatalf("uniqueidentifier = %v, want %v", got, want)
	}
}

func TestQueryLeavesNonGUIDBinaryColumnsUnchanged(t *testing.T) {
	q, db, _ := newTestQuerier(&sqlServerDialect, staticCreds{fallback: "Archive"})
	// Invalid UTF-8 (0xff is never a valid lead byte), so jsonValue's fallback hex rendering applies.
	raw := []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	db.answerTyped("SELECT hash", []string{"hash"}, []string{"BINARY"}, []driver.Value{raw})

	result, err := q.Query(context.Background(), "", "SELECT hash FROM t", 10)
	if err != nil {
		t.Fatal(err)
	}
	want := "0x" + strings.Repeat("ff", 16)
	if got := result.Rows[0][0]; got != want {
		t.Fatalf("binary column = %v, want %v (the hex rendering, not the uniqueidentifier form)", got, want)
	}
}

func TestSQLServerDatabasesSQLExcludesRDSAdmin(t *testing.T) {
	if !strings.Contains(sqlServerDialect.databasesSQL, "name <> 'rdsadmin'") {
		t.Fatalf("databasesSQL = %q, want it to exclude rdsadmin", sqlServerDialect.databasesSQL)
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

func TestConnectionErrorExplainsRejectedCredentials(t *testing.T) {
	rejected := errors.New(`failed to connect to user=acme_rw: failed SASL auth: FATAL: password authentication failed for user "acme_rw" (SQLSTATE 28P01)`)

	iam := newSQLQuerier(&postgresDialect, newIAMTokenProvider(nil, "db", 5432, "acme_rw", "app"), 15432, nil)
	err := iam.connectionError(rejected)
	if !strings.Contains(err.Error(), "GRANT rds_iam TO acme_rw") || !strings.Contains(err.Error(), "IAM auth turned off") || !errors.Is(err, rejected) {
		t.Fatalf("IAM error = %v", err)
	}

	password := newSQLQuerier(&postgresDialect, newPasswordProvider("acme_rw", "wrong", "app"), 15432, nil)
	if err := password.connectionError(rejected); !strings.Contains(err.Error(), "password entered for acme_rw was rejected") {
		t.Fatalf("password error = %v", err)
	}

	other := errors.New("relation users does not exist")
	if err := iam.connectionError(other); err != other {
		t.Fatalf("unrelated error rewritten: %v", err)
	}
}

// rotatingCreds is a credentialRefresher whose refresh calls onRefresh, e.g. to accept connections again.
type rotatingCreds struct {
	onRefresh func()
	staticCreds
	refreshes int
}

func (r *rotatingCreds) refresh() {
	r.refreshes++
	if r.onRefresh != nil {
		r.onRefresh()
	}
}

var errLoginFailed = errors.New("mssql: login error: Login failed for user 'admin'")

func TestQueryRereadsRotatedSecretOnce(t *testing.T) {
	creds := &rotatingCreds{staticCreds: staticCreds{fallback: "app"}}
	q, db, opened := newTestQuerier(&sqlServerDialect, creds)
	db.connectErr = errLoginFailed
	creds.onRefresh = func() { db.connectErr = nil }

	if _, err := q.Query(context.Background(), "", "SELECT 1", 1); err != nil {
		t.Fatalf("query after rotation: %v", err)
	}
	if creds.refreshes != 1 || !reflect.DeepEqual(*opened, []string{"app", "app"}) {
		t.Fatalf("refreshes = %d opened = %v, want one refresh and a new pool", creds.refreshes, *opened)
	}
}

func TestExecRereadsRotatedSecretAndRunsStatementOnce(t *testing.T) {
	creds := &rotatingCreds{staticCreds: staticCreds{fallback: "app"}}
	q, db, _ := newTestQuerier(&sqlServerDialect, creds)
	db.connectErr = errLoginFailed
	creds.onRefresh = func() { db.connectErr = nil }

	affected, err := q.Exec(context.Background(), "", "DELETE FROM users WHERE id = 1")
	if err != nil || affected != 3 {
		t.Fatalf("affected = %d, err = %v", affected, err)
	}
	if got := db.statements(); !reflect.DeepEqual(got, []string{"DELETE FROM users WHERE id = 1"}) {
		t.Fatalf("statements = %q, want the statement run once", got)
	}
}

func TestQueryGivesUpWhenRereadSecretIsStillRejected(t *testing.T) {
	creds := &rotatingCreds{staticCreds: staticCreds{fallback: "app"}}
	q, db, _ := newTestQuerier(&sqlServerDialect, creds)
	db.connectErr = errLoginFailed

	_, err := q.Query(context.Background(), "", "SELECT 1", 1)
	if err == nil || !strings.Contains(err.Error(), "Login failed") || creds.refreshes != 1 {
		t.Fatalf("err = %v refreshes = %d, want the rejection reported after a single retry", err, creds.refreshes)
	}
}

func TestQueryDoesNotRetryOtherErrorsOrStaticCredentials(t *testing.T) {
	creds := &rotatingCreds{staticCreds: staticCreds{fallback: "app"}}
	q, db, opened := newTestQuerier(&postgresDialect, creds)
	db.connectErr = errors.New("dial tcp 127.0.0.1:15432: connect: connection refused")
	if _, err := q.Query(context.Background(), "", "SELECT 1", 1); err == nil || creds.refreshes != 0 {
		t.Fatalf("err = %v refreshes = %d, want no retry for a refused connection", err, creds.refreshes)
	}

	static, staticDB, staticOpened := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})
	staticDB.connectErr = errLoginFailed
	if _, err := static.Query(context.Background(), "", "SELECT 1", 1); err == nil || len(*staticOpened) != 1 {
		t.Fatalf("err = %v opened = %v, want no retry without cached secrets", err, *staticOpened)
	}
	if len(*opened) != 1 {
		t.Fatalf("opened = %v, want a single pool", *opened)
	}
}

func TestQueryReportsRejectedPasswordOnce(t *testing.T) {
	rejected := 0
	creds := newPasswordProvider("acme_rw", "wrong", "app")
	creds.onRejected = func() { rejected++ }
	q, db, _ := newTestQuerier(&postgresDialect, creds)
	db.connectErr = errLoginFailed

	//nolint:errcheck // test intentionally ignores error to verify callback behavior
	_, _ = q.Query(context.Background(), "", "SELECT 1", 1)
	//nolint:errcheck // test intentionally ignores error to verify callback called only once
	_, _ = q.Query(context.Background(), "", "SELECT 1", 1)
	if rejected != 1 {
		t.Fatalf("rejected = %d, want one notification", rejected)
	}
}

func TestQueryDoesNotReportOtherErrorsAsRejectedPassword(t *testing.T) {
	rejected := 0
	creds := newPasswordProvider("acme_rw", "s3cret", "app")
	creds.onRejected = func() { rejected++ }
	q, db, _ := newTestQuerier(&postgresDialect, creds)
	db.connectErr = errors.New("dial tcp 127.0.0.1:15432: connect: connection refused")

	//nolint:errcheck // test intentionally ignores error to verify no callback for non-auth-failure
	_, _ = q.Query(context.Background(), "", "SELECT 1", 1)
	if rejected != 0 {
		t.Fatalf("rejected = %d, want none for a refused connection", rejected)
	}
}
