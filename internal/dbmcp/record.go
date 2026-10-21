package dbmcp

import (
	"context"

	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// recordingQuerier records every call made to its inner Querier, one event per MCP tool call.
type recordingQuerier struct {
	inner  Querier
	record func(*mcpserve.ToolEvent)
	base   mcpserve.ToolEvent
}

// newRecordingQuerier wraps inner; base carries the session fields copied into every event.
func newRecordingQuerier(inner Querier, base *mcpserve.ToolEvent, record func(*mcpserve.ToolEvent)) Querier {
	return &recordingQuerier{inner: inner, base: *base, record: record}
}

// start returns an event for tool and a function completing and recording it.
func (q *recordingQuerier) start(tool string) (e *mcpserve.ToolEvent, done func(rows int64, err error)) {
	return mcpserve.StartEvent(&q.base, tool, q.record)
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
	var columns int
	if err == nil {
		columns = len(description.Columns)
	}
	done(int64(columns), err)
	return description, err
}

func (q *recordingQuerier) Query(ctx context.Context, database, query string, maxRows int) (Result, error) {
	e, done := q.start("query")
	e.Database, e.SQL, e.MaxRows = database, query, maxRows
	result, err := q.inner.Query(ctx, database, query, maxRows)
	var rows int
	if err == nil {
		e.Truncated, rows = result.Truncated, result.RowCount
	}
	done(int64(rows), err)
	return result, err
}

func (q *recordingQuerier) Exec(ctx context.Context, database, statement string) (int64, error) {
	e, done := q.start("execute")
	e.Database, e.SQL = database, statement
	affected, err := q.inner.Exec(ctx, database, statement)
	done(affected, err)
	return affected, err
}

func (q *recordingQuerier) Plan(ctx context.Context, req *PlanRequest) (PlanResult, error) {
	e, done := q.start("query_plan")
	e.Database, e.SQL = req.Database, req.SQL
	result, err := q.inner.Plan(ctx, req)
	var rows int64
	if err == nil && result.RowCount != nil {
		rows = *result.RowCount
	}
	done(rows, err)
	return result, err
}

func (q *recordingQuerier) Close() error {
	return q.inner.Close()
}
