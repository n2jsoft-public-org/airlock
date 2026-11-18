package dbmcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// flakyQuerier fails its first failures listings, as a server whose tunnel is still opening.
type flakyQuerier struct {
	fakeQuerier
	failures int
	calls    int
}

func (f *flakyQuerier) Databases(context.Context) ([]string, error) {
	f.calls++
	if f.calls <= f.failures {
		return nil, errors.New("connection refused")
	}
	return []string{"app", "billing"}, nil
}

func TestListDatabasesOnceRetriesAFewTimes(t *testing.T) {
	listDatabasesRetry = time.Millisecond
	t.Cleanup(func() { listDatabasesRetry = 5 * time.Second })

	var got []string
	var tables map[string][]string
	flaky := &flakyQuerier{failures: 2}
	listDatabasesOnce(t.Context(), flaky, func(names []string, listed map[string][]string) { got, tables = names, listed })
	if !slices.Equal(got, []string{"app", "billing"}) || flaky.calls != 3 {
		t.Fatalf("got = %v after %d calls", got, flaky.calls)
	}
	if len(tables) != 2 || !slices.Equal(tables["billing"], []string{".users"}) {
		t.Fatalf("tables = %v", tables)
	}

	got = nil
	down := &flakyQuerier{failures: 100}
	listDatabasesOnce(t.Context(), down, func(names []string, _ map[string][]string) { got = names })
	if got != nil || down.calls != listDatabasesAttempts {
		t.Fatalf("got = %v after %d calls, want none after %d", got, down.calls, listDatabasesAttempts)
	}
}

func TestRecordingQuerierReportsListedDatabases(t *testing.T) {
	var got []string
	tables := map[string][]string{}
	q := &recordingQuerier{inner: &fakeQuerier{}, record: func(*mcpserve.ToolEvent) {}, onDatabases: func(names []string, listed map[string][]string) {
		got, tables = names, listed
	}}
	if _, err := q.Databases(t.Context()); err != nil {
		t.Fatal(err)
	}
	// An agent's list_databases lists no table: the catalog keeps those it knows.
	if !slices.Equal(got, []string{"app"}) || tables != nil {
		t.Fatalf("got = %v, tables = %v", got, tables)
	}
}

// partialQuerier lists the tables of app only, as a login that reaches some databases.
type partialQuerier struct{ fakeQuerier }

func (p *partialQuerier) Tables(_ context.Context, database, _ string) ([]Table, bool, error) {
	if database != "app" {
		return nil, false, errors.New("login failed")
	}
	return []Table{{Schema: "dbo", Name: "invoices"}}, false, nil
}

func TestListTablesSkipsUnreachableDatabasesAndCaps(t *testing.T) {
	databases := []string{"app", "locked"}
	for idx := range catalogDatabases {
		databases = append(databases, fmt.Sprintf("db%03d", idx))
	}
	tables := listTables(t.Context(), &partialQuerier{}, databases)
	if !slices.Equal(tables["app"], []string{"dbo.invoices"}) || len(tables) != 1 {
		t.Fatalf("tables = %v", tables)
	}
	all := listTables(t.Context(), &fakeQuerier{}, databases)
	if len(all) != catalogDatabases {
		t.Fatalf("listed %d databases, want %d", len(all), catalogDatabases)
	}
}
