package dbmcp

import (
	"context"
	"database/sql/driver"
	"errors"
	"slices"
	"testing"

	mssql "github.com/microsoft/go-mssqldb"
)

func TestCheckSignInRunsSelectOneOnTheDefaultDatabase(t *testing.T) {
	q, db, opened := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})
	db.answer("SELECT 1", []string{"?column?"}, []driver.Value{int64(1)})

	if err := checkSignIn(context.Background(), func() Querier { return q }); err != nil {
		t.Fatal(err)
	}
	if len(*opened) != 1 || (*opened)[0] != "app" {
		t.Fatalf("opened = %v, want the default database", *opened)
	}
	if !slices.Contains(db.statements(), "SELECT 1") {
		t.Fatalf("statements = %q, want SELECT 1", db.statements())
	}
	if len(q.pools) != 0 {
		t.Fatal("the check must close its connections")
	}
}

func TestCheckSignInTellsARejectedPasswordFromOtherFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		err      error
		rejected bool
	}{
		"postgres password":      {errors.New("FATAL: password authentication failed for user \"app\" (SQLSTATE 28P01)"), true},
		"sql server login":       {mssql.Error{Number: 18456, Message: "Login failed for user 'app'."}, true},
		"sql server no database": {mssql.Error{Number: 4060, Message: "Cannot open database", All: []mssql.Error{{Number: 4060}, {Number: 18456}}}, false},
		"tunnel down":            {errors.New("dial tcp 127.0.0.1:1433: connect: connection refused"), false},
	} {
		t.Run(name, func(t *testing.T) {
			q, db, _ := newTestQuerier(&sqlServerDialect, staticCreds{})
			db.connectErr = tc.err
			err := checkSignIn(context.Background(), func() Querier { return q })
			if err == nil || errors.Is(err, ErrSignInRejected) != tc.rejected {
				t.Fatalf("err = %v, rejected = %v, want rejected = %v", err, errors.Is(err, ErrSignInRejected), tc.rejected)
			}
		})
	}
}

func TestCheckSignInRefusesUnsupportedEngines(t *testing.T) {
	err := CheckSignIn(context.Background(), "oracle-ee", PasswordCredentials("u", "p", ""), 3306)
	if !errors.Is(err, ErrEngineUnsupported) {
		t.Fatalf("err = %v, want ErrEngineUnsupported", err)
	}
}

func TestDefaultDatabase(t *testing.T) {
	for _, tc := range []struct{ engine, dbName, want string }{
		{"postgres", "", "postgres"},
		{"aurora-postgresql", "app", "app"},
		{"sqlserver-web", "", ""},
		{"sqlserver-se", "Archive", "Archive"},
	} {
		if got := DefaultDatabase(tc.engine, tc.dbName); got != tc.want {
			t.Errorf("DefaultDatabase(%q, %q) = %q, want %q", tc.engine, tc.dbName, got, tc.want)
		}
	}
}

func TestCheckDirectSignInRefusesUnsupportedEngines(t *testing.T) {
	err := CheckDirectSignIn(context.Background(), "oracle", PasswordCredentials("u", "p", ""), Address{Host: "db", Port: 1521})
	if !errors.Is(err, ErrEngineUnsupported) {
		t.Fatalf("err = %v, want ErrEngineUnsupported", err)
	}
}

func TestReadOnlyRestsOnGrants(t *testing.T) {
	if ReadOnlyRestsOnGrants(EnginePostgres) {
		t.Fatal("PostgreSQL reads run in read-only transactions")
	}
	if !ReadOnlyRestsOnGrants(EngineSQLServer) {
		t.Fatal("SQL Server has no read-only transaction: its read-only access rests on grants")
	}
}
