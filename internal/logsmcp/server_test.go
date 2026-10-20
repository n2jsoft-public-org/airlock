package logsmcp

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft/airlock/internal/awsapi"
	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

var testNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// fakeAWS is a CloudWatch Logs API answering with reply and recording every request body.
type fakeAWS struct {
	reply    func(op string, body map[string]any) any
	requests map[string][]map[string]any
	mu       sync.Mutex
}

func (f *fakeAWS) calls(op string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[op]
}

// connect serves NewServer over in-memory transports against f and returns a client session, the
// operations and the events recorded.
func connect(t *testing.T, f *fakeAWS) (*mcp.ClientSession, *logs, *[]mcpserve.ToolEvent) {
	t.Helper()
	f.requests = map[string][]map[string]any{}
	awsapitest.Env(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		op := awsapitest.Operation(r)
		f.mu.Lock()
		f.requests[op] = append(f.requests[op], body)
		f.mu.Unlock()
		awsapitest.Respond(t, w, http.StatusOK, f.reply(op, body))
	}, "dev")
	awsCfg, err := awsapi.NewConfigs().Get(t.Context(), "dev", "eu-west-3")
	if err != nil {
		t.Fatal(err)
	}
	l := newLogs(cloudwatchlogs.NewFromConfig(awsCfg))
	l.now, l.poll = func() time.Time { return testNow }, time.Millisecond
	var events []mcpserve.ToolEvent
	server := NewServer(&ServerConfig{Name: "acme-dev-logs-eu-west-3", Profile: "dev", Region: "eu-west-3"}, l,
		func(e *mcpserve.ToolEvent) { events = append(events, *e) })
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, cerr := server.Connect(t.Context(), serverTransport, nil); cerr != nil {
		t.Fatal(cerr)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() }) //nolint:errcheck // in-memory session.
	return cs, l, &events
}

// call runs tool and decodes its result into out, failing on errors.
func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any, out any) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("%s = %+v, %v", tool, res, err)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s content = %#v", tool, res.Content[0])
	}
	if err := json.Unmarshal([]byte(text.Text), out); err != nil {
		t.Fatalf("%s: %v in %s", tool, err, text.Text)
	}
}

// callError runs tool and returns the text of its error result.
func callError(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil || !res.IsError {
		t.Fatalf("%s = %+v, %v; want an error result", tool, res, err)
	}
	text, _ := res.Content[0].(*mcp.TextContent) //nolint:errcheck // checked below.
	if text == nil {
		t.Fatalf("%s content = %#v", tool, res.Content[0])
	}
	return text.Text
}

func TestServerIsReadOnly(t *testing.T) {
	cs, _, _ := connect(t, &fakeAWS{reply: func(string, map[string]any) any { return map[string]any{} }})
	listed, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not read-only", tool.Name)
		}
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if want := []string{"filter_events", "list_log_groups", "list_log_streams", "query"}; !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
}

func TestPrototypeServerNeedsNoClient(t *testing.T) {
	if NewServer(&ServerConfig{Name: "prototype"}, nil, nil) == nil {
		t.Fatal("nil server")
	}
}

func TestListLogGroupsPassesThePrefixAndPages(t *testing.T) {
	f := &fakeAWS{reply: func(string, map[string]any) any {
		return map[string]any{"nextToken": "page-3", "logGroups": []map[string]any{{
			"logGroupName": "/aws/lambda/orders", "retentionInDays": 14, "storedBytes": 2048, "logGroupClass": "STANDARD", "creationTime": 1760000000000,
		}}}
	}}
	cs, _, events := connect(t, f)
	var out listGroupsOutput
	call(t, cs, "list_log_groups", map[string]any{"prefix": "/aws/lambda/", "start": "page-2"}, &out)
	req := f.calls("DescribeLogGroups")[0]
	if req["logGroupNamePrefix"] != "/aws/lambda/" || req["nextToken"] != "page-2" || req["limit"] != float64(listPage) {
		t.Fatalf("request = %v", req)
	}
	g := out.Groups[0]
	if out.Next != "page-3" || g.Name != "/aws/lambda/orders" || g.RetentionDays != 14 || g.StoredBytes != 2048 || g.Class != "STANDARD" ||
		g.Created != "2025-10-09T08:53:20Z" {
		t.Fatalf("out = %+v", out)
	}
	if e := (*events)[0]; e.Engine != Engine || e.Command != "list_log_groups /aws/lambda/" || *e.Rows != 1 || e.Access != "read-only" {
		t.Fatalf("event = %+v", e)
	}
}

