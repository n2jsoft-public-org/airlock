package dbmcp

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
)

// fakeDB is a database/sql driver that records statements and answers queries by prefix.
type fakeDB struct {
	// connectErr, when set, fails every new connection.
	connectErr error
	answers    map[string]fakeAnswer
	log        []string
	mu         sync.Mutex
}

type fakeAnswer struct {
	err     error
	columns []string
	rows    [][]driver.Value
}

func newFakeDB() *fakeDB { return &fakeDB{answers: map[string]fakeAnswer{}} }

func (f *fakeDB) answer(prefix string, columns []string, rows ...[]driver.Value) {
	f.answers[prefix] = fakeAnswer{columns: columns, rows: rows}
}

func (f *fakeDB) record(entry string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, entry)
}

func (f *fakeDB) statements() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.log...)
}

// opener returns an opener recording which databases were opened.
func (f *fakeDB) opener(opened *[]string) opener {
	return func(_ context.Context, database string) (*sql.DB, error) {
		*opened = append(*opened, database)
		return sql.OpenDB(fakeConnector{db: f}), nil
	}
}

type fakeConnector struct{ db *fakeDB }

func (c fakeConnector) Connect(context.Context) (driver.Conn, error) {
	if c.db.connectErr != nil {
		return nil, c.db.connectErr
	}
	return fakeConn(c), nil
}
func (fakeConnector) Driver() driver.Driver { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use the connector") }

type fakeConn struct{ db *fakeDB }

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("prepare not supported") }
func (fakeConn) Close() error                        { return nil }
func (fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("use BeginTx") }

func (c fakeConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if opts.ReadOnly {
		c.db.record("BEGIN READ ONLY")
	} else {
		c.db.record("BEGIN")
	}
	return fakeTx(c), nil
}

func (c fakeConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.db.record(query)
	if answer, ok := c.db.lookup(query); ok && answer.err != nil {
		return nil, answer.err
	}
	return driver.RowsAffected(3), nil
}

func (c fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	entry := query
	for _, arg := range args {
		entry += " | " + arg.Value.(string)
	}
	c.db.record(entry)
	answer, ok := c.db.lookup(query)
	if !ok {
		return &fakeRows{}, nil
	}
	if answer.err != nil {
		return nil, answer.err
	}
	return &fakeRows{columns: answer.columns, rows: answer.rows}, nil
}

func (f *fakeDB) lookup(query string) (fakeAnswer, bool) {
	for prefix, answer := range f.answers {
		if strings.HasPrefix(query, prefix) {
			return answer, true
		}
	}
	return fakeAnswer{}, false
}

type fakeTx struct{ db *fakeDB }

func (t fakeTx) Commit() error   { t.db.record("COMMIT"); return nil }
func (t fakeTx) Rollback() error { t.db.record("ROLLBACK"); return nil }

type fakeRows struct {
	columns []string
	rows    [][]driver.Value
	next    int
}

func (r *fakeRows) Columns() []string { return r.columns }
func (*fakeRows) Close() error        { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}
