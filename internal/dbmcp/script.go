package dbmcp

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// maxScriptBytes caps the size of a script file.
	maxScriptBytes = 16 << 20
	// scriptSavepoint isolates each statement of a PostgreSQL script: an error aborts the
	// transaction up to it only.
	scriptSavepoint = "airlock_script"
)

// ScriptRequest is a read-only script to run.
type ScriptRequest struct {
	Database string
	SQL      string
	// File is the local file SQL was read from; empty for an inline script.
	File    string
	MaxRows int
	Timeout time.Duration
}

// ScriptResult is the outcome of a script: the result sets of its statements (batches on SQL
// Server), and the errors of those that failed, which did not stop the next ones.
type ScriptResult struct {
	ResultSets []ScriptResultSet `json:"result_sets"`
	Errors     []ScriptError     `json:"errors"`
	Statements int               `json:"statements"`
	// Truncated tells that rows or result sets were left out, max_rows being reached.
	Truncated bool `json:"truncated"`
}

// ScriptResultSet is one result set of a script, with the statement that returned it.
type ScriptResultSet struct {
	Result
	Statement int `json:"statement"`
}

// ScriptError is the error of one statement of a script, numbered from 1, at line Line of the script.
type ScriptError struct {
	Error     string `json:"error"`
	Statement int    `json:"statement"`
	Line      int    `json:"line"`
}

// scriptStatement is a statement (a batch on SQL Server) of a script, starting at line.
type scriptStatement struct {
	sql  string
	line int
}

// readScript returns the script of a run_script call: sql, or the content of the local .sql file
// at path. file is path when the script comes from it.
func readScript(path, sqlText string) (script, file string, err error) {
	switch {
	case path != "" && sqlText != "":
		return "", "", errors.New("give file_path or sql, not both")
	case path == "" && strings.TrimSpace(sqlText) == "":
		return "", "", errors.New("file_path or sql is required")
	case path == "":
		return sqlText, "", nil
	case !filepath.IsAbs(path):
		return "", "", fmt.Errorf("file_path must be an absolute path: %s", path)
	case !strings.EqualFold(filepath.Ext(path), ".sql"):
		return "", "", fmt.Errorf("file_path must name a .sql file: %s", path)
	}
	f, err := os.Open(path) // #nosec G304 -- a .sql file of the user, run under the read-only guards.
	if err != nil {
		return "", "", err
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // read only.
	data, err := io.ReadAll(io.LimitReader(f, maxScriptBytes+1))
	switch {
	case err != nil:
		return "", "", err
	case len(data) > maxScriptBytes:
		return "", "", fmt.Errorf("%s is larger than %d MiB", path, maxScriptBytes>>20)
	case !utf8.Valid(data):
		return "", "", fmt.Errorf("%s is not UTF-8 text: save it as UTF-8", path)
	}
	return string(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))), path, nil
}

// splitScript cuts script into statements: at semicolons, or at GO lines (batches) on SQL Server,
// outside comments and quoted text. Statements with no code are left out.
func splitScript(engine Engine, script string) ([]scriptStatement, error) {
	code, err := stripLiterals(engine, script)
	if err != nil {
		return nil, err
	}
	original, stripped := []rune(script), []rune(code)
	if len(original) != len(stripped) {
		return nil, errors.New("the script could not be split into statements")
	}
	var statements []scriptStatement
	// first is the line of the first code since start, 0 before any.
	start, line, first := 0, 1, 0
	lineStart, firstAtLineStart := 0, 0
	cut := func(end, next, firstLine int) {
		if firstLine > 0 {
			statements = append(statements, scriptStatement{sql: strings.TrimSpace(string(original[start:end])), line: firstLine})
		}
		start, first = next, 0
	}
	for i := 0; i <= len(stripped); i++ {
		if i == len(stripped) || stripped[i] == '\n' {
			if engine == EngineSQLServer && strings.EqualFold(strings.TrimSpace(string(stripped[lineStart:i])), "GO") {
				cut(lineStart, min(i+1, len(stripped)), firstAtLineStart)
			}
			line++
			lineStart, firstAtLineStart = i+1, first
			continue
		}
		switch r := stripped[i]; {
		case r == ';' && engine != EngineSQLServer:
			cut(i, i+1, first)
		case first == 0 && !unicode.IsSpace(r):
			first = line
		}
	}
	if start < len(original) {
		cut(len(original), len(original), first)
	}
	return statements, nil
}

// checkScriptStatement applies the read-only guard of engine to one statement of a script: a
// single read on PostgreSQL and MySQL, whose transactions are read-only; checkScriptBatch on SQL Server.
func checkScriptStatement(engine Engine, statement string) error {
	if engine == EngineSQLServer {
		return checkScriptBatch(statement)
	}
	return CheckReadOnly(engine, statement)
}

