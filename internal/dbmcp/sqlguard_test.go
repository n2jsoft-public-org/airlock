package dbmcp

import (
	"strings"
	"testing"
)

func TestCheckReadOnlyAccepts(t *testing.T) {
	cases := []struct {
		name   string
		engine Engine
		query  string
	}{
		{"select", EnginePostgres, "SELECT * FROM users"},
		{"lowercase with trailing semicolon", EngineSQLServer, "select top 10 * from dbo.Users;"},
		{"cte", EnginePostgres, "WITH x AS (SELECT 1) SELECT * FROM x"},
		{"explain postgres", EnginePostgres, "EXPLAIN SELECT * FROM users"},
		{"keyword in string literal", EnginePostgres, "SELECT * FROM logs WHERE msg = 'DELETE FROM users; DROP TABLE x'"},
		{"escaped quote in literal", EngineSQLServer, "SELECT 'it''s; DROP' AS x"},
		{"keyword in quoted identifier", EnginePostgres, `SELECT "update" FROM t`},
		{"keyword in bracket identifier", EngineSQLServer, "SELECT [delete] FROM t"},
		{"keyword in line comment", EnginePostgres, "SELECT 1 -- DROP TABLE users\n"},
		{"keyword in block comment", EngineSQLServer, "SELECT /* ; DELETE */ 1"},
		{"keyword in dollar quote", EnginePostgres, "SELECT $tag$ DROP; $tag$ AS x"},
		{"identifier containing keyword", EnginePostgres, "SELECT updated_at, created_by FROM t"},
		{"positional parameter is not a dollar quote", EnginePostgres, "SELECT $1"},
		{"escape string with escaped quote", EnginePostgres, "SELECT E'it\\'s; DROP' AS x"},
		{"identifier ending in e before a string is not an escape string", EnginePostgres, "SELECT 1 FROM t WHERE name = 'a\\' OR code = 'b'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckReadOnly(tc.engine, tc.query); err != nil {
				t.Fatalf("CheckReadOnly(%q) = %v, want nil", tc.query, err)
			}
		})
	}
}

func TestCheckReadOnlyRejects(t *testing.T) {
	cases := []struct {
		name    string
		engine  Engine
		query   string
		wantErr string
	}{
		{"empty", EnginePostgres, "  ;  ", "empty"},
		{"comment only", EnginePostgres, "-- nothing", "empty"},
		{"delete", EnginePostgres, "DELETE FROM users", "only SELECT or WITH"},
		{"chained statements", EnginePostgres, "SELECT 1; DELETE FROM users", "single statement"},
		{"writing cte", EnginePostgres, "WITH d AS (DELETE FROM users RETURNING *) SELECT * FROM d", "DELETE"},
		{"select into", EngineSQLServer, "SELECT * INTO backup FROM users", "INTO"},
		{"select for update", EnginePostgres, "SELECT * FROM users FOR UPDATE", "UPDATE"},
		{"explain analyze executes the query", EnginePostgres, "EXPLAIN ANALYZE SELECT * FROM users", "ANALYZE"},
		{"explain on sql server", EngineSQLServer, "EXPLAIN SELECT 1", "only SELECT or WITH"},
		{"exec", EngineSQLServer, "EXEC sp_who", "only SELECT or WITH"},
		{"openrowset", EngineSQLServer, "SELECT * FROM OPENROWSET('x', 'y', 'z')", "OPENROWSET"},
		{"comment hides nothing", EngineSQLServer, "SELECT 1 /* x */; DROP TABLE users", "single statement"},
		{"bracket is not a quote on postgres", EnginePostgres, "SELECT ARRAY[1]; DROP TABLE t", "single statement"},
		{"unterminated literal", EnginePostgres, "SELECT 'oops", "unterminated"},
		{"unterminated comment", EngineSQLServer, "SELECT 1 /* oops", "unterminated"},
		{"escape string hides a second statement", EnginePostgres, "SELECT E'x\\'y' FROM t; DROP TABLE users; --'", "single statement"},
		{"lowercase escape string", EnginePostgres, "SELECT e'\\'' ; DELETE FROM t", "single statement"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckReadOnly(tc.engine, tc.query)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("CheckReadOnly(%q) = %v, want error containing %q", tc.query, err, tc.wantErr)
			}
		})
	}
}
