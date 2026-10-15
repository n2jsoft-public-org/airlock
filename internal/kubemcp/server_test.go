package kubemcp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

func connect(t *testing.T, c cluster) (*mcp.ClientSession, *[]mcpserve.ToolEvent) {
	t.Helper()
	var events []mcpserve.ToolEvent
	server := NewServer(&ServerConfig{Name: "acme-dev-platform", Target: "platform", Profile: "dev", Region: "eu-west-3"}, c,
		func(e *mcpserve.ToolEvent) { events = append(events, *e) })
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

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func resultText(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestServerOffersSixReadOnlyTools(t *testing.T) {
	session, _ := connect(t, testAPICluster(t, newFakeAPI()))
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("tool %s is not annotated read-only", tool.Name)
		}
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	if got := strings.Join(names, ","); got != "get_resource,list_events,list_namespaces,list_resource_types,list_resources,pod_logs" {
		t.Fatalf("tools = %s", got)
	}
}

func TestServerListResourcesRecordsTheCall(t *testing.T) {
	api := newFakeAPI()
	api.route("/api/v1/namespaces/web/pods", `{"kind":"PodList","items":[{"metadata":{"name":"api-0","namespace":"web"}},{"metadata":{"name":"api-1","namespace":"web"}}]}`)
	session, events := connect(t, testAPICluster(t, api))

	result := callTool(t, session, "list_resources", map[string]any{"resource": "pods", "namespace": "web"})
	if result.IsError || !strings.Contains(resultText(result), "api-1") {
		t.Fatalf("result = %s", resultText(result))
	}
	if len(*events) != 1 {
		t.Fatalf("events = %+v", *events)
	}
	e := (*events)[0]
	if e.Tool != "list_resources" || e.Namespace != "web" || e.Resource != "pods" || e.Rows == nil || *e.Rows != 2 ||
		e.Engine != "kubernetes" || e.Access != "read-only" || e.Server != "acme-dev-platform" {
		t.Fatalf("event = %+v", e)
	}
}

func TestServerGetResourceReturnsTheRedactedObject(t *testing.T) {
	api := newFakeAPI()
	api.route("/api/v1/namespaces/web/secrets/db", secretJSON)
	session, events := connect(t, testAPICluster(t, api))

	result := callTool(t, session, "get_resource", map[string]any{"resource": "secrets", "namespace": "web", "name": "db"})
	var obj map[string]any
	if err := json.Unmarshal([]byte(resultText(result)), &obj); err != nil || result.IsError {
		t.Fatalf("result = %s (%v)", resultText(result), err)
	}
	if strings.Contains(resultText(result), "czNjcmV0") || (*events)[0].Name != "db" {
		t.Fatalf("secret leaked or event wrong: %s %+v", resultText(result), (*events)[0])
	}
}

func TestServerReportsErrorsAndRecordsThem(t *testing.T) {
	session, events := connect(t, testAPICluster(t, newFakeAPI()))
	result := callTool(t, session, "get_resource", map[string]any{"resource": "gizmos", "name": "x"})
	if !result.IsError || !strings.Contains(resultText(result), "unknown resource type") {
		t.Fatalf("result = %s", resultText(result))
	}
	if len(*events) != 1 || (*events)[0].Error == "" || (*events)[0].Rows != nil {
		t.Fatalf("events = %+v", *events)
	}
}

func TestServerPodLogsAndEvents(t *testing.T) {
	api := newFakeAPI()
	api.route("/api/v1/namespaces/web/pods/api-0/log", "started\n")
	api.route("/api/v1/namespaces/web/events", `{"items":[{"reason":"Started","lastTimestamp":"2026-10-01T10:00:00Z","involvedObject":{"kind":"Pod","name":"api-0"}}]}`)
	session, events := connect(t, testAPICluster(t, api))

	if result := callTool(t, session, "pod_logs", map[string]any{"namespace": "web", "pod": "api-0"}); !strings.Contains(resultText(result), "started") {
		t.Fatalf("logs = %s", resultText(result))
	}
	if result := callTool(t, session, "list_events", map[string]any{"namespace": "web"}); !strings.Contains(resultText(result), "Started") {
		t.Fatalf("events = %s", resultText(result))
	}
	if (*events)[0].Tool != "pod_logs" || (*events)[0].Resource != "pods" || (*events)[0].Name != "api-0" || (*events)[1].Resource != "events" {
		t.Fatalf("events = %+v", *events)
	}
}

func TestServerListEventsNotesTheEventsNotFetched(t *testing.T) {
	api := newFakeAPI()
	page := `{"metadata":{"continue":"again"},"items":[{"reason":"Loop","lastTimestamp":"2026-10-01T10:00:00Z","involvedObject":{"kind":"Pod","name":"api-0"}}]}`
	api.route("/api/v1/events", page)
	api.routeContinue("/api/v1/events", "again", page)
	session, _ := connect(t, testAPICluster(t, api))
	result := callTool(t, session, "list_events", map[string]any{})
	if text := resultText(result); !strings.Contains(text, `"note":"more than 5000 events: newest first among the first 5000 returned by the API; narrow with namespace, kind or name"`) {
		t.Fatalf("result = %s", text)
	}
	api = newFakeAPI()
	api.route("/api/v1/events", `{"items":[]}`)
	session, _ = connect(t, testAPICluster(t, api))
	if text := resultText(callTool(t, session, "list_events", map[string]any{})); strings.Contains(text, "note") {
		t.Fatalf("result = %s, want no note when every event was fetched", text)
	}
}

// An integer over 2^53 in an object must reach the client exactly, not rounded through a float64.
func TestServerGetResourceKeepsBigIntegers(t *testing.T) {
	api := newFakeAPI()
	api.route("/api/v1/namespaces/web/pods/ids", `{"kind":"Pod","metadata":{"name":"ids","generation":1234567890123456789}}`)
	session, _ := connect(t, testAPICluster(t, api))

	result := callTool(t, session, "get_resource", map[string]any{"resource": "pods", "namespace": "web", "name": "ids"})
	if text := resultText(result); result.IsError || !strings.Contains(text, `"generation":1234567890123456789`) {
		t.Fatalf("result = %s", text)
	}
}
