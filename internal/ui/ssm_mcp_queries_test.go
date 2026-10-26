package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
	"github.com/n2jsoft-public-org/airlock/internal/sessions"
)

func queryEvent(tool, sql string) mcpserve.ToolEvent {
	return mcpserve.ToolEvent{
		Time: time.Date(2026, 9, 29, 14, 5, 7, 0, time.Local), Tool: tool, Database: "archive", SQL: sql,
		Rows: new(int64(3)), Duration: 18 * time.Millisecond,
	}
}

// newMCPTestModel returns a dashboard with one database session serving an MCP server.
func newMCPTestModel(t *testing.T) ssmModel {
	t.Helper()
	s := testSession(1, "db")
	s.MCP = &sessions.MCP{LogDir: "/home/me/.config/jumphost/logs"}
	return newServiceModel(t, newFakeService(s))
}

// sendQuery feeds m a call made through the MCP server of session 1.
func sendQuery(t *testing.T, m ssmModel, query mcpserve.ToolEvent) ssmModel { //nolint:gocritic // a test value.
	t.Helper()
	m, _ = sendEvent(t, m, sessions.Event{Kind: sessions.EventMCPQuery, ID: 1, Query: &query})
	return m
}

func plainView(m ssmModel) string {
	return ansi.Strip(m.render())
}

func TestMCPQueryEventsAreKeptPerSessionAndCapped(t *testing.T) {
	m := newMCPTestModel(t)

	for i := range sessions.MaxMCPQueries + 5 {
		m = sendQuery(t, m, queryEvent("query", "SELECT "+strings.Repeat("x", i%3)))
	}
	queries := m.sessions[0].MCP
	if len(queries.Events) != sessions.MaxMCPQueries || queries.Total != sessions.MaxMCPQueries+5 {
		t.Fatalf("kept %d events, total %d", len(queries.Events), queries.Total)
	}
}

func TestWritableMCPSessionIsFlagged(t *testing.T) {
	for _, width := range []int{140, 80} { // sidebar cards, then the compact pills
		reader, writer := testSession(1, "reader"), testSession(2, "writer")
		reader.MCP, writer.MCP = &sessions.MCP{}, &sessions.MCP{Writes: true}
		m := newServiceModel(t, newFakeService(reader, writer))
		m = m.selectSession(0)
		m, _ = update(t, m, tea.WindowSizeMsg{Width: width, Height: 40})

		// The unselected writer is flagged too: its badge must be seen from any session.
		if got := strings.Count(plainView(m), "WRITES"); got != 1 {
			t.Fatalf("width %d: %d WRITES badges, want one for the writer:\n%s", width, got, plainView(m))
		}
	}
}

func TestMKeyTogglesTheQueriesPanel(t *testing.T) {
	m := newMCPTestModel(t)
	m = sendQuery(t, m, queryEvent("query", "SELECT id, name FROM users"))

	if strings.Contains(plainView(m), "SELECT id, name FROM users") {
		t.Fatal("queries must not be shown before pressing m")
	}
	m, _ = update(t, m, key("m"))
	view := plainView(m)
	for _, want := range []string{"MCP calls 1", "14:05:07  query · archive", "3 rows · 18ms", "SELECT id, name FROM users"} {
		if !strings.Contains(view, want) {
			t.Fatalf("queries panel misses %q:\n%s", want, view)
		}
	}

	m, _ = update(t, m, key("m"))
	if strings.Contains(plainView(m), "SELECT id, name FROM users") {
		t.Fatal("pressing m again must go back to the logs")
	}
}

func TestMKeyIgnoredWithoutMCPServer(t *testing.T) {
	m, _ := newSessionsModel(t, "cache")

	m, _ = update(t, m, key("m"))
	if m.showQueries {
		t.Fatal("m must not switch to queries for a session without MCP server")
	}
	if strings.Contains(plainView(m), "MCP calls") {
		t.Fatal("sessions without MCP server must not mention MCP calls")
	}
}

func TestQueriesPanelEmptyStateShowsLogDir(t *testing.T) {
	m := newMCPTestModel(t)
	m, _ = update(t, m, key("m"))

	view := plainView(m)
	for _, want := range []string{"No MCP calls yet", "/home/me/.config/jumphost/logs"} {
		if !strings.Contains(view, want) {
			t.Fatalf("empty queries panel misses %q:\n%s", want, view)
		}
	}
}

func TestQueriesPanelShowsErrorsAndWrites(t *testing.T) {
	m := newMCPTestModel(t)
	rejected := queryEvent("query", "DELETE FROM users")
	rejected.Rows, rejected.Error = nil, "only a single read-only SELECT or WITH statement is allowed"
	rejected.LogError = errors.New("disk full").Error()
	write := queryEvent("execute", "UPDATE users SET name = 'x'")

	m = sendQuery(t, m, rejected)
	m = sendQuery(t, m, write)
	m, _ = update(t, m, key("m"))

	view := plainView(m)
	for _, want := range []string{"✗ failed", "only a single read-only SELECT", "not saved to the audit log: disk full", "execute · archive", "3 rows affected"} {
		if !strings.Contains(view, want) {
			t.Fatalf("queries panel misses %q:\n%s", want, view)
		}
	}
}

func TestFooterHintsQueriesKeyForMCPSessions(t *testing.T) {
	if cache, _ := newSessionsModel(t, "cache"); strings.Contains(plainView(cache), "m  queries") {
		t.Fatal("sessions without MCP server must not hint the queries key")
	}
	m := newMCPTestModel(t)
	if !strings.Contains(plainView(m), "m  queries") {
		t.Fatalf("footer misses the queries key:\n%s", plainView(m))
	}
	m, _ = update(t, m, key("m"))
	if !strings.Contains(plainView(m), "m  logs") {
		t.Fatalf("footer misses the logs key:\n%s", plainView(m))
	}
}

func TestQueriesPanelShowsKubernetesAndCacheCalls(t *testing.T) {
	m := newMCPTestModel(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.Local)
	m = sendQuery(t, m, mcpserve.ToolEvent{Time: at, Tool: "list_resources", Engine: "kubernetes", Namespace: "web", Resource: "pods", Rows: new(int64(5))})
	m = sendQuery(t, m, mcpserve.ToolEvent{Time: at, Tool: "command", Engine: "redis", DB: new(0), Command: "HGETALL user:1", Rows: new(int64(2))})
	m, _ = update(t, m, key("m"))

	view := plainView(m)
	for _, want := range []string{"MCP calls 2", "list_resources · pods in web", "5 items", "list pods -n web", "command · db 0", "2 items", "HGETALL user:1"} {
		if !strings.Contains(view, want) {
			t.Fatalf("calls panel misses %q:\n%s", want, view)
		}
	}
}
