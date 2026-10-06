package gateway

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/n2jsoft/airlock/internal/dbmcp"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/sessions/sessionstest"
)

// relayServer is a gateway server with the relayed tools only, applying settings, and a client of it.
func relayServer(t *testing.T, m *sessions.Manager, settings Settings) (*relay, *mcp.ClientSession) {
	t.Helper()
	r := newRelay(t.Context(), m, time.Now, func() Settings { return settings })
	t.Cleanup(r.closeAll)
	server := mcp.NewServer(&mcp.Implementation{Name: "airlock", Version: serverVersion}, nil)
	if err := r.add(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	return r, connect(t, server)
}

func TestRelayedToolsMirrorTheSessionTools(t *testing.T) {
	m, _ := newTestManager(t, sessionstest.NewRunner())
	_, cs := relayServer(t, m, Settings{})
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		tools[tool.Name] = tool
	}
	for _, name := range []string{"db_list_databases", "db_list_tables", "db_describe_table", "db_query", "db_execute",
		"k8s_list_namespaces", "k8s_list_resource_types", "k8s_list_resources", "k8s_get_resource", "k8s_pod_logs", "k8s_list_events",
		"cache_info", "cache_scan_keys", "cache_get_key", "cache_command", "cache_execute"} {
		tool, ok := tools[name]
		if !ok {
			t.Errorf("missing %s", name)
			continue
		}
		schema, _ := tool.InputSchema.(map[string]any) //nolint:errcheck // checked through required.
		required, _ := schema["required"].([]any)      //nolint:errcheck // checked below.
		if !slices.Contains(required, any("session")) {
			t.Errorf("%s: session is not required: %v", name, schema)
		}
	}
	if strings.Contains(tools["db_query"].Description, "PostgreSQL") {
		t.Fatalf("db_query names one dialect: %q", tools["db_query"].Description)
	}
	if hint := tools["db_execute"].Annotations.DestructiveHint; hint == nil || !*hint {
		t.Fatal("db_execute must keep its destructive hint")
	}
}

func TestRelayCallsTheSessionServer(t *testing.T) {
	runner := sessionstest.NewRunner()
	m, _ := newTestManager(t, runner)
	q := &fakeQuerier{}
	id := openDirect(t, m, runner, "billing", dbmcp.NewServer(&dbmcp.ServerConfig{Name: "airlock-billing", Target: "billing"}, q), false, "")
	r, cs := relayServer(t, m, Settings{AgentWrites: true})

	text, isError := callTool(t, cs, "db_query", map[string]any{"session": int(id), "sql": "select 1"})
	if isError || !strings.Contains(text, `"row_count":1`) {
		t.Fatalf("db_query = %q (error %v)", text, isError)
	}
	// Review Focus 4: a quoted id works too.
	if text, isError = callTool(t, cs, "db_query", map[string]any{"session": itoa(id), "sql": "select 2"}); isError {
		t.Fatalf("quoted session id refused: %q", text)
	}
	if got := q.ran(); !slices.Equal(got, []string{"select 1", "select 2"}) {
		t.Fatalf("queries = %v", got)
	}
	if _, ok := r.lastCallOf(id); !ok {
		t.Fatal("the call time of the session must be recorded")
	}
	if text, isError = callTool(t, cs, "db_execute", map[string]any{"session": int(id), "sql": "delete from t"}); !isError || !strings.Contains(text, "read-only") {
		t.Fatalf("execute on a read-only session = %q", text)
	}
}

func TestRelayRefusals(t *testing.T) {
	runner := sessionstest.NewRunner()
	m, _ := newTestManager(t, runner)
	runner.AWS("platform", "eks")
	runner.WithMCP("platform", &sessionstest.MCPServer{S: mcp.NewServer(&mcp.Implementation{Name: "k", Version: "1"}, nil)})
	eks, err := m.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: "eks", Name: "platform"}})
	if err != nil {
		t.Fatal(err)
	}
	plain := openDirect(t, m, runner, "plain", nil, false, "")
	_, cs := relayServer(t, m, Settings{})

	cases := []struct {
		args map[string]any
		tool string
		want string
	}{
		{tool: "db_query", args: map[string]any{"sql": "select 1"}, want: "session is required"},
		{tool: "db_query", args: map[string]any{"session": 99, "sql": "select 1"}, want: "no session 99; call list_sessions"},
		{tool: "db_query", args: map[string]any{"session": int(eks), "sql": "select 1"}, want: "k8s_"},
		{tool: "db_list_databases", args: map[string]any{"session": int(plain)}, want: "has no MCP server"},
	}
	for _, tc := range cases {
		if text, isError := callTool(t, cs, tc.tool, tc.args); !isError || !strings.Contains(text, tc.want) {
			t.Errorf("%s %v = %q (error %v), want %q", tc.tool, tc.args, text, isError, tc.want)
		}
	}
	if err := m.Stop(t.Context(), plain); err != nil {
		t.Fatal(err)
	}
	if text, isError := callTool(t, cs, "db_list_databases", map[string]any{"session": int(plain)}); !isError || !strings.Contains(text, "ended") {
		t.Fatalf("ended session = %q", text)
	}
}

// F2: with agentWrites false, no session takes writes through the gateway, the user's included.
func TestRelayExecuteFollowsAgentWrites(t *testing.T) {
	runner := sessionstest.NewRunner()
	m, _ := newTestManager(t, runner)
	q := &fakeQuerier{}
	id := openDirect(t, m, runner, "billing", dbmcp.NewServer(&dbmcp.ServerConfig{Name: "airlock-billing", Target: "billing", AllowWrites: true}, q), true, "")
	args := map[string]any{"session": int(id), "sql": "delete from t"}

	r, off := relayServer(t, m, Settings{})
	var audited []mcpserve.ToolEvent
	r.audit("airlock", func(e *mcpserve.ToolEvent) { audited = append(audited, *e) })
	if text, isError := callTool(t, off, "db_execute", args); !isError || !strings.Contains(text, "agent writes are off") {
		t.Fatalf("execute with agentWrites false = %q (error %v)", text, isError)
	}
	if got := q.executed(); len(got) != 0 {
		t.Fatalf("executed = %v", got)
	}
	// The refused attempt is in the audit log, with what the agent tried.
	if len(audited) != 1 || audited[0].Tool != "db_execute" || audited[0].Server != "airlock" || audited[0].Target != "billing" ||
		audited[0].SQL != "delete from t" || !strings.HasPrefix(audited[0].Decision, "refused: agent writes are off") {
		t.Fatalf("audited = %+v", audited)
	}
	_, on := relayServer(t, m, Settings{AgentWrites: true})
	if text, isError := callTool(t, on, "db_execute", args); isError {
		t.Fatalf("execute with agentWrites true = %q", text)
	}
	if got := q.executed(); !slices.Equal(got, []string{"delete from t"}) {
		t.Fatalf("executed = %v", got)
	}
}

func itoa(id sessions.ID) string { return strconv.Itoa(int(id)) }

func TestAgentName(t *testing.T) {
	cases := map[string]string{"claude-code/2.1.0 (darwin)": "claude-code", "": "agent", "<script>": "script", "Codex CLI": "Codex"}
	for ua, want := range cases {
		req := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: http.Header{"User-Agent": {ua}}}}
		if got := agentName(req); got != want {
			t.Errorf("agentName(%q) = %q, want %q", ua, got, want)
		}
	}
	if agentName(&mcp.CallToolRequest{}) != "agent" {
		t.Fatal("no header gives agent")
	}
}
