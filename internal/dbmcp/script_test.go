package dbmcp

import (
	"context"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

func TestStripLiteralsKeepsPositions(t *testing.T) {
	for _, tc := range []struct {
		engine Engine
		query  string
	}{
		{EnginePostgres, "SELECT 'a;\nb' -- c;\n, $x$ ;\n $x$, \"pg_terminate_backend\"(1) /* d\n/* e */ */"},
		{EngineSQLServer, "SELECT N'é;''\n' AS [x;\ny] -- GO\nGO"},
		{EngineMySQL, "SELECT 'a\\';\n' # c\n, `x;` /* y\n */"},
	} {
		code, err := stripLiterals(tc.engine, tc.query)
		if err != nil {
			t.Fatal(err)
		}
		if len([]rune(code)) != len([]rune(tc.query)) || strings.Count(code, "\n") != strings.Count(tc.query, "\n") {
			t.Fatalf("%s: %q does not keep the positions of %q", tc.engine, code, tc.query)
		}
		if strings.Contains(code, ";") {
			t.Fatalf("%s: %q keeps a quoted semicolon", tc.engine, code)
		}
	}
}

func TestSplitScriptAtGOLines(t *testing.T) {
	script := "-- checks\nCREATE TABLE #r (id int)\ngo\n\nINSERT #r SELECT 1; INSERT #r SELECT 2\n/*\nGO\n*/\nSELECT 'x\nGO\n'\n  GO  -- end\nSELECT * FROM #r\n"
	got, err := splitScript(EngineSQLServer, script)
	if err != nil {
		t.Fatal(err)
	}
	want := []scriptStatement{
		{sql: "-- checks\nCREATE TABLE #r (id int)", line: 2},
		{sql: "INSERT #r SELECT 1; INSERT #r SELECT 2\n/*\nGO\n*/\nSELECT 'x\nGO\n'", line: 5},
		{sql: "SELECT * FROM #r", line: 13},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("batches = %#v\nwant %#v", got, want)
	}
}

func TestSplitScriptAtSemicolons(t *testing.T) {
	script := "SELECT 1;\n-- only a comment;\n;\nSELECT $$;$$\n  , ';' ;SELECT 3"
	got, err := splitScript(EnginePostgres, script)
	if err != nil {
		t.Fatal(err)
	}
	want := []scriptStatement{
		{sql: "SELECT 1", line: 1},
		// The ";" of the comment ends nothing, the lone ";" an empty statement, left out.
		{sql: "SELECT $$;$$\n  , ';'", line: 4},
		{sql: "SELECT 3", line: 5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("statements = %#v\nwant %#v", got, want)
	}
}

func TestCheckScriptBatchAllowsTemporaryObjects(t *testing.T) {
	for _, batch := range []string{
		"SET NOCOUNT ON; SET ANSI_NULLS ON SET QUOTED_IDENTIFIER ON; CREATE TABLE #r (id nvarchar(255), ok bit)",
		"DECLARE @n int = 0; SET @n = (SELECT COUNT(*) FROM dbo.users); INSERT INTO #r VALUES ('1', CASE WHEN @n > 0 THEN 1 ELSE 0 END)",
		"BEGIN TRY INSERT #r SELECT 'x', 1 FROM dbo.t WHERE id = 1 END TRY BEGIN CATCH INSERT INTO #r VALUES ('x', NULL) END CATCH",
		"DECLARE @t TABLE (id int); INSERT @t SELECT id FROM dbo.users; UPDATE @t SET id = id + 1; DELETE FROM @t WHERE id = 2",
		"UPDATE #r SET ok = 0 FROM #r JOIN dbo.users u ON u.name = #r.id",
		"SELECT id INTO #ids FROM dbo.users; TRUNCATE TABLE #ids; DELETE #ids",
		"IF OBJECT_ID('tempdb..#r') IS NOT NULL DROP TABLE IF EXISTS #r, #ids",
		";WITH c AS (SELECT 1 AS n) SELECT n FROM c; PRINT 'done'",
		"WHILE @@ROWCOUNT > 0 BEGIN SELECT 1 BREAK END",
	} {
		if err := checkScriptBatch(batch); err != nil {
			t.Errorf("%q: %v", batch, err)
		}
	}
}

func TestCheckScriptBatchRefusesWrites(t *testing.T) {
	for batch, want := range map[string]string{
		"INSERT INTO dbo.users VALUES (1)":                 "INSERT",
		"SELECT 1 INSERT users SELECT * FROM #r":           "INSERT",
		"UPDATE u SET name = 'x' FROM dbo.users u":         "UPDATE",
		"DELETE FROM users WHERE 1 = 1":                    "DELETE",
		"SELECT * INTO dbo.copy FROM dbo.users":            "INTO",
		"SELECT * INTO ##shared FROM dbo.users":            "INTO",
		"CREATE TABLE dbo.t (id int)":                      "CREATE",
		"CREATE INDEX ix ON #r (id)":                       "CREATE",
		"DROP TABLE #r, dbo.users":                         "DROP",
		"DROP TABLE #r.x":                                  "DROP",
		"TRUNCATE TABLE dbo.users":                         "TRUNCATE",
		"INSERT #r EXEC ('SELECT 1')":                      "EXEC",
		"BEGIN TRAN SELECT 1":                              "TRAN",
		"SELECT 1 COMMIT":                                  "COMMIT",
		"SET XACT_ABORT ON":                                "SET",
		"SELECT 1 MERGE #r USING dbo.t ON 1 = 1":           "MERGE",
		"sp_send_dbmail @recipients = 'a@b.c'":             "SP_SEND_DBMAIL",
		"SELECT NEXT VALUE FOR dbo.seq":                    "NEXT VALUE FOR",
		"SELECT 1 WAITFOR DELAY '00:01'":                   "WAITFOR",
		"DECLARE c CURSOR FOR SELECT id FROM t FOR UPDATE": "UPDATE",
	} {
		err := checkScriptBatch(batch)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error = %v, want one naming %s", batch, err, want)
		}
	}
}

func TestScriptRunsSQLServerBatchesAndGoesOnAfterAnError(t *testing.T) {
	q, db, _ := newTestQuerier(&sqlServerDialect, staticCreds{fallback: "app"})
	db.answers["SELECT bad"] = fakeAnswer{err: mssql.Error{Number: 207, Message: "Invalid column name 'bad'."}}
	db.answer("SELECT XACT_STATE()", []string{""}, []driver.Value{int64(1)})
	db.answer("SELECT * FROM #r", []string{"id"}, []driver.Value{int64(1)}, []driver.Value{int64(2)})

	result, err := q.Script(t.Context(), &ScriptRequest{
		SQL: "CREATE TABLE #r (id int)\nGO\nSELECT bad FROM dbo.t\nGO\nSELECT * FROM #r", MaxRows: 1, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Statements != 3 || !result.Truncated || len(result.ResultSets) != 1 || result.ResultSets[0].Statement != 3 ||
		!reflect.DeepEqual(result.ResultSets[0].Rows, [][]any{{int64(1)}}) {
		t.Fatalf("result = %+v", result)
	}
	if !reflect.DeepEqual(result.Errors, []ScriptError{{Statement: 2, Line: 3, Error: "mssql: Invalid column name 'bad'. (207)"}}) {
		t.Fatalf("errors = %+v", result.Errors)
	}
	want := []string{"BEGIN", "SET LOCK_TIMEOUT 5000", "CREATE TABLE #r (id int)", "SELECT bad FROM dbo.t", "SELECT XACT_STATE()", "SELECT * FROM #r", "ROLLBACK"}
	if got := db.statements(); !reflect.DeepEqual(got, want) {
		t.Fatalf("statements = %q\nwant %q", got, want)
	}
}

func TestScriptStopsWhenTheErrorEndedTheSQLServerTransaction(t *testing.T) {
	q, db, _ := newTestQuerier(&sqlServerDialect, staticCreds{fallback: "app"})
	db.answers["SELECT bad"] = fakeAnswer{err: errors.New("deadlock")}
	db.answer("SELECT XACT_STATE()", []string{""}, []driver.Value{int64(0)})

	result, err := q.Script(t.Context(), &ScriptRequest{SQL: "SELECT bad\nGO\nSELECT 2", MaxRows: 10, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Error, "the script stopped") {
		t.Fatalf("errors = %+v", result.Errors)
	}
	for _, statement := range db.statements() {
		if statement == "SELECT 2" {
			t.Fatal("no batch may run once the transaction ended")
		}
	}
}

func TestScriptIsolatesPostgresStatementsWithSavepoints(t *testing.T) {
	q, db, _ := newTestQuerier(&postgresDialect, staticCreds{fallback: "app"})
	db.answers["SELECT bad"] = fakeAnswer{err: errors.New(`column "bad" does not exist`)}
	db.answer("SELECT 2", []string{"n"}, []driver.Value{int64(2)})

	result, err := q.Script(t.Context(), &ScriptRequest{SQL: "SELECT bad;\nSELECT 2;", MaxRows: 10, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) != 1 || result.Errors[0].Line != 1 || len(result.ResultSets) != 1 || result.ResultSets[0].Statement != 2 {
		t.Fatalf("result = %+v", result)
	}
	want := []string{
		"BEGIN READ ONLY", "SET LOCAL standard_conforming_strings = on", "SET LOCAL statement_timeout = '30s'",
		"SAVEPOINT airlock_script", "SELECT bad", "ROLLBACK TO SAVEPOINT airlock_script", "RELEASE SAVEPOINT airlock_script",
		"SAVEPOINT airlock_script", "SELECT 2", "RELEASE SAVEPOINT airlock_script", "ROLLBACK",
	}
	if got := db.statements(); !reflect.DeepEqual(got, want) {
		t.Fatalf("statements = %q\nwant %q", got, want)
	}
}

func TestScriptChecksEveryStatementBeforeRunning(t *testing.T) {
	for _, tc := range []struct {
		dialect *dialect
		script  string
		want    string
	}{
		{&postgresDialect, "SELECT 1;\nCREATE TEMP TABLE t (id int)", "statement 2 (line 2)"},
		{&mysqlDialect, "SELECT 1;\nCOMMIT;\nDELETE FROM users", "statement 2 (line 2)"},
		{&sqlServerDialect, "SELECT 1\nGO\nDELETE FROM dbo.users", "statement 2 (line 3)"},
	} {
		q, db, _ := newTestQuerier(tc.dialect, staticCreds{fallback: "app"})
		_, err := q.Script(t.Context(), &ScriptRequest{SQL: tc.script, MaxRows: 10, Timeout: time.Minute})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error = %v", tc.dialect.engine, err)
		}
		if got := db.statements(); len(got) != 0 {
			t.Fatalf("%s: nothing may run before every statement is checked, ran %q", tc.dialect.engine, got)
		}
	}
}

func TestReadScript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checks.SQL")
	if err := os.WriteFile(path, []byte("\xef\xbb\xbfSELECT 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	script, file, err := readScript(path, "")
	if err != nil || script != "SELECT 1" || file != path {
		t.Fatalf("readScript = %q, %q, %v", script, file, err)
	}
	if script, file, err = readScript("", "SELECT 2"); err != nil || script != "SELECT 2" || file != "" {
		t.Fatalf("inline = %q, %q, %v", script, file, err)
	}
	notSQL := filepath.Join(dir, "credentials")
	if err := os.WriteFile(notSQL, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, sql, want string }{
		{"checks.sql", "", "absolute"},
		{notSQL, "", ".sql"},
		{path, "SELECT 2", "not both"},
		{"", " ", "required"},
		{filepath.Join(dir, "missing.sql"), "", "no such file"},
	} {
		if _, _, err := readScript(tc.path, tc.sql); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("readScript(%q, %q) = %v, want an error with %q", tc.path, tc.sql, err, tc.want)
		}
	}
}

func TestRunScriptToolReadsTheFileAndAuditsItsHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checks.sql")
	if err := os.WriteFile(path, []byte("SELECT 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	inner := &fakeQuerier{}
	var events []*mcpserve.ToolEvent
	q := newRecordingQuerier(inner, &mcpserve.ToolEvent{Target: "app"}, func(e *mcpserve.ToolEvent) { events = append(events, e) })
	session := connect(t, testServerConfig(false), q)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "run_script", Arguments: map[string]any{"file_path": path, "timeout_seconds": 5000}})
	if err != nil || res.IsError {
		t.Fatalf("run_script = %+v, %v", res, err)
	}
	text, _ := res.Content[0].(*mcp.TextContent) //nolint:errcheck // a nil text fails below.
	if text == nil || !strings.Contains(text.Text, `"result_sets":[{"columns":["n"]`) || !strings.Contains(text.Text, `"statement":2`) {
		t.Fatalf("reply = %+v", res.Content)
	}
	if req := inner.lastScript; req.SQL != "SELECT 1" || req.File != path || req.MaxRows != defaultMaxRows || req.Timeout != maxScriptTimeout*time.Second {
		t.Fatalf("request = %+v", req)
	}
	if len(events) != 1 || events[0].Tool != "run_script" || events[0].SQL != "" || events[0].File != path ||
		events[0].SHA256 != "e004ebd5b5532a4b85984a62f8ad48a81aa3460c1ca07701f386135d72cdecf5" || events[0].Error != "1 of 2 statements failed" {
		t.Fatalf("events = %+v", events)
	}
}
