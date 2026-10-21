package dbmcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	mssql "github.com/microsoft/go-mssqldb"
)

const (
	// queryTimeout bounds every database round trip made for a tool call.
	queryTimeout = 30 * time.Second
	// maxTables caps list_tables results.
	maxTables = 1000
)

// Table is a table or view.
type Table struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Type   string `json:"type"`
}

// Column describes a table column.
type Column struct {
	Default  *string `json:"default,omitempty"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable bool    `json:"nullable"`
}

// ForeignKey is one column of a foreign key constraint.
type ForeignKey struct {
	Name             string `json:"name"`
	Column           string `json:"column"`
	ReferencedSchema string `json:"referenced_schema"`
	ReferencedTable  string `json:"referenced_table"`
	ReferencedColumn string `json:"referenced_column"`
}

// Index is an index and its definition.
type Index struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
}

// TableDescription is the structure of one table.
type TableDescription struct {
	Schema      string       `json:"schema"`
	Table       string       `json:"table"`
	Columns     []Column     `json:"columns"`
	PrimaryKey  []string     `json:"primary_key"`
	ForeignKeys []ForeignKey `json:"foreign_keys"`
	Indexes     []Index      `json:"indexes"`
}

// Result is the outcome of a read query.
type Result struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	RowCount  int      `json:"row_count"`
	Truncated bool     `json:"truncated"`
}

// Querier runs the database side of the MCP tools.
type Querier interface {
	Databases(ctx context.Context) ([]string, error)
	Tables(ctx context.Context, database, schema string) (tables []Table, truncated bool, err error)
	Describe(ctx context.Context, database, schema, table string) (TableDescription, error)
	Query(ctx context.Context, database, query string, maxRows int) (Result, error)
	Exec(ctx context.Context, database, statement string) (int64, error)
	Plan(ctx context.Context, req *PlanRequest) (PlanResult, error)
	Close() error
}

// opener opens a connection pool to one database.
type opener func(ctx context.Context, database string) (*sql.DB, error)

// sqlQuerier implements Querier over database/sql, with one pool per database.
type sqlQuerier struct {
	creds     CredentialProvider
	open      opener
	pools     map[string]*sql.DB
	dialect   *dialect
	defaultDB string
	addr      Address
	mu        sync.Mutex
}

func newSQLQuerier(d *dialect, creds CredentialProvider, addr Address, open opener) *sqlQuerier {
	return &sqlQuerier{dialect: d, creds: creds, addr: addr, open: open, pools: map[string]*sql.DB{}}
}

func (q *sqlQuerier) pool(ctx context.Context, database string) (*sql.DB, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if database == "" {
		if q.defaultDB == "" {
			creds, err := q.creds.Credentials(ctx, "")
			if err != nil {
				return nil, err
			}
			q.defaultDB = creds.Database
		}
		database = q.defaultDB
	}
	if db, ok := q.pools[database]; ok {
		return db, nil
	}
	db, err := q.open(ctx, database)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	q.pools[database] = db
	return db, nil
}

// credentialRefresher is a CredentialProvider caching secrets that can be rotated while a session runs.
type credentialRefresher interface {
	// refresh forgets the cached secrets so the next Credentials call reads them again.
	refresh()
}

// credentialRejectionListener is a CredentialProvider told when the database rejected its
// credentials for good (after the refresh retry, if any).
type credentialRejectionListener interface {
	rejected()
}

// withPool runs fn on the pool of database. When the database rejects credentials read from a
// secret, the secret may have been rotated since it was read: fn runs once more on a new pool,
// with the secret read again. Rejected credentials fail at sign-in, before fn's statement runs.
func (q *sqlQuerier) withPool(ctx context.Context, database string, fn func(db *sql.DB) error) error {
	db, err := q.pool(ctx, database)
	if err != nil {
		return err
	}
	err = fn(db)
	if err == nil || !isAuthFailure(err) {
		return err
	}
	creds := unwrapProvider(q.creds)
	if refresher, ok := creds.(credentialRefresher); ok {
		refresher.refresh()
		q.dropPool(db)
		if db, err = q.pool(ctx, database); err != nil {
			return err
		}
		if err = fn(db); err == nil || !isAuthFailure(err) {
			return err
		}
	}
	if listener, ok := creds.(credentialRejectionListener); ok && isPasswordRejected(err) {
		listener.rejected()
	}
	return err
}

// dropPool closes db and forgets it, so the next call opens a new pool (SQL Server pools keep the
// credentials they were opened with). A pool another call already replaced is left alone.
func (q *sqlQuerier) dropPool(db *sql.DB) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for name, pool := range q.pools {
		if pool == db {
			delete(q.pools, name)
			_ = db.Close() //nolint:errcheck // the pool is dropped either way.
		}
	}
}

// read runs query in a transaction that is always rolled back, read-only where the engine supports it.
func (q *sqlQuerier) read(ctx context.Context, database string, maxRows int, query string, args ...any) (Result, error) {
	var result Result
	err := q.withPool(ctx, database, func(db *sql.DB) (err error) {
		result, err = q.readFrom(ctx, db, maxRows, query, args...)
		return err
	})
	return result, err
}

func (q *sqlQuerier) readFrom(ctx context.Context, db *sql.DB, maxRows int, query string, args ...any) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: q.dialect.readOnlyTx})
	if err != nil {
		return Result{}, q.connectionError(err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // the transaction only reads; ending it is all that matters.

	for _, statement := range q.dialect.txSetup {
		if _, setupErr := tx.ExecContext(ctx, statement); setupErr != nil {
			return Result{}, setupErr
		}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return Result{}, err
	}
	result, collectErr := collectRows(rows, maxRows)
	if collectErr == nil && result.Truncated {
		// Cancel before Close: some drivers (pgx) otherwise drain every remaining row on Close,
		// streaming the rest of a capped result through the tunnel until the statement times out.
		// The connection is left broken and discarded by the pool; the truncated result still stands.
		cancel()
	}
	_ = rows.Close() //nolint:errcheck // a truncated result fails Close with the cancellation; collectErr has the real error.
	if collectErr != nil {
		return Result{}, collectErr
	}
	return result, nil
}

func collectRows(rows *sql.Rows, maxRows int) (Result, error) {
	columns, err := rows.Columns()
	if err != nil {
		return Result{}, err
	}
	types := columnTypes(rows, len(columns))
	result := Result{Columns: columns, Rows: [][]any{}}
	for rows.Next() {
		if len(result.Rows) == maxRows {
			result.Truncated = true
			break
		}
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return Result{}, err
		}
		for i, value := range values {
			raw, isRaw := value.([]byte)
			switch {
			case isRaw && types[i] == "UNIQUEIDENTIFIER" && len(raw) == 16:
				values[i] = truncateText(mssqlGUID(raw))
			case isRaw && types[i] == "BIT" && len(raw) <= 8:
				values[i] = bitValue(raw)
			default:
				values[i] = jsonValue(value)
			}
		}
		result.Rows = append(result.Rows, values)
	}
	if err := rows.Err(); err != nil {
		return Result{}, err
	}
	result.RowCount = len(result.Rows)
	return result, nil
}

// columnTypes returns the upper-cased database type of each column (indexed as in rows.Columns()),
// e.g. SQL Server's UNIQUEIDENTIFIER or MySQL's BIT. Drivers that don't report column types (or an
// error) yield empty names.
func columnTypes(rows *sql.Rows, n int) []string {
	names := make([]string, n)
	types, err := rows.ColumnTypes()
	if err != nil {
		return names
	}
	for i, t := range types {
		if i < n {
			names[i] = strings.ToUpper(t.DatabaseTypeName())
		}
	}
	return names
}

// connectionError explains the usual causes of a failed connection: the tunnel is not up yet, the
// database reached directly cannot be reached or its certificate is not trusted, or the database
// rejected the credentials.
func (q *sqlQuerier) connectionError(err error) error {
	if q.addr.tunnel {
		if strings.Contains(err.Error(), "connection refused") {
			return fmt.Errorf("tunnel to localhost:%d is not accepting connections yet; retry shortly: %w", q.addr.Port, err)
		}
	} else if hint := directConnectionHint(q.addr, err); hint != "" {
		return fmt.Errorf("%s: %w", hint, err)
	}
	if hinter, ok := unwrapProvider(q.creds).(authFailureHinter); ok && isAuthFailure(err) {
		return fmt.Errorf("%s: %w", hinter.authFailureHint(), err)
	}
	return err
}

// authFailureHinter explains how to fix credentials the database rejected.
type authFailureHinter interface {
	authFailureHint() string
}

// isAuthFailure reports whether the database rejected the credentials (PostgreSQL 28P01, SQL Server
// 18456, MySQL 1045).
func isAuthFailure(err error) bool {
	if myErr, ok := errors.AsType[*mysql.MySQLError](err); ok {
		return myErr.Number == mysqlAccessDenied
	}
	text := err.Error()
	return strings.Contains(text, "28P01") || strings.Contains(text, "password authentication failed") ||
		strings.Contains(text, "Login failed for user")
}

// isPasswordRejected reports whether the database refused the password itself, which
// isAuthFailure does not tell apart on SQL Server: a login to an unknown or inaccessible database
// (error 4060) always comes with 18456 "Login failed", although the password was accepted.
func isPasswordRejected(err error) bool {
	if myErr, ok := errors.AsType[*mysql.MySQLError](err); ok {
		// 1044, access denied to a database, follows an accepted password.
		return myErr.Number == mysqlAccessDenied
	}
	if sqlErr, ok := errors.AsType[mssql.Error](err); ok {
		chain := sqlErr.All
		if len(chain) == 0 {
			chain = []mssql.Error{sqlErr}
		}
		rejected := false
		for i := range chain {
			switch chain[i].Number {
			case mssqlCannotOpenDatabase:
				return false
			case mssqlLoginFailed:
				rejected = true
			}
		}
		return rejected
	}
	text := err.Error()
	if strings.Contains(text, "28P01") || strings.Contains(text, "password authentication failed") {
		return true
	}
	return strings.Contains(text, "Login failed for user") && !strings.Contains(text, "Cannot open database")
}

// SQL Server error numbers of a refused login.
const (
	mssqlLoginFailed        = 18456
	mssqlCannotOpenDatabase = 4060
)

// mysqlAccessDenied is the MySQL error of refused credentials (ER_ACCESS_DENIED_ERROR).
const mysqlAccessDenied = 1045

func (q *sqlQuerier) Query(ctx context.Context, database, query string, maxRows int) (Result, error) {
	if err := CheckReadOnly(q.dialect.engine, query); err != nil {
		return Result{}, err
	}
	return q.read(ctx, database, maxRows, query)
}

func (q *sqlQuerier) Exec(ctx context.Context, database, statement string) (int64, error) {
	var affected int64
	err := q.withPool(ctx, database, func(db *sql.DB) error {
		execCtx, cancel := context.WithTimeout(ctx, queryTimeout)
		defer cancel()

		result, err := db.ExecContext(execCtx, statement)
		if err != nil {
			return q.connectionError(err)
		}
		affected, err = result.RowsAffected()
		return err
	})
	return affected, err
}

func (q *sqlQuerier) Databases(ctx context.Context) ([]string, error) {
	names, err := q.creds.Databases(ctx)
	if err != nil || names != nil {
		return names, err
	}
	result, err := q.read(ctx, "", maxTables, q.dialect.databasesSQL)
	if err != nil {
		return nil, err
	}
	names = make([]string, 0, len(result.Rows))
	for _, row := range result.Rows {
		names = append(names, text(row[0]))
	}
	return names, nil
}

func (q *sqlQuerier) Tables(ctx context.Context, database, schema string) ([]Table, bool, error) {
	result, err := q.read(ctx, database, maxTables, q.dialect.tablesSQL, schema)
	if err != nil {
		return nil, false, err
	}
	tables := make([]Table, 0, len(result.Rows))
	for _, row := range result.Rows {
		tables = append(tables, Table{Schema: text(row[0]), Name: text(row[1]), Type: text(row[2])})
	}
	return tables, result.Truncated, nil
}

func (q *sqlQuerier) Describe(ctx context.Context, database, schema, table string) (TableDescription, error) {
	if schema == "" {
		schema = q.dialect.defaultSchema
	}
	description := TableDescription{
		Schema: schema, Table: table,
		Columns: []Column{}, PrimaryKey: []string{}, ForeignKeys: []ForeignKey{}, Indexes: []Index{},
	}

	columns, err := q.read(ctx, database, maxTables, q.dialect.columnsSQL, schema, table)
	if err != nil {
		return TableDescription{}, err
	}
	if len(columns.Rows) == 0 {
		// MySQL has no default schema: the current database is.
		return TableDescription{}, fmt.Errorf("table %s not found", strings.TrimPrefix(schema+"."+table, "."))
	}
	for _, row := range columns.Rows {
		column := Column{Name: text(row[0]), Type: text(row[1]), Nullable: strings.EqualFold(text(row[2]), "YES")}
		if row[3] != nil {
			column.Default = new(text(row[3]))
		}
		description.Columns = append(description.Columns, column)
	}

	primaryKey, err := q.read(ctx, database, maxTables, q.dialect.primaryKeySQL, schema, table)
	if err != nil {
		return TableDescription{}, err
	}
	for _, row := range primaryKey.Rows {
		description.PrimaryKey = append(description.PrimaryKey, text(row[0]))
	}

	foreignKeys, err := q.read(ctx, database, maxTables, q.dialect.foreignKeysSQL, schema, table)
	if err != nil {
		return TableDescription{}, err
	}
	for _, row := range foreignKeys.Rows {
		description.ForeignKeys = append(description.ForeignKeys, ForeignKey{
			Name: text(row[0]), Column: text(row[1]),
			ReferencedSchema: text(row[2]), ReferencedTable: text(row[3]), ReferencedColumn: text(row[4]),
		})
	}

	indexes, err := q.read(ctx, database, maxTables, q.dialect.indexesSQL, schema, table)
	if err != nil {
		return TableDescription{}, err
	}
	for _, row := range indexes.Rows {
		description.Indexes = append(description.Indexes, Index{Name: text(row[0]), Definition: text(row[1])})
	}
	return description, nil
}

func (q *sqlQuerier) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	var errs []error
	for name, db := range q.pools {
		errs = append(errs, db.Close())
		delete(q.pools, name)
	}
	return errors.Join(errs...)
}

// reset closes every pool and forgets the default database, so the next call signs in again
// with the current credentials.
func (q *sqlQuerier) reset() error {
	err := q.Close()
	q.mu.Lock()
	q.defaultDB = ""
	q.mu.Unlock()
	return err
}

// text renders a converted column value as a string.
func text(value any) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
}
