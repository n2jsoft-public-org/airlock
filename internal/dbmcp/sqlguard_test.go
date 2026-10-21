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
		{"standard string ending in a backslash", EnginePostgres, "SELECT 1 FROM t WHERE name = 'a\\' OR code = 'b'"},
		{"dollar sign inside an identifier is not a dollar quote", EnginePostgres, "SELECT price$usd FROM t"},
		{"keyword in backtick identifier", EngineMySQL, "SELECT `delete`, `a``;b` FROM `order`"},
		{"keyword in hash comment", EngineMySQL, "SELECT 1 # ; DROP TABLE users\n"},
		{"keyword in double-dash comment", EngineMariaDB, "SELECT 1 -- ; DROP TABLE users"},
		{"backslash-escaped quote", EngineMySQL, "SELECT 'it\\'s; DROP' AS x"},
		{"double-quoted string", EngineMySQL, `SELECT "a\"; DELETE" AS x`},
		{"optimizer hint", EngineMySQL, "SELECT /*+ MAX_EXECUTION_TIME(1000) */ 1"},
		{"show", EngineMySQL, "SHOW CREATE TABLE orders"},
		{"describe", EngineMariaDB, "DESCRIBE orders"},
		{"desc", EngineMySQL, "desc orders"},
		{"explain mysql", EngineMySQL, "EXPLAIN FORMAT=JSON SELECT * FROM orders"},
		{"numeric literals", EngineSQLServer, "SELECT 1, 1.5, .5, 1E5, 1.5e-3, $1, 0x1F, col2, t1.c3 FROM t1 WHERE a = 1e0"},
		{"fetch next", EngineSQLServer, "SELECT * FROM t ORDER BY id OFFSET 10 ROWS FETCH NEXT 10 ROWS ONLY"},
		{"transaction advisory lock", EnginePostgres, "SELECT pg_try_advisory_xact_lock(1)"},
		{"lock function name in a string", EnginePostgres, "SELECT pid FROM pg_stat_activity WHERE query LIKE '%pg_terminate_backend%'"},
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
		{"dollar signs after identifiers are not a dollar quote hiding a second statement", EnginePostgres, "SELECT a$b$ FROM t; DELETE FROM t; SELECT c$b$ FROM t", "single statement"},
		{"sql server batch commits and disables a trigger without a semicolon", EngineSQLServer, "SELECT 1 COMMIT DISABLE TRIGGER ALL ON dbo.t", "COMMIT"},
		{"sql server batch begins a transaction without a semicolon", EngineSQLServer, "SELECT 1 BEGIN TRAN", "BEGIN"},
		{"mysql delete", EngineMySQL, "DELETE FROM users", "only SELECT, WITH, SHOW, DESCRIBE or EXPLAIN"},
		{"show on postgres", EnginePostgres, "SHOW search_path", "only SELECT or WITH"},
		{"select into outfile", EngineMySQL, "SELECT * FROM users INTO OUTFILE '/tmp/u'", "INTO"},
		{"select into variable", EngineMariaDB, "SELECT id INTO @x FROM users", "INTO"},
		{"lock in share mode", EngineMySQL, "SELECT * FROM users LOCK IN SHARE MODE", "LOCK"},
		{"lock tables", EngineMySQL, "LOCK TABLES users WRITE", "only SELECT"},
		{"set global", EngineMySQL, "SET GLOBAL read_only = 0", "only SELECT"},
		{"load data", EngineMySQL, "LOAD DATA LOCAL INFILE '/etc/passwd' INTO TABLE t", "only SELECT"},
		{"explain analyze executes the query on mysql", EngineMySQL, "EXPLAIN ANALYZE SELECT * FROM users", "ANALYZE"},
		{"describe hides a delete", EngineMySQL, "DESCRIBE DELETE FROM users", "DELETE"},
		{"executable comment", EngineMySQL, "SELECT 1 /*! ; DELETE FROM users */", "executable comments"},
		{"mariadb executable comment", EngineMariaDB, "SELECT 1 /*M!100100 ; DELETE FROM users */", "executable comments"},
		{"mysql comments do not nest", EngineMySQL, "SELECT 1 /* /* */ ; DELETE FROM users /* */", "single statement"},
		{"double dash without space is a subtraction", EngineMySQL, "SELECT 1--1; DELETE FROM users", "single statement"},
		{"backslash escape does not end a string", EngineMySQL, "SELECT '\\'' ; DELETE FROM users; SELECT ''", "single statement"},
		{"hash is no comment on postgres", EnginePostgres, "SELECT 1 # 2; DELETE FROM users", "single statement"},
		{"number glued to a keyword", EngineSQLServer, "SELECT 1DELETE FROM dbo.t SELECT 1COMMIT", "DELETE"},
		{"number glued to commit", EngineSQLServer, "SELECT 1 SELECT 1COMMIT", "COMMIT"},
		{"number glued to drop", EngineSQLServer, "SELECT 1DROP TABLE dbo.t", "DROP"},
		{"exponent glued to a keyword", EngineSQLServer, "SELECT 1E0DELETE FROM dbo.t", "DELETE"},
		{"fraction and exponent glued to a keyword", EngineSQLServer, "SELECT 1.e5DELETE FROM dbo.t", "DELETE"},
		{"bare exponent glued to a keyword", EngineSQLServer, "SELECT 1EXEC sp_who", "EXEC"},
		{"fraction glued to a keyword", EngineSQLServer, "SELECT 1.EXEC sp_who", "EXEC"},
		{"money glued to a keyword", EngineSQLServer, "SELECT $1DELETE FROM dbo.t", "DELETE"},
		{"binary literal glued to a keyword", EngineSQLServer, "SELECT 0x1FUPDATETEXT", "UPDATETEXT"},
		{"number glued to into outfile", EngineMySQL, "SELECT * FROM users WHERE 1=1e0INTO OUTFILE '/tmp/u'", "INTO"},
		{"bare number", EngineSQLServer, "1", "only SELECT or WITH"},
		{"session advisory lock", EnginePostgres, "SELECT pg_advisory_lock(1)", "PG_ADVISORY_LOCK"},
		{"session try advisory lock", EnginePostgres, "SELECT pg_catalog.pg_try_advisory_lock_shared(1)", "PG_TRY_ADVISORY_LOCK_SHARED"},
		{"terminate backend", EnginePostgres, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity", "PG_TERMINATE_BACKEND"},
		{"quoted cancel backend", EnginePostgres, `SELECT "pg_cancel_backend"(pid) FROM pg_stat_activity`, "PG_CANCEL_BACKEND"},
		{"unicode escape identifier", EnginePostgres, `SELECT U&"pg_terminate_backen\0064"(1)`, "Unicode escapes"},
		{"mysql named lock", EngineMySQL, "SELECT GET_LOCK('x', 10)", "GET_LOCK"},
		{"backtick named lock", EngineMySQL, "SELECT `get_lock`('x', 10)", "GET_LOCK"},
		{"sequence value", EngineSQLServer, "SELECT NEXT VALUE FOR dbo.seq", "NEXT VALUE FOR"},
		{"sequence value with a comment", EngineMariaDB, "SELECT NEXT /* x */ VALUE FOR seq", "NEXT VALUE FOR"},
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