// scriptBatchStarts are the statements a T-SQL script batch may start with: a batch starting with
// another name runs the procedure of that name, without EXEC.
var scriptBatchStarts = map[string]bool{
	"SELECT": true, "WITH": true, "DECLARE": true, "SET": true, "IF": true, "WHILE": true, "BEGIN": true,
	"INSERT": true, "UPDATE": true, "DELETE": true, "CREATE": true, "DROP": true, "TRUNCATE": true,
	"PRINT": true, "RAISERROR": true, "THROW": true,
}

// scriptSessionOptions are the SET options a T-SQL script may change, as generated scripts do: they
// only change how this session reads statements and values, and are reset with the connection.
var scriptSessionOptions = map[string]bool{
	"NOCOUNT": true, "ANSI_NULLS": true, "QUOTED_IDENTIFIER": true, "ANSI_WARNINGS": true, "ANSI_PADDING": true,
	"ARITHABORT": true, "CONCAT_NULL_YIELDS_NULL": true, "DATEFORMAT": true, "DATEFIRST": true,
}

// checkScriptBatch is the read-only guard of a T-SQL script batch, which SQL Server cannot run in a
// read-only transaction: the reads CheckReadOnly allows, variables, control flow (IF, WHILE,
// BEGIN … END, TRY … CATCH), and writes to temporary tables (#name) and table variables (@name)
// only. Like CheckReadOnly, it is a guardrail: the transaction always rolled back and the grants of
// the login remain the boundary.
func checkScriptBatch(batch string) error {
	code, err := stripLiterals(EngineSQLServer, batch)
	if err != nil {
		return err
	}
	words := tokens(code, true)
	if len(words) == 0 {
		return nil
	}
	if !scriptBatchStarts[words[0]] {
		return fmt.Errorf("a batch may not start with %s (it would run a procedure of that name)", words[0])
	}
	at := func(i int) string {
		if i < 0 || i >= len(words) {
			return ""
		}
		return words[i]
	}
	for idx, word := range words {
		// target is the index of the object word writes to, 0 when it writes nothing.
		target := 0
		switch word {
		case "DECLARE", "BEGIN":
			// BEGIN TRAN still fails on TRAN.
			continue
		case "SET":
			if isTableVariable(at(idx+1)) || scriptSessionOptions[at(idx+1)] || (at(idx-2) == "UPDATE" && isTempObject(at(idx-1))) {
				continue
			}
			return fmt.Errorf("SET %s is not allowed in a script: only on a @variable, the columns of a temporary table, or display options such as NOCOUNT", at(idx+1))
		case "INSERT":
			target = idx + 1
			if at(target) == "INTO" {
				target++
			}
		case "DELETE":
			target = idx + 1
			if at(target) == "FROM" {
				target++
			}
		case "UPDATE", "INTO":
			target = idx + 1
		case "CREATE", "DROP", "TRUNCATE":
			if at(idx+1) != "TABLE" {
				return fmt.Errorf("%s is only allowed on temporary tables (#name) in a script", word)
			}
			target = idx + 2
			if word == "DROP" && at(target) == "IF" && at(target+1) == "EXISTS" {
				target += 2
			}
		}
		if target > 0 {
			for {
				if !isTempObject(at(target)) || at(target+1) == "." {
					return fmt.Errorf("%s is only allowed on temporary tables (#name) and table variables (@name) in a script", word)
				}
				if word != "DROP" || at(target+1) != "," {
					break
				}
				target += 2
			}
			continue
		}
		if forbiddenKeywords[word] {
			return fmt.Errorf("keyword %s is not allowed in a read-only script", word)
		}
		if sideEffectFunctions[word] {
			return fmt.Errorf("function %s is not allowed in a read-only script", word)
		}
		if word == "NEXT" && at(idx+1) == "VALUE" && at(idx+2) == "FOR" {
			return errors.New("NEXT VALUE FOR is not allowed in a read-only script")
		}
	}
	return nil
}

// isTempObject reports whether name is a local temporary table (#name) or a table variable (@name):
// objects of the session alone, unlike global temporary tables (##name).
func isTempObject(name string) bool {
	return isTableVariable(name) || (len(name) > 1 && name[0] == '#' && name[1] != '#')
}

func isTableVariable(name string) bool {
	return len(name) > 1 && name[0] == '@' && name[1] != '@'
}

