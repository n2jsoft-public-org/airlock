package sessions

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

func TestApplyKeepsLogAndQueriesAcrossUpdates(t *testing.T) {
	list := Apply(nil, &Event{Kind: EventAdded, ID: 1, Session: &Session{ID: 1, Target: "db", MCP: &MCP{}}})
	list = Apply(list, &Event{Kind: EventLines, ID: 1, Lines: []LogLine{{Text: "a"}, {Text: "b"}}})
	list = Apply(list, &Event{Kind: EventMCPQuery, ID: 1, Query: &mcpserve.ToolEvent{Tool: "query"}})
	list = Apply(list, &Event{Kind: EventUpdated, ID: 1, Session: &Session{ID: 1, Target: "db", State: StateRunning, MCP: &MCP{Total: 1}}})

	s := list[0]
	if s.State != StateRunning || len(s.Lines) != 2 || len(s.MCP.Events) != 1 || s.MCP.Total != 1 {
		t.Fatalf("session = %+v", s)
	}
	list = Apply(list, &Event{Kind: EventReplaced, ID: 1, Session: &Session{ID: 1, Lines: []LogLine{{Text: "restart"}}}})
	if len(list[0].Lines) != 1 || list[0].Lines[0].Text != "restart" {
		t.Fatalf("replaced = %+v", list[0])
	}
	if list = Apply(list, &Event{Kind: EventRemoved, ID: 1}); len(list) != 0 {
		t.Fatalf("removed = %+v", list)
	}
}

func TestApplyCapsTheLog(t *testing.T) {
	list := Apply(nil, &Event{Kind: EventAdded, ID: 1, Session: &Session{ID: 1}})
	lines := make([]LogLine, MaxLogLines+10)
	lines[len(lines)-1].Text = "last"
	list = Apply(list, &Event{Kind: EventLines, ID: 1, Lines: lines})
	if len(list[0].Lines) != MaxLogLines || list[0].Lines[MaxLogLines-1].Text != "last" {
		t.Fatalf("lines = %d", len(list[0].Lines))
	}
}

func TestCloneDoesNotShareSlices(t *testing.T) {
	s := Session{Lines: []LogLine{{Text: "a"}}, SignIn: SignIn{Failed: map[string]string{"k": "e"}}}
	c := s.Clone()
	c.Lines[0].Text, c.SignIn.Failed["k"] = "changed", "changed"
	if s.Lines[0].Text != "a" || s.SignIn.Failed["k"] != "e" {
		t.Fatal("Clone shares memory with the original")
	}
}

func TestSessionAndEventSurviveJSON(t *testing.T) {
	at := time.Date(2026, 10, 2, 9, 30, 0, 0, time.UTC)
	rows := int64(3)
	candidate := jumphost.SignInCandidate{Label: "secret", User: "app", SecretARN: "arn:secret", Discovered: true}
	session := Session{
		StartedAt: at, EndedAt: at.Add(time.Hour), ReconnectAt: at.Add(time.Minute),
		Lines: []LogLine{{At: at, Text: "hello"}},
		SignIn: SignIn{
			State: SignInChoosing, User: "app", Err: "boom", Candidates: []jumphost.SignInCandidate{candidate},
			Failed: map[string]string{CandidateKey(&candidate): "rejected"},
		},
		MCP: &MCP{
			LogDir: "/tmp/logs", Total: 1, Writes: true,
			Events: []mcpserve.ToolEvent{{Time: at, Rows: &rows, Server: "s", Tool: "query", SQL: "select 1"}},
		},
		Options: jumphost.Options{
			ResourceType: "rds", Name: "db", DBUser: "app", ProjectDir: "/work", LocalPortNumber: 5432,
		},
		Details:      jumphost.SessionDetails{ConnectedServer: "db:5432", Auth: "AWS IAM"},
		Label:        "db",
		ResourceType: "rds", Target: "db", Profile: "dev", Region: "eu-west-1", Err: "ended", Cleanup: "done",
		ID: 7, LocalPort: 5432, Connections: 2, Reconnects: 1, State: StateRunning,
		Environment: "staging", Production: true, Ready: true, StopRequested: true, Reconnecting: true, HasSignIn: true, HasCredentials: true,
	}
	event := Event{
		Kind: EventUpdated, Seq: 42, ID: 7, Session: &session, Lines: []LogLine{{At: at, Text: "line"}},
		Query: &mcpserve.ToolEvent{Time: at, Rows: &rows, Tool: "query"},
		Name:  "db", User: "app", Err: "e", Notice: "n",
	}

	var gotSession Session
	var gotEvent Event
	for _, tc := range []struct{ in, out any }{{&session, &gotSession}, {&event, &gotEvent}} {
		data, err := json.Marshal(tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, tc.out); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(session, gotSession) {
		t.Fatalf("session\n got %+v\nwant %+v", gotSession, session)
	}
	if !reflect.DeepEqual(event, gotEvent) {
		t.Fatalf("event\n got %+v\nwant %+v", gotEvent, event)
	}
}
