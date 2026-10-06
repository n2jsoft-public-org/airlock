package mcpserve

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func testBaseEvent() ToolEvent {
	return ToolEvent{Server: "acme-dev-app", Profile: "dev", Region: "eu-west-3", Target: "app", Engine: "postgres", Access: "read-only"}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test file.
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestToolEventSQLJSONIsUnchanged(t *testing.T) {
	e := testBaseEvent()
	e.Time = time.Date(2026, 9, 29, 14, 5, 0, 0, time.UTC)
	e.Tool, e.Database, e.Schema, e.SQL, e.MaxRows, e.Rows, e.Truncated = "query", "archive", "public", "SELECT 1", 200, new(int64(3)), true

	raw, err := json.Marshal(&e)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"time":"2026-09-29T14:05:00Z","rows":3,"server":"acme-dev-app","profile":"dev","region":"eu-west-3",` +
		`"target":"app","engine":"postgres","access":"read-only","tool":"query","database":"archive","schema":"public",` +
		`"sql":"SELECT 1","max_rows":200,"truncated":true}`
	if string(raw) != want {
		t.Fatalf("json =\n%s\nwant\n%s", raw, want)
	}
}

func TestToolEventNewKindsJSON(t *testing.T) {
	e := ToolEvent{Tool: "command", DB: new(0), Command: "GET user:1", Engine: "valkey"}
	raw, err := json.Marshal(&e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"db":0`, `"command":"GET user:1"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("json %s lacks %s", raw, want)
		}
	}
	k := ToolEvent{Tool: "get_resource", Namespace: "web", Resource: "pods", Name: "api-0"}
	raw, err = json.Marshal(&k)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"namespace":"web"`, `"resource":"pods"`, `"name":"api-0"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("json %s lacks %s", raw, want)
		}
	}
}

func TestToolEventDetail(t *testing.T) {
	cases := []struct {
		e    *ToolEvent
		want string
	}{
		{&ToolEvent{Tool: "query", SQL: "SELECT 1"}, "SELECT 1"},
		{&ToolEvent{Tool: "command", Command: "HGETALL user:1"}, "HGETALL user:1"},
		{&ToolEvent{Tool: "get_resource", Resource: "pods", Name: "api-0", Namespace: "web"}, "get pods/api-0 -n web"},
		{&ToolEvent{Tool: "get_resource", Resource: "nodes", Name: "ip-10-0-1-2"}, "get nodes/ip-10-0-1-2"},
		{&ToolEvent{Tool: "list_resources", Resource: "deployments", Namespace: "web"}, "list deployments -n web"},
		{&ToolEvent{Tool: "pod_logs", Resource: "pods", Name: "api-0", Namespace: "web"}, "logs pods/api-0 -n web"},
		{&ToolEvent{Tool: "list_databases"}, ""},
	}
	for _, c := range cases {
		if got := c.e.Detail(); got != c.want {
			t.Errorf("%s: Detail() = %q, want %q", c.e.Tool, got, c.want)
		}
	}
}

func TestStartEventRecordsCountOrError(t *testing.T) {
	var got []ToolEvent
	record := func(e *ToolEvent) { got = append(got, *e) }
	base := testBaseEvent()

	e, done := StartEvent(&base, "list_tables", record)
	e.Database = "archive"
	done(4, nil)
	_, done = StartEvent(&base, "query", record)
	done(0, errors.New("boom"))

	if len(got) != 2 || got[0].Tool != "list_tables" || got[0].Database != "archive" || got[0].Rows == nil || *got[0].Rows != 4 {
		t.Fatalf("first event = %+v", got[0])
	}
	if got[1].Rows != nil || got[1].Error != "boom" || got[1].Time.IsZero() {
		t.Fatalf("second event = %+v", got[1])
	}
	if base.Tool != "" {
		t.Fatal("StartEvent must not modify base")
	}
}
