package dbmcp

import (
	"context"
	"errors"
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
	flaky := &flakyQuerier{failures: 2}
	listDatabasesOnce(t.Context(), flaky, func(names []string) { got = names })
	if !slices.Equal(got, []string{"app", "billing"}) || flaky.calls != 3 {
		t.Fatalf("got = %v after %d calls", got, flaky.calls)
	}

	got = nil
	down := &flakyQuerier{failures: 100}
	listDatabasesOnce(t.Context(), down, func(names []string) { got = names })
	if got != nil || down.calls != listDatabasesAttempts {
		t.Fatalf("got = %v after %d calls, want none after %d", got, down.calls, listDatabasesAttempts)
	}
}

func TestRecordingQuerierReportsListedDatabases(t *testing.T) {
	var got []string
	q := &recordingQuerier{inner: &fakeQuerier{}, record: func(*mcpserve.ToolEvent) {}, onDatabases: func(names []string) { got = names }}
	if _, err := q.Databases(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"app"}) {
		t.Fatalf("got = %v", got)
	}
}
