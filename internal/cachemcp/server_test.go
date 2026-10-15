package cachemcp

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

func connect(t *testing.T, c *cache, allowWrites bool) (*mcp.ClientSession, *[]mcpserve.ToolEvent) {
	t.Helper()
	var events []mcpserve.ToolEvent
	server := NewServer(&ServerConfig{Name: "acme-dev-sessions", Target: "sessions", Profile: "dev", Region: "eu-west-3", Engine: "valkey", AllowWrites: allowWrites},
		c, func(e *mcpserve.ToolEvent) { events = append(events, *e) })
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Log(err)
		}
	})
	return session, &events
}

func toolNames(t *testing.T, session *mcp.ClientSession) string {
	t.Helper()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (text string, isError bool) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, content := range result.Content {
		if tc, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n"), result.IsError
}

func TestServerToolsDependOnWriteAccess(t *testing.T) {
	c, _ := testCache(t)
	readOnly, _ := connect(t, c, false)
	if got := toolNames(t, readOnly); got != "command,get_key,info,scan_keys" {
		t.Fatalf("read-only tools = %s", got)
	}
	writable, _ := connect(t, c, true)
	if got := toolNames(t, writable); got != "command,execute,get_key,info,scan_keys" {
		t.Fatalf("writable tools = %s", got)
	}
}

func TestServerRecordsEveryCall(t *testing.T) {
	c, m := testCache(t)
	seed(t, m.Set("user:1", "alice"))
	session, events := connect(t, c, true)

	if text, isErr := callTool(t, session, "get_key", map[string]any{"key": "user:1"}); isErr || !strings.Contains(text, "alice") {
		t.Fatalf("get_key = %s", text)
	}
	if text, isErr := callTool(t, session, "command", map[string]any{"args": []string{"SET", "user:1", "bob"}}); !isErr || !strings.Contains(text, "read-only guard") {
		t.Fatalf("command SET = %s", text)
	}
	if text, isErr := callTool(t, session, "execute", map[string]any{"args": []string{"SET", "user:1", "bob"}, "db": 0}); isErr {
		t.Fatalf("execute = %s", text)
	}
	if text, _ := callTool(t, session, "scan_keys", map[string]any{"pattern": "user:*"}); !strings.Contains(text, "user:1") {
		t.Fatalf("scan_keys = %s", text)
	}

	got := make([]string, 0, len(*events))
	for _, e := range *events {
		if e.DB == nil || *e.DB != 0 || e.Engine != "valkey" || e.Access != "read-write" {
			t.Fatalf("event = %+v", e)
		}
		got = append(got, e.Tool+"|"+e.Command+"|"+e.Error)
	}
	want := []string{
		"get_key|get_key user:1|",
		"command|SET user:1 bob|" + (*events)[1].Error,
		"execute|SET user:1 bob|",
		"scan_keys|SCAN 0 MATCH user:*|",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") || (*events)[1].Error == "" {
		t.Fatalf("events =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestServerRecordsTruncatedCommands(t *testing.T) {
	c, m := testCache(t)
	seed(t, m.Set("big", strings.Repeat("x", maxStringBytes+1)))
	session, events := connect(t, c, false)
	if _, isErr := callTool(t, session, "command", map[string]any{"args": []string{"GET", "big"}}); isErr {
		t.Fatal("command GET failed")
	}
	if len(*events) != 1 || !(*events)[0].Truncated {
		t.Fatalf("events = %+v", *events)
	}
}

func TestServerInfoRecordsSections(t *testing.T) {
	c, _ := testCache(t)
	session, events := connect(t, c, false)
	if text, isErr := callTool(t, session, "info", map[string]any{"section": "clients"}); isErr || !strings.Contains(text, "connected_clients") {
		t.Fatalf("info = %s", text)
	}
	if (*events)[0].Command != "INFO clients" || (*events)[0].Access != "read-only" {
		t.Fatalf("event = %+v", (*events)[0])
	}
}

func TestServerToolsRefuseADatabaseOutOfRange(t *testing.T) {
	c, _ := testCache(t)
	session, _ := connect(t, c, true)
	calls := map[string]map[string]any{
		"info":      {},
		"scan_keys": {},
		"get_key":   {"key": "k"},
		"command":   {"args": []string{"GET", "k"}},
		"execute":   {"args": []string{"SET", "k", "v"}},
	}
	for tool, args := range calls {
		for _, db := range []int{-1, 1024} {
			args["db"] = db
			if text, isErr := callTool(t, session, tool, args); !isErr || !strings.Contains(text, "db must be between 0 and 1023") {
				t.Errorf("%s with db %d = %s, want refused", tool, db, text)
			}
		}
	}
	c.clients.mu.Lock()
	defer c.clients.mu.Unlock()
	if len(c.clients.byDB) != 0 {
		t.Fatalf("clients created for %d databases, want none", len(c.clients.byDB))
	}
}

func TestInstructionsDescribeHowTheCacheIsReached(t *testing.T) {
	local := instructions(&ServerConfig{Engine: "redis", Target: "docker cache", Via: "a local Docker container reached directly at 127.0.0.1:6379."})
	if !strings.Contains(local, "a local Docker container reached directly at 127.0.0.1:6379.") ||
		strings.Contains(local, "ElastiCache") || strings.Contains(local, "profile") {
		t.Fatalf("local instructions = %q", local)
	}
	aws := instructions(&ServerConfig{Engine: "valkey", Target: "sessions", Profile: "dev", Region: "eu-west-3"})
	if !strings.Contains(aws, "(Amazon ElastiCache), reached through an airlock SSM tunnel. AWS profile dev, region eu-west-3.") {
		t.Fatalf("elasticache instructions = %q", aws)
	}
}

// An integer reply over 2^53 must reach the client exactly, not rounded through a float64.
func TestServerKeepsBigIntegers(t *testing.T) {
	c, _ := testCache(t)
	session, _ := connect(t, c, true)
	text, isErr := callTool(t, session, "execute", map[string]any{"args": []string{"INCRBY", "counter", "1234567890123456789"}})
	if isErr || !strings.Contains(text, `"reply":1234567890123456789`) {
		t.Fatalf("execute = %s", text)
	}
}
