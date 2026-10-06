package dbmcp

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeQuerier records tool calls and returns canned values.
type fakeQuerier struct {
	queryErr    error
	lastSQL     string
	lastDB      string
	lastMaxRows int
}

func (f *fakeQuerier) Databases(context.Context) ([]string, error) { return []string{"app"}, nil }

func (f *fakeQuerier) Tables(_ context.Context, database, schema string) ([]Table, bool, error) {
	f.lastDB = database
	return []Table{{Schema: schema, Name: "users", Type: "BASE TABLE"}}, false, nil
}

func (f *fakeQuerier) Describe(_ context.Context, _, schema, table string) (TableDescription, error) {
	return TableDescription{Schema: schema, Table: table, Columns: []Column{{Name: "id", Type: "integer"}},
		PrimaryKey: []string{"id"}, ForeignKeys: []ForeignKey{}, Indexes: []Index{}}, nil
}

func (f *fakeQuerier) Query(_ context.Context, database, query string, maxRows int) (Result, error) {
	f.lastDB, f.lastSQL, f.lastMaxRows = database, query, maxRows
	if f.queryErr != nil {
		return Result{}, f.queryErr
	}
	return Result{Columns: []string{"n"}, Rows: [][]any{{1}}, RowCount: 1}, nil
}

func (f *fakeQuerier) Exec(_ context.Context, _, statement string) (int64, error) {
	f.lastSQL = statement
	return 7, nil
}

func (*fakeQuerier) Close() error { return nil }

func connect(t *testing.T, cfg *ServerConfig, q Querier) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := NewServer(cfg, q).Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("closing the client session: %v", err)
		}
	})
	return session
}

func testServerConfig(allowWrites bool) *ServerConfig {
	return &ServerConfig{Name: "acme-dev-app", Target: "app", Profile: "dev", Region: "eu-west-3", Engine: EnginePostgres, AllowWrites: allowWrites}
}

func toolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

func call(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return result
}

func resultText(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if textContent, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, textContent.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestServerReadOnlyTools(t *testing.T) {
	session := connect(t, testServerConfig(false), &fakeQuerier{})

	got := strings.Join(toolNames(t, session), ",")
	if got != "describe_table,list_databases,list_tables,query" {
		t.Fatalf("tools = %s", got)
	}
	if instructions := session.InitializeResult().Instructions; !strings.Contains(instructions, "read-only") || !strings.Contains(instructions, "profile dev") {
		t.Fatalf("instructions = %q", instructions)
	}
}

func TestServerExposesExecuteOnlyWithWrites(t *testing.T) {
	q := &fakeQuerier{}
	session := connect(t, testServerConfig(true), q)

	if got := strings.Join(toolNames(t, session), ","); !strings.Contains(got, "execute") {
		t.Fatalf("tools = %s, want execute", got)
	}
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "execute" && (tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint) {
			t.Fatal("execute must be annotated as destructive")
		}
	}

	result := call(t, session, "execute", map[string]any{"sql": "DELETE FROM t"})
	if result.IsError || !strings.Contains(resultText(result), `"rows_affected":7`) {
		t.Fatalf("execute result = %s", resultText(result))
	}
	if instructions := session.InitializeResult().Instructions; !strings.Contains(instructions, "WRITES ARE ENABLED") {
		t.Fatalf("instructions = %q", instructions)
	}
}

func TestQueryToolDefaultsAndClampsRows(t *testing.T) {
	q := &fakeQuerier{}
	session := connect(t, testServerConfig(false), q)

	result := call(t, session, "query", map[string]any{"sql": "SELECT 1"})
	if result.IsError {
		t.Fatalf("query failed: %s", resultText(result))
	}
	if q.lastMaxRows != defaultMaxRows {
		t.Fatalf("max rows = %d, want %d", q.lastMaxRows, defaultMaxRows)
	}
	var structured Result
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &structured); err != nil || structured.RowCount != 1 {
		t.Fatalf("structured content = %s", raw)
	}

	call(t, session, "query", map[string]any{"sql": "SELECT 1", "max_rows": 5000, "database": "reporting"})
	if q.lastMaxRows != maxRowsLimit || q.lastDB != "reporting" {
		t.Fatalf("max rows = %d database = %q", q.lastMaxRows, q.lastDB)
	}
}

func TestQueryToolReportsErrorsToTheModel(t *testing.T) {
	session := connect(t, testServerConfig(false), &fakeQuerier{queryErr: errors.New(`relation "userz" does not exist`)})

	result := call(t, session, "query", map[string]any{"sql": "SELECT * FROM userz"})
	if !result.IsError || !strings.Contains(resultText(result), `relation "userz" does not exist`) {
		t.Fatalf("result = %+v", result)
	}
}

func TestQueryToolRequiresSQL(t *testing.T) {
	session := connect(t, testServerConfig(false), &fakeQuerier{})

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "query", Arguments: map[string]any{}})
	if err == nil && !result.IsError {
		t.Fatal("expected missing sql to be rejected")
	}
}

func TestDescribeAndListTools(t *testing.T) {
	q := &fakeQuerier{}
	session := connect(t, testServerConfig(false), q)

	if result := call(t, session, "describe_table", map[string]any{"table": "users"}); result.IsError || !strings.Contains(resultText(result), `"primary_key":["id"]`) {
		t.Fatalf("describe_table = %s", resultText(result))
	}
	if result := call(t, session, "list_tables", map[string]any{"schema": "public"}); result.IsError || !strings.Contains(resultText(result), `"name":"users"`) {
		t.Fatalf("list_tables = %s", resultText(result))
	}
	if result := call(t, session, "list_databases", nil); result.IsError || !strings.Contains(resultText(result), `"databases":["app"]`) {
		t.Fatalf("list_databases = %s", resultText(result))
	}
}

func TestInstructionsKeepTheTunnelText(t *testing.T) {
	want := "Database app (PostgreSQL dialect), reached through an airlock SSM tunnel. AWS profile dev, region eu-west-3. " +
		"Access is read-only. Explore with list_databases, list_tables and describe_table before writing queries. " +
		"The query tool runs one SELECT/WITH statement and returns at most 1000 rows."
	if got := instructions(testServerConfig(false)); got != want {
		t.Fatalf("instructions = %q\nwant %q", got, want)
	}
}

func TestInstructionsOfADirectDatabase(t *testing.T) {
	cfg := &ServerConfig{Name: "acme-legacy-billing", Target: "legacy-billing", Engine: EngineSQLServer, Via: "reached directly at billing.db.internal:1433."}
	got := instructions(cfg)
	if !strings.HasPrefix(got, "Database legacy-billing (T-SQL (SQL Server) dialect), reached directly at billing.db.internal:1433. Access is read-only. ") {
		t.Fatalf("instructions = %q", got)
	}
	if strings.Contains(got, "SSM") || strings.Contains(got, "AWS profile") {
		t.Fatalf("a direct database has no tunnel nor AWS profile: %q", got)
	}
}