func TestListLogGroupsRefusesPrefixWithPattern(t *testing.T) {
	cs, _, _ := connect(t, &fakeAWS{reply: func(string, map[string]any) any { return map[string]any{} }})
	if text := callError(t, cs, "list_log_groups", map[string]any{"prefix": "/aws/", "pattern": "orders"}); !strings.Contains(text, "not both") {
		t.Fatalf("error = %q", text)
	}
}

func TestListLogStreamsMostRecentFirst(t *testing.T) {
	f := &fakeAWS{reply: func(string, map[string]any) any {
		return map[string]any{"logStreams": []map[string]any{{"logStreamName": "2026/10/20/[$LATEST]abc", "lastEventTimestamp": 1760961600000}}}
	}}
	cs, _, _ := connect(t, f)
	var out listStreamsOutput
	call(t, cs, "list_log_streams", map[string]any{"group": "/aws/lambda/orders"}, &out)
	req := f.calls("DescribeLogStreams")[0]
	if req["logGroupIdentifier"] != "/aws/lambda/orders" || req["orderBy"] != "LastEventTime" || req["descending"] != true {
		t.Fatalf("request = %v", req)
	}
	if len(out.Streams) != 1 || out.Streams[0].LastEvent != "2025-10-20T12:00:00Z" {
		t.Fatalf("out = %+v", out)
	}
}

func TestQueryPollsUntilCompleteAndDropsThePointer(t *testing.T) {
	polls := 0
	f := &fakeAWS{reply: func(op string, _ map[string]any) any {
		switch op {
		case "StartQuery":
			return map[string]any{"queryId": "q-1"}
		case "GetQueryResults":
			polls++
			if polls < 3 {
				return map[string]any{"status": "Running"}
			}
			return map[string]any{
				"status":     "Complete",
				"statistics": map[string]any{"recordsMatched": 1, "recordsScanned": 500, "bytesScanned": 65536},
				"results": [][]map[string]any{{
					{"field": "@timestamp", "value": "2026-10-20 11:59:00.000"}, {"field": "@message", "value": "ERROR boom"}, {"field": "@ptr", "value": "xyz"},
				}},
			}
		}
		return map[string]any{}
	}}
	cs, _, events := connect(t, f)
	var out queryOutput
	call(t, cs, "query", map[string]any{"groups": []string{"/aws/lambda/orders", "/ecs/api"}, "query": "filter @message like /ERROR/"}, &out)
	req := f.calls("StartQuery")[0]
	groups, _ := req["logGroupIdentifiers"].([]any) //nolint:errcheck // nil fails below.
	if req["startTime"] != float64(testNow.Add(-15*time.Minute).Unix()) || req["endTime"] != float64(testNow.Unix()) || req["limit"] != float64(defaultLimit) ||
		len(groups) != 2 {
		t.Fatalf("request = %v", req)
	}
	if polls != 3 || len(out.Rows) != 1 || out.Rows[0]["@message"] != "ERROR boom" || out.Rows[0]["@ptr"] != "" ||
		out.Statistics.BytesScanned != 65536 || out.Statistics.RecordsScanned != 500 || out.Truncated {
		t.Fatalf("polls = %d, out = %+v", polls, out)
	}
	if c := (*events)[0].Command; c != "query /aws/lambda/orders,/ecs/api [15m, now] filter @message like /ERROR/" {
		t.Fatalf("command = %q", c)
	}
}

func TestQueryStopsPastItsTimeout(t *testing.T) {
	f := &fakeAWS{reply: func(op string, _ map[string]any) any {
		if op == "StartQuery" {
			return map[string]any{"queryId": "q-1"}
		}
		return map[string]any{"status": "Running"}
	}}
	cs, l, _ := connect(t, f)
	l.queryTimeout = 20 * time.Millisecond
	text := callError(t, cs, "query", map[string]any{"groups": []string{"/ecs/api"}, "query": "stats count(*)", "start": "1h"})
	if !strings.Contains(text, "stopped") {
		t.Fatalf("error = %q", text)
	}
	if stops := f.calls("StopQuery"); len(stops) != 1 || stops[0]["queryId"] != "q-1" {
		t.Fatalf("StopQuery = %v", stops)
	}
}

