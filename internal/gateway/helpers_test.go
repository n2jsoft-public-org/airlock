package gateway

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/n2jsoft-public-org/airlock/internal/dbmcp"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
	"github.com/n2jsoft-public-org/airlock/internal/sessions"
	"github.com/n2jsoft-public-org/airlock/internal/sessions/sessionstest"
)

func newTestManager(t *testing.T, runner *sessionstest.Runner) (*sessions.Manager, *sessionstest.Launcher) {
	t.Helper()
	launcher := sessionstest.NewLauncher()
	m := sessions.NewManager(context.Background(), sessions.Config{Runner: sessions.Fixed(runner), Launch: launcher.Launch})
	t.Cleanup(m.Shutdown)
	return m, launcher
}

// openDirect opens the direct database name served by server, as openedBy ("" for the user).
//
//nolint:unparam // writes is true in the open_session tests.
func openDirect(t *testing.T, m *sessions.Manager, runner *sessionstest.Runner, name string, server *mcp.Server, writes bool, openedBy string) sessions.ID {
	t.Helper()
	runner.Direct(name)
	if server != nil {
		runner.WithMCP(name, &sessionstest.MCPServer{S: server, Writes: writes})
	}
	id, err := m.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: sessions.DirectResourceType, Name: name, OpenedBy: openedBy}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	cs, err := connectClient(t.Context(), server)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() }) //nolint:errcheck // an in-memory session.
	return cs
}

// callTool calls tool and returns the text of its result and whether it is an error.
func callTool(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (text string, isError bool) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var parts []string
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n"), res.IsError
}

// fakeQuerier answers the database tools and records the queries.
type fakeQuerier struct {
	queries []string
	execs   []string
	mu      sync.Mutex
}

func (q *fakeQuerier) Databases(context.Context) ([]string, error) { return []string{"orders"}, nil }
func (q *fakeQuerier) Tables(context.Context, string, string) ([]dbmcp.Table, bool, error) {
	return nil, false, nil
}
func (q *fakeQuerier) Describe(context.Context, string, string, string) (dbmcp.TableDescription, error) {
	return dbmcp.TableDescription{}, nil
}
func (q *fakeQuerier) Query(_ context.Context, _, query string, _ int) (dbmcp.Result, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queries = append(q.queries, query)
	return dbmcp.Result{Columns: []string{"n"}, Rows: [][]any{{1}}, RowCount: 1}, nil
}
func (q *fakeQuerier) Exec(_ context.Context, _, statement string) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.execs = append(q.execs, statement)
	return 1, nil
}
func (q *fakeQuerier) Plan(_ context.Context, req *dbmcp.PlanRequest) (dbmcp.PlanResult, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queries = append(q.queries, req.SQL)
	return dbmcp.PlanResult{Mode: req.Mode, Statements: []dbmcp.StatementPlan{}}, nil
}
func (q *fakeQuerier) Close() error { return nil }

func (q *fakeQuerier) ran() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.queries...)
}

func (q *fakeQuerier) executed() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.execs...)
}
