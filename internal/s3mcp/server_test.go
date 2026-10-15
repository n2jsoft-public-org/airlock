package s3mcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// connect serves NewServer over in-memory transports and returns a client session and the events recorded.
func connect(t *testing.T, f *fakeAPI, allowWrites bool) (*mcp.ClientSession, *[]mcpserve.ToolEvent) {
	t.Helper()
	var events []mcpserve.ToolEvent
	server := NewServer(&ServerConfig{Name: "acme-dev-s3", Profile: "dev", Region: "eu-west-3", AllowWrites: allowWrites},
		newBuckets(f), func(e *mcpserve.ToolEvent) { events = append(events, *e) })
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

func TestReadOnlyServerHasNoWriteTool(t *testing.T) {
	cs, _ := connect(t, &fakeAPI{}, false)
	want := []string{"get_object", "head_object", "list_buckets", "list_objects"}
	if got := toolNames(t, cs); !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestWritableServerAddsTheWriteTools(t *testing.T) {
	cs, _ := connect(t, &fakeAPI{}, true)
	want := []string{"delete_object", "get_object", "head_object", "list_buckets", "list_objects", "put_object"}
	if got := toolNames(t, cs); !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestPrototypeServerNeedsNoClient(t *testing.T) {
	if NewServer(&ServerConfig{Name: "prototype", AllowWrites: true}, nil, nil) == nil {
		t.Fatal("nil server")
	}
}

func TestListObjectsEventNamesTheBucketPrefixAndCount(t *testing.T) {
	f := &fakeAPI{bucketRegion: "eu-west-3", prefixes: []string{"logs/2026/"}}
	cs, events := connect(t, f, false)
	if res := call(t, cs, "list_objects", map[string]any{"bucket": "b", "prefix": "logs/", "recursive": true}); res.IsError {
		t.Fatalf("res = %+v", res)
	}
	e := (*events)[0]
	if e.Engine != Engine || e.Target != Target || e.Tool != "list_objects" || e.Command != "list_objects b logs/ recursive" || e.Rows == nil || *e.Rows != 1 {
		t.Fatalf("event = %+v", e)
	}
}

func TestGetObjectEventRecordsTheOffsetAndTheCut(t *testing.T) {
	body := strings.Repeat("a", maxTextBytes+10)
	f := &fakeAPI{bucketRegion: "eu-west-3", head: headOf(int64(len(body)), "text/plain"), body: []byte(body)}
	cs, events := connect(t, f, false)
	if res := call(t, cs, "get_object", map[string]any{"bucket": "b", "key": "k.txt", "offset": 4}); res.IsError {
		t.Fatalf("res = %+v", res)
	}
	if e := (*events)[0]; e.Command != "get_object b/k.txt @4" || !e.Truncated {
		t.Fatalf("event = %+v", e)
	}
}

func TestPutObjectSendsTheConditionsAndNeverLogsTheContent(t *testing.T) {
	f := &fakeAPI{bucketRegion: "eu-west-3"}
	cs, events := connect(t, f, true)
	res := call(t, cs, "put_object", map[string]any{"bucket": "b", "key": "k.txt", "content": "secret-person@example.com", "if_none_match": "*"})
	if res.IsError {
		t.Fatalf("res = %+v", res)
	}
	in := f.putIn
	if f.putBody != "secret-person@example.com" || aws.ToString(in.IfNoneMatch) != "*" || in.IfMatch != nil ||
		aws.ToString(in.ContentType) != "text/plain; charset=utf-8" || aws.ToInt64(in.ContentLength) != 25 {
		t.Fatalf("put = %+v, body = %q", in, f.putBody)
	}
	raw, _ := json.Marshal(*events) //nolint:errcheck // plain structs.
	if (*events)[0].Command != "put_object b/k.txt" || strings.Contains(string(raw), "secret-person") {
		t.Fatalf("events = %s", raw)
	}
}

func TestDeleteObjectReturnsTheDeleteMarker(t *testing.T) {
	f := &fakeAPI{bucketRegion: "eu-west-3"}
	cs, _ := connect(t, f, true)
	res := call(t, cs, "delete_object", map[string]any{"bucket": "b", "key": "k.txt", "version_id": "v1"})
	text, ok := res.Content[0].(*mcp.TextContent)
	if res.IsError || !ok || text.Text != `{"version_id":"v3","delete_marker":true}` || aws.ToString(f.deleteIn.VersionId) != "v1" {
		t.Fatalf("res = %+v, delete = %+v", res, f.deleteIn)
	}
}

func TestVersionedCallsRecordTheVersion(t *testing.T) {
	f := &fakeAPI{bucketRegion: "eu-west-3", head: headOf(3, "text/plain"), body: []byte("abc")}
	cs, events := connect(t, f, true)
	call(t, cs, "delete_object", map[string]any{"bucket": "b", "key": "k.txt", "version_id": "v1"})
	call(t, cs, "get_object", map[string]any{"bucket": "b", "key": "k.txt", "version_id": "v1", "offset": 4})
	if got := []string{(*events)[0].Command, (*events)[1].Command}; got[0] != "delete_object b/k.txt v=v1" || got[1] != "get_object b/k.txt v=v1 @4" {
		t.Fatalf("commands = %v", got)
	}
}

func TestToolErrorIsReturnedAndRecorded(t *testing.T) {
	f := &fakeAPI{err: context.DeadlineExceeded}
	cs, events := connect(t, f, false)
	res := call(t, cs, "list_buckets", map[string]any{})
	if !res.IsError || (*events)[0].Error == "" {
		t.Fatalf("res = %+v, event = %+v", res, (*events)[0])
	}
}

func TestStartServesAndNamesTheServerAfterTheProfile(t *testing.T) {
	handle, err := Start(mcpserve.NewHost(), &SessionConfig{
		AWS: func(context.Context, ...string) (string, error) { return "", nil }, ProjectDir: t.TempDir(),
		Profile: "dev", Region: "eu-west-3", ServerPrefix: "acme", SkipRegistration: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if !strings.HasPrefix(handle.Display(), "read-only · acme-dev-s3 -> http://127.0.0.1:") {
		t.Fatalf("display = %q", handle.Display())
	}
}