func TestQueryReportsAFailedQuery(t *testing.T) {
	f := &fakeAWS{reply: func(op string, _ map[string]any) any {
		if op == "StartQuery" {
			return map[string]any{"queryId": "q-1"}
		}
		return map[string]any{"status": "Failed"}
	}}
	cs, _, _ := connect(t, f)
	if text := callError(t, cs, "query", map[string]any{"groups": []string{"/ecs/api"}, "query": "stats count(*)"}); !strings.Contains(text, "Failed") {
		t.Fatalf("error = %q", text)
	}
}

func TestQueryCapsRowsAndValues(t *testing.T) {
	long := strings.Repeat("x", maxValueBytes+10)
	f := &fakeAWS{reply: func(op string, _ map[string]any) any {
		if op == "StartQuery" {
			return map[string]any{"queryId": "q-1"}
		}
		row := []map[string]any{{"field": "@message", "value": long}}
		return map[string]any{"status": "Complete", "results": [][]map[string]any{row, row, row}}
	}}
	cs, _, _ := connect(t, f)
	var out queryOutput
	call(t, cs, "query", map[string]any{"groups": []string{"/ecs/api"}, "query": "fields @message", "limit": 2}, &out)
	if len(out.Rows) != 2 || !out.Truncated || !strings.HasSuffix(out.Rows[0]["@message"], truncatedSuffix) {
		t.Fatalf("rows = %d, truncated = %v", len(out.Rows), out.Truncated)
	}
}

func TestQueryRefusesNoGroup(t *testing.T) {
	cs, _, _ := connect(t, &fakeAWS{reply: func(string, map[string]any) any { return map[string]any{} }})
	if text := callError(t, cs, "query", map[string]any{"groups": []string{}, "query": "fields @message"}); !strings.Contains(text, "log groups") {
		t.Fatalf("error = %q", text)
	}
}

func TestFilterEventsKeepsTheNewestAcrossPages(t *testing.T) {
	event := func(ms int64, message string) map[string]any {
		return map[string]any{"logStreamName": "web-1", "timestamp": ms, "message": message}
	}
	f := &fakeAWS{reply: func(_ string, body map[string]any) any {
		if body["nextToken"] == nil {
			return map[string]any{"nextToken": "p2", "events": []map[string]any{event(1, "first"), event(2, "second")}}
		}
		return map[string]any{"events": []map[string]any{event(3, "third"), event(4, strings.Repeat("y", maxValueBytes+1))}}
	}}
	cs, _, events := connect(t, f)
	var out filterOutput
	call(t, cs, "filter_events", map[string]any{"group": "/ecs/api", "pattern": "ERROR", "streams": []string{"web-1"}, "start": "1h", "limit": 2}, &out)
	req := f.calls("FilterLogEvents")[0]
	if req["filterPattern"] != "ERROR" || req["startTime"] != float64(testNow.Add(-time.Hour).UnixMilli()) || len(f.calls("FilterLogEvents")) != 2 {
		t.Fatalf("requests = %v", f.calls("FilterLogEvents"))
	}
	if len(out.Events) != 2 || out.Events[0].Message != "third" || !strings.HasSuffix(out.Events[1].Message, truncatedSuffix) || !out.Truncated ||
		out.Events[0].Time != "1970-01-01T00:00:00.003Z" {
		t.Fatalf("out = %+v", out)
	}
	if c := (*events)[0].Command; c != "filter_events /ecs/api [1h, now] ERROR" {
		t.Fatalf("command = %q", c)
	}
}

func TestStartServesAndNamesTheServerAfterProfileAndRegion(t *testing.T) {
	awsapitest.Env(t, func(http.ResponseWriter, *http.Request) {}, "dev")
	awsCfg, err := awsapi.NewConfigs().Get(t.Context(), "dev", "eu-west-3")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := Start(mcpserve.NewHost(), &SessionConfig{
		AWS: awsCfg, ProjectDir: t.TempDir(), Profile: "dev", Region: "eu-west-3", ServerPrefix: "acme", SkipRegistration: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if !strings.HasPrefix(handle.Display(), "read-only · acme-dev-logs-eu-west-3 -> http://127.0.0.1:") {
		t.Fatalf("display = %q", handle.Display())
	}
}
