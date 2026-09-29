package dbmcp

import (
	"context"
	"sync"
	"time"
)

// QueryEvent is one MCP tool call made against a session's database.
type QueryEvent struct {
	Time time.Time `json:"time"`
	// Rows is the number of rows returned (read tools) or affected (execute); nil on error.
	Rows     *int64 `json:"rows,omitempty"`
	Server   string `json:"server"`
	Profile  string `json:"profile"`
	Region   string `json:"region"`
	Target   string `json:"target"`
	Engine   Engine `json:"engine"`
	Access   string `json:"access"`
	Tool     string `json:"tool"`
	Database string `json:"database,omitempty"`
	Schema   string `json:"schema,omitempty"`
	Table    string `json:"table,omitempty"`
	// SQL is the statement sent by the AI assistant (query and execute tools only).
	SQL   string `json:"sql,omitempty"`
	Error string `json:"error,omitempty"`
	// LogError explains why the event could not be written to the audit log (empty when it was).
	LogError  string        `json:"-"`
	Duration  time.Duration `json:"-"`
	MaxRows   int           `json:"max_rows,omitempty"`
	Truncated bool          `json:"truncated,omitempty"`
}

// queryRecorder writes events to the audit log and hands them to subscribers.
type queryRecorder struct {
	// log is nil when events are not written to disk.
	log         *AuditLog
	subscribers []func(QueryEvent)
	mu          sync.Mutex
}

func (r *queryRecorder) subscribe(fn func(QueryEvent)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subscribers = append(r.subscribers, fn)
}

// record never fails: an audit log error is reported to subscribers on the event.
func (r *queryRecorder) record(e *QueryEvent) {
	if r.log != nil {
		if err := r.log.Write(e); err != nil {
			e.LogError = err.Error()
		}
	}
	r.mu.Lock()
	subscribers := append([]func(QueryEvent){}, r.subscribers...)
	r.mu.Unlock()
	for _, fn := range subscribers {
		fn(*e)
	}
}

// recordingQuerier records every call made to its inner Querier, one event per MCP tool call.
type recordingQuerier struct {
	inner  Querier
	record func(*QueryEvent)
	base   QueryEvent
}

// newRecordingQuerier wraps inner; base carries the session fields copied into every event.
func newRecordingQuerier(inner Querier, base *QueryEvent, record func(*QueryEvent)) Querier {
	return &recordingQuerier{inner: inner, base: *base, record: record}
}

// start returns an event for tool and a function completing and recording it.
func (q *recordingQuerier) start(tool string) (e *QueryEvent, done func(rows int64, err error)) {
	event := q.base
	event.Tool = tool
	event.Time = time.Now()
	return &event, func(rows int64, err error) {
		event.Duration = time.Since(event.Time)
		if err != nil {
			event.Error = err.Error()
		} else {
			event.Rows = &rows
		}
		q.record(&event)
	}
}

func (q *recordingQuerier) Databases(ctx context.Context) ([]string, error) {
	_, done := q.start("list_databases")
	names, err := q.inner.Databases(ctx)
	done(int64(len(names)), err)
	return names, err
}

func (q *recordingQuerier) Tables(ctx context.Context, database, schema string) ([]Table, bool, error) {
	e, done := q.start("list_tables")
	e.Database, e.Schema = database, schema
	tables, truncated, err := q.inner.Tables(ctx, database, schema)
	e.Truncated = truncated
	done(int64(len(tables)), err)
	return tables, truncated, err
}

func (q *recordingQuerier) Describe(ctx context.Context, database, schema, table string) (TableDescription, error) {
	e, done := q.start("describe_table")
	e.Database, e.Schema, e.Table = database, schema, table
	description, err := q.inner.Describe(ctx, database, schema, table)
	done(int64(len(description.Columns)), err)
	return description, err
}

func (q *recordingQuerier) Query(ctx context.Context, database, query string, maxRows int) (Result, error) {
	e, done := q.start("query")
	e.Database, e.SQL, e.MaxRows = database, query, maxRows
	result, err := q.inner.Query(ctx, database, query, maxRows)
	e.Truncated = result.Truncated
	done(int64(result.RowCount), err)
	return result, err
}

func (q *recordingQuerier) Exec(ctx context.Context, database, statement string) (int64, error) {
	e, done := q.start("execute")
	e.Database, e.SQL = database, statement
	affected, err := q.inner.Exec(ctx, database, statement)
	done(affected, err)
	return affected, err
}

func (q *recordingQuerier) Close() error {
	return q.inner.Close()
}
