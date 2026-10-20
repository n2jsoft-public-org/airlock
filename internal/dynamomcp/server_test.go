package dynamomcp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft/airlock/internal/awsapi"
	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// connect serves NewServer over in-memory transports and returns a client session and the events recorded.
func connect(t *testing.T, f *fakeAPI, allowWrites bool) (*mcp.ClientSession, *[]mcpserve.ToolEvent) {
	t.Helper()
	var events []mcpserve.ToolEvent
	server := NewServer(&ServerConfig{Name: "acme-dev-dynamodb-eu-west-3", Profile: "dev", Region: "eu-west-3", AllowWrites: allowWrites},
		newTables(f), func(e *mcpserve.ToolEvent) { events = append(events, *e) })
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

func TestReadOnlyServerHasNoWriteTool(t *testing.T) {
	cs, _ := connect(t, &fakeAPI{}, false)
	want := []string{"describe_table", "get_item", "list_tables", "query", "scan"}
	if got := toolNames(t, cs); !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestWritableServerAddsTheWriteTools(t *testing.T) {
	cs, _ := connect(t, &fakeAPI{}, true)
	want := []string{"delete_item", "describe_table", "get_item", "list_tables", "put_item", "query", "scan", "update_item"}
	if got := toolNames(t, cs); !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestPrototypeServerNeedsNoClient(t *testing.T) {
	if NewServer(&ServerConfig{Name: "prototype", AllowWrites: true}, nil, nil) == nil {
		t.Fatal("nil server")
	}
}

func TestLargeIntegerFromTheClientStaysExact(t *testing.T) {
	f := &fakeAPI{}
	cs, _ := connect(t, f, false)
	_, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_item", Arguments: json.RawMessage(
		`{"table":"orders","key":{"pk":12345678901234567890,"sk":"a"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := f.getIn.Key["pk"].(*types.AttributeValueMemberN); !ok || n.Value != "12345678901234567890" {
		t.Fatalf("pk = %#v", f.getIn.Key["pk"])
	}
}

func TestQueryEventLogsTheExpressionButNeverTheValues(t *testing.T) {
	f := &fakeAPI{items: []map[string]types.AttributeValue{order(0, 10)}}
	cs, events := connect(t, f, false)
	_, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "query", Arguments: map[string]any{
		"table": "orders", "index": "by-email", "key_condition": "email = :e", "values": map[string]any{":e": "secret-person@example.com"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(*events) != 1 {
		t.Fatalf("events = %+v", *events)
	}
	e := (*events)[0]
	raw, _ := json.Marshal(e) //nolint:errcheck // plain struct.
	if e.Engine != Engine || e.Table != "orders" || e.Command != "query orders [by-email] email = :e" || e.Rows == nil || *e.Rows != 1 ||
		strings.Contains(string(raw), "secret-person") {
		t.Fatalf("event = %s", raw)
	}
}

func TestUpdateItemSendsExpressionsAndReturnsTheNewItem(t *testing.T) {
	f := &fakeAPI{item: order(0, 10)}
	cs, events := connect(t, f, true)
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "update_item", Arguments: map[string]any{
		"table": "orders", "key": map[string]any{"pk": "customer#1", "sk": "order#a"},
		"update": "SET #s = :s", "condition": "attribute_exists(pk)", "names": map[string]any{"#s": "status"}, "values": map[string]any{":s": "shipped"},
	}})
	if err != nil || res.IsError {
		t.Fatalf("res = %+v, %v", res, err)
	}
	in := f.updateIn
	if *in.UpdateExpression != "SET #s = :s" || *in.ConditionExpression != "attribute_exists(pk)" || in.ExpressionAttributeNames["#s"] != "status" ||
		in.ReturnValues != types.ReturnValueAllNew {
		t.Fatalf("update = %+v", in)
	}
	if (*events)[0].Command != "update_item orders SET #s = :s if attribute_exists(pk)" {
		t.Fatalf("command = %q", (*events)[0].Command)
	}
}

func TestToolErrorIsReturnedAndRecorded(t *testing.T) {
	f := &fakeAPI{err: context.DeadlineExceeded}
	cs, events := connect(t, f, false)
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_tables", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || (*events)[0].Error == "" {
		t.Fatalf("res = %+v, event = %+v", res, (*events)[0])
	}
}

func TestStartServesAndNamesTheServerAfterProfileAndRegion(t *testing.T) {
	awsapitest.Env(t, func(http.ResponseWriter, *http.Request) {}, "dev")
	awsCfg, err := awsapi.NewConfigs().Get(t.Context(), "dev", "eu-west-3")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := Start(mcpserve.NewHost(), &SessionConfig{
		AWS: awsCfg, ProjectDir: t.TempDir(),
		Profile: "dev", Region: "eu-west-3", ServerPrefix: "acme", SkipRegistration: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if !strings.HasPrefix(handle.Display(), "read-only · acme-dev-dynamodb-eu-west-3 -> http://127.0.0.1:") {
		t.Fatalf("display = %q", handle.Display())
	}
}

const bigN = "12345678901234567890"

// resultText is the text of a tool result, failing on errors.
func resultText(t *testing.T, cs *mcp.ClientSession, tool string, args any) string {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("%s = %+v, %v", tool, res, err)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s content = %#v", tool, res.Content[0])
	}
	return text.Text
}

func TestLargeIntegerOfAnItemStaysExact(t *testing.T) {
	f := &fakeAPI{item: map[string]types.AttributeValue{"pk": &types.AttributeValueMemberN{Value: bigN}, "sk": &types.AttributeValueMemberS{Value: "a"}}}
	cs, _ := connect(t, f, false)
	text := resultText(t, cs, "get_item", map[string]any{"table": "orders", "key": map[string]any{"pk": 1, "sk": "a"}})
	if !strings.Contains(text, `"pk":`+bigN) {
		t.Fatalf("text = %s", text)
	}
}

func TestLargeIntegerOfANextKeyStaysExact(t *testing.T) {
	f := &fakeAPI{items: []map[string]types.AttributeValue{order(0, 10)}, lastKey: map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberN{Value: bigN}, "sk": &types.AttributeValueMemberS{Value: "order#a"},
	}}
	cs, _ := connect(t, f, false)
	text := resultText(t, cs, "query", map[string]any{"table": "orders", "key_condition": "pk = :pk", "values": map[string]any{":pk": 1}})
	if !strings.Contains(text, `"next_key":{"pk":`+bigN) {
		t.Fatalf("text = %s", text)
	}
}

func TestLargeIntegerOfAWrittenItemStaysExact(t *testing.T) {
	f := &fakeAPI{}
	cs, _ := connect(t, f, true)
	resultText(t, cs, "put_item", json.RawMessage(`{"table":"orders","item":{"pk":`+bigN+`,"sk":"a"}}`))
	if n, ok := f.putIn.Item["pk"].(*types.AttributeValueMemberN); !ok || n.Value != bigN {
		t.Fatalf("pk = %#v", f.putIn.Item["pk"])
	}
}
