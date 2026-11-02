package sqsmcp

import (
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// connect serves NewServer over in-memory transports and returns a client session and the events recorded.
func connect(t *testing.T, f *fakeSQS, allowWrites bool) (*mcp.ClientSession, *[]mcpserve.ToolEvent) {
	t.Helper()
	var events []mcpserve.ToolEvent
	server := NewServer(&ServerConfig{Name: "acme-dev-sqs", Profile: "dev", Region: "eu-west-3", AllowWrites: allowWrites},
		newQueues(f), nil, func(e *mcpserve.ToolEvent) { events = append(events, *e) })
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(t.Context(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() }) //nolint:errcheck // in-memory session.
	return cs, &events
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	listed, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestReadOnlyServerHasNoMessageTool(t *testing.T) {
	cs, _ := connect(t, &fakeSQS{}, false)
	got := toolNames(t, cs)
	for _, want := range []string{"list_queues", "get_queue"} {
		if !slices.Contains(got, want) {
			t.Fatalf("tools = %v, missing %s", got, want)
		}
	}
	for _, none := range []string{"peek_messages", "repost_message"} {
		if slices.Contains(got, none) {
			t.Fatalf("tools = %v, unexpected %s", got, none)
		}
	}
}

func TestPrototypeServerNeedsNoServices(t *testing.T) {
	if NewServer(&ServerConfig{Name: "prototype", AllowWrites: true}, nil, nil, nil) == nil {
		t.Fatal("nil server")
	}
}

func TestListQueuesRecordsAnEvent(t *testing.T) {
	f := &fakeSQS{urls: []string{"https://sqs.eu-west-3.amazonaws.com/1/orders", "https://sqs.eu-west-3.amazonaws.com/1/orders.fifo"}}
	cs, events := connect(t, f, false)
	if res := call(t, cs, "list_queues", map[string]any{"prefix": "ord"}); res.IsError {
		t.Fatalf("res = %+v", res)
	}
	if len(*events) != 1 {
		t.Fatalf("events = %+v", *events)
	}
	e := (*events)[0]
	if e.Tool != "list_queues" || e.Engine != "sqs" || e.Access != "read-only" || e.Command != "list_queues ord" || e.Rows == nil || *e.Rows != 2 {
		t.Fatalf("event = %+v", e)
	}
}