// Script runs a read-only script in a transaction always rolled back, read-only on PostgreSQL and
// MySQL. Every statement is checked before the first one runs.
func (q *sqlQuerier) Script(ctx context.Context, req *ScriptRequest) (ScriptResult, error) {
	engine := q.dialect.engine
	statements, err := splitScript(engine, req.SQL)
	if err != nil {
		return ScriptResult{}, err
	}
	if len(statements) == 0 {
		return ScriptResult{}, errors.New("script is empty")
	}
	for idx, s := range statements {
		if checkErr := checkScriptStatement(engine, s.sql); checkErr != nil {
			return ScriptResult{}, fmt.Errorf("statement %d (line %d): %w", idx+1, s.line, checkErr)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	var result ScriptResult
	err = q.withPool(ctx, req.Database, func(db *sql.DB) error {
		result = ScriptResult{Statements: len(statements), ResultSets: []ScriptResultSet{}, Errors: []ScriptError{}}
		return q.readTx(ctx, db, nil, func(tx *sql.Tx) error {
			return q.runScript(ctx, tx, statements, req, &result)
		})
	})
	if err != nil {
		return ScriptResult{}, err
	}
	return result, nil
}

// runScript runs statements in tx, adding their result sets and errors to result.
func (q *sqlQuerier) runScript(ctx context.Context, tx *sql.Tx, statements []scriptStatement, req *ScriptRequest, result *ScriptResult) error {
	savepoints := q.dialect.engine == EnginePostgres
	rows := 0
	for idx, s := range statements {
		if savepoints {
			if _, err := tx.ExecContext(ctx, "SAVEPOINT "+scriptSavepoint); err != nil {
				return err
			}
		}
		sets, dropped, err := queryResultSets(ctx, tx, s.sql, req.MaxRows-rows)
		for _, set := range sets {
			result.ResultSets = append(result.ResultSets, ScriptResultSet{Result: set, Statement: idx + 1})
			rows += set.RowCount
			result.Truncated = result.Truncated || set.Truncated
		}
		result.Truncated = result.Truncated || dropped
		if ctx.Err() != nil {
			return fmt.Errorf("the script stopped after %s, at statement %d (line %d): %w", req.Timeout, idx+1, s.line, ctx.Err())
		}
		if err == nil {
			if savepoints {
				if _, releaseErr := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+scriptSavepoint); releaseErr != nil {
					return releaseErr
				}
			}
			continue
		}
		failure := ScriptError{Statement: idx + 1, Line: s.line, Error: err.Error()}
		stop := q.recoverScript(ctx, tx)
		if stop != nil {
			failure.Error += "; the script stopped: " + stop.Error()
		}
		result.Errors = append(result.Errors, failure)
		if stop != nil {
			return nil
		}
	}
	return nil
}

// recoverScript gets tx ready for the next statement after one failed; an error stops the script.
func (q *sqlQuerier) recoverScript(ctx context.Context, tx *sql.Tx) error {
	switch q.dialect.engine {
	case EnginePostgres:
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+scriptSavepoint); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+scriptSavepoint)
		return err
	case EngineSQLServer:
		// Some errors roll the transaction back (or doom it): the next batches would run outside it.
		var state int
		if err := tx.QueryRowContext(ctx, "SELECT XACT_STATE()").Scan(&state); err != nil {
			return err
		}
		if state != 1 {
			return errors.New("the error ended the transaction")
		}
	}
	// A MySQL error ends the statement only; a deadlock rolls the transaction back, after which
	// the next statements, all reads, run on their own.
	return nil
}

// queryResultSets runs statement and returns its result sets with at most maxRows rows in all;
// dropped tells that result sets were left out, maxRows being reached.
func queryResultSets(ctx context.Context, tx *sql.Tx, statement string, maxRows int) (sets []Result, dropped bool, err error) {
	rows, err := tx.QueryContext(ctx, statement)
	if err != nil {
		return nil, false, err
	}
	// ponytail: unlike query, a truncated result set is not cancelled (that would end the
	// transaction): pgx reads its remaining rows on Close, within the statement timeout.
	defer func() { _ = rows.Close() }() //nolint:errcheck // rows.Err has the error.
	for {
		columns, err := rows.Columns()
		if err != nil {
			return sets, dropped, err
		}
		switch {
		case len(columns) == 0:
			// A statement returning no rows (INSERT, SET…) in a T-SQL batch.
		case maxRows <= 0:
			dropped = dropped || rows.Next()
		default:
			set, err := collectRows(rows, maxRows)
			if err != nil {
				return sets, dropped, err
			}
			sets = append(sets, set)
			maxRows -= set.RowCount
		}
		if !rows.NextResultSet() {
			return sets, dropped, rows.Err()
		}
	}
}
