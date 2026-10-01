package dbmcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/mcpserve"
)

func testBaseEvent() mcpserve.ToolEvent {
	return mcpserve.ToolEvent{Server: "acme-dev-app", Profile: "dev", Region: "eu-west-3", Target: "app", Engine: "postgres", Access: "read-only"}
}

// recorded wraps inner with a recorder collecting its events.
func recorded(inner Querier) (Querier, *[]mcpserve.ToolEvent) {
	var events []mcpserve.ToolEvent
	base := testBaseEvent()
	return newRecordingQuerier(inner, &base, func(e *mcpserve.ToolEvent) { events = append(events, *e) }), &events
}

func onlyEvent(t *testing.T, events []mcpserve.ToolEvent) mcpserve.ToolEvent {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("recorded %d events, want 1: %+v", len(events), events)
	}
	e := events[0]
	if e.Time.IsZero() || e.Duration < 0 {
		t.Fatalf("event time = %v, duration = %v", e.Time, e.Duration)
	}
	if e.Server != "acme-dev-app" || e.Profile != "dev" || e.Region != "eu-west-3" || e.Target != "app" || e.Engine != "postgres" || e.Access != "read-only" {
		t.Fatalf("session fields not copied: %+v", e)
	}
	return e
}

func TestRecordingQuerierRecordsQuery(t *testing.T) {
	inner := &fakeQuerier{}
	q, events := recorded(inner)

	result, err := q.Query(context.Background(), "archive", "SELECT 1", 50)
	if err != nil || result.RowCount != 1 {
		t.Fatalf("result = %+v, err = %v; want the inner result", result, err)
	}
	e := onlyEvent(t, *events)
	if e.Tool != "query" || e.Database != "archive" || e.SQL != "SELECT 1" || e.MaxRows != 50 {
		t.Fatalf("event = %+v", e)
	}
	if e.Rows == nil || *e.Rows != 1 || e.Error != "" {
		t.Fatalf("rows = %v, error = %q", e.Rows, e.Error)
	}
}

func TestRecordingQuerierRecordsRejectedQuery(t *testing.T) {
	guardErr := CheckReadOnly(EnginePostgres, "DELETE FROM users")
	if guardErr == nil {
		t.Fatal("the guard must reject DELETE")
	}
	q, events := recorded(&fakeQuerier{queryErr: guardErr})

	if _, err := q.Query(context.Background(), "", "DELETE FROM users", 200); !errors.Is(err, guardErr) {
		t.Fatalf("err = %v, want the inner error", err)
	}
	e := onlyEvent(t, *events)
	if e.SQL != "DELETE FROM users" || e.Error != guardErr.Error() || e.Rows != nil {
		t.Fatalf("event = %+v", e)
	}
}

func TestRecordingQuerierRecordsExecute(t *testing.T) {
	q, events := recorded(&fakeQuerier{})

	affected, err := q.Exec(context.Background(), "archive", "UPDATE users SET name = 'x'")
	if err != nil || affected != 7 {
		t.Fatalf("affected = %d, err = %v", affected, err)
	}
	e := onlyEvent(t, *events)
	if e.Tool != "execute" || e.SQL != "UPDATE users SET name = 'x'" || e.Rows == nil || *e.Rows != 7 {
		t.Fatalf("event = %+v", e)
	}
}

func TestRecordingQuerierRecordsMetadataTools(t *testing.T) {
	ctx := context.Background()
	q, events := recorded(&fakeQuerier{})

	if _, err := q.Databases(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := q.Tables(ctx, "archive", "public"); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Describe(ctx, "archive", "public", "users"); err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, e := range *events {
		if e.SQL != "" {
			t.Fatalf("metadata tool %s must not record SQL: %q", e.Tool, e.SQL)
		}
		got = append(got, strings.Join([]string{e.Tool, e.Database, e.Schema, e.Table}, "|"))
	}
	want := []string{"list_databases|||", "list_tables|archive|public|", "describe_table|archive|public|users"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events = %q, want %q", got, want)
	}
}
