package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/n2jsoft/airlock/internal/dbmcp"
	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/sessions/sessionstest"
)

type fakeDiscovery struct {
	searchErr error
	failures  map[string]error
	passwords map[string]string
	saved     []jumphost.SavedConnection
	found     []jumphost.ResourceSummary
	searched  []jumphost.Options
}

func (d *fakeDiscovery) ListAWSProfileSummaries(context.Context) ([]jumphost.AWSProfileSummary, error) {
	return []jumphost.AWSProfileSummary{{Name: "dev"}, {Name: "prod"}}, nil
}
func (d *fakeDiscovery) ListSavedConnections(_ context.Context, profile, region string) ([]jumphost.SavedConnection, error) {
	var kept []jumphost.SavedConnection
	for idx := range d.saved {
		conn := &d.saved[idx]
		if conn.Ref.Type != sessions.DirectResourceType && ((profile != "" && conn.Prefs.Profile != profile) || (region != "" && conn.Ref.Region != region)) {
			continue
		}
		kept = append(kept, *conn)
	}
	return kept, nil
}
func (d *fakeDiscovery) SearchResources(_ context.Context, options *jumphost.Options) ([]jumphost.ResourceSummary, map[string]error, error) {
	d.searched = append(d.searched, *options)
	// Like the runner, the search reports the region it resolved for the profile.
	if options.AwsRegion == "" {
		options.AwsRegion = "eu-west-3"
	}
	return d.found, d.failures, d.searchErr
}
func (d *fakeDiscovery) LoadKeychainPassword(_ context.Context, options jumphost.Options) (string, bool) { //nolint:gocritic // signature fixed by Discovery.
	password, ok := d.passwords[options.Name+"|"+options.DBUser]
	return password, ok
}
func (d *fakeDiscovery) CheckLocalPort(int) error { return nil }

type fakeDatabases map[string]directdb.Database

func (f fakeDatabases) Get(name string) (directdb.Database, bool, error) {
	db, ok := f[name]
	return db, ok, nil
}

//nolint:unparam // the open_session tests use one name.
func savedRDS(name, profile string) jumphost.SavedConnection {
	return jumphost.SavedConnection{Ref: jumphost.TargetRef{Account: "1", Region: "eu-west-3", Type: "rds", Name: name},
		Prefs: jumphost.ConnectionPrefs{Profile: profile, ResourceType: "rds"}}
}

// newToolset serves the gateway tools over m; events collects the audit events.
func newToolset(t *testing.T, m *sessions.Manager, settings *Settings, d *fakeDiscovery, dbs fakeDatabases) (*mcp.ClientSession, *[]mcpserve.ToolEvent) {
	t.Helper()
	return newToolsetWith(t, m, settings, d, dbs, nil)
}

// newToolsetWith is newToolset with the Docker containers of containers (nil: none).
func newToolsetWith(t *testing.T, m *sessions.Manager, settings *Settings, d *fakeDiscovery, dbs fakeDatabases, containers jumphost.DockerContainers) (*mcp.ClientSession, *[]mcpserve.ToolEvent) {
	t.Helper()
	var events []mcpserve.ToolEvent
	r := newRelay(t.Context(), m, time.Now, func() Settings { return *settings })
	t.Cleanup(r.closeAll)
	tools := &toolset{
		manager: m, relay: r, settings: func() Settings { return *settings },
		discovery: func() (Discovery, error) { return d, nil }, databases: dbs, containers: containers,
		record: func(e *mcpserve.ToolEvent) { events = append(events, *e) }, now: time.Now, name: "airlock",
		startTimeout: 5 * time.Second,
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "airlock", Version: serverVersion}, nil)
	tools.add(server)
	return connect(t, server), &events
}

func decodeText[T any](t *testing.T, text string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("%v: %s", err, text)
	}
	return v
}

type targetsReply struct {
	Failures map[string]string `json:"failures"`
	Targets  []struct {
		Type        string `json:"type"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Profile     string `json:"profile"`
		Region      string `json:"region"`
		Opens       string `json:"opens"`
		Production  bool   `json:"production"`
		Saved       bool   `json:"saved"`
	} `json:"targets"`
	Profiles []string `json:"profiles"`
}

func TestListTargetsSaved(t *testing.T) {
	m, _ := newTestManager(t, sessionstest.NewRunner())
	m.ViewsChanged(1)
	d := &fakeDiscovery{saved: []jumphost.SavedConnection{
		savedRDS("orders-db", "dev"), savedRDS("orders-db", "prod"),
		{Ref: jumphost.DirectTargetRef("billing")},
		{Ref: jumphost.TargetRef{Type: "rds", Name: "gone"}, ProfileMissing: true},
		// A forward serves no tool: list_targets leaves it out.
		{Ref: jumphost.TargetRef{Account: "111111111111", Region: "eu-west-3", Type: "forward", Name: "search.internal:443"}, Prefs: jumphost.ConnectionPrefs{Profile: "dev"}},
	}}
	cs, _ := newToolset(t, m, &Settings{AutoConnect: AutoConnectNonProd, Environments: mustEnvironments(t, "prod!=prod")}, d,
		fakeDatabases{"billing": {Name: "billing", Environment: "prod"}})
	text, isError := callTool(t, cs, "list_targets", nil)
	if isError {
		t.Fatal(text)
	}
	reply := decodeText[targetsReply](t, text)
	if len(reply.Targets) != 3 || len(reply.Profiles) != 2 {
		t.Fatalf("reply = %+v", reply)
	}
	opens := map[string]string{}
	for _, target := range reply.Targets {
		opens[target.Name+"|"+target.Profile] = target.Opens
	}
	if opens["orders-db|dev"] != "auto" || opens["orders-db|prod"] != "prompt" || opens["billing|"] != "prompt" {
		t.Fatalf("opens = %v", opens)
	}
	for _, target := range reply.Targets {
		// A direct database is reached outside AWS: its saved entry's placeholder region is not shown.
		if want := map[string]string{"orders-db": "eu-west-3", "billing": ""}[target.Name]; target.Region != want {
			t.Errorf("%s region = %q, want %q", target.Name, target.Region, want)
		}
	}
	if len(d.searched) != 0 {
		t.Fatal("without profile, list_targets must not call AWS")
	}
}

func TestListTargetsRefusesUnknownType(t *testing.T) {
	m, _ := newTestManager(t, sessionstest.NewRunner())
	d := &fakeDiscovery{saved: []jumphost.SavedConnection{savedRDS("orders-db", "dev")}}
	cs, _ := newToolset(t, m, &Settings{}, d, fakeDatabases{})
	text, isError := callTool(t, cs, "list_targets", map[string]any{"type": "kafka"})
	if !isError || !strings.Contains(text, "unknown type") {
		t.Fatalf("isError = %v, text = %q", isError, text)
	}
}

func TestListTargetsSearchIsUnattended(t *testing.T) {
	m, _ := newTestManager(t, sessionstest.NewRunner())
	d := &fakeDiscovery{found: []jumphost.ResourceSummary{{Name: "orders-db", Type: "rds"}, {Name: "platform", Type: "eks"}}}
	cs, _ := newToolset(t, m, &Settings{AutoConnect: AutoConnectAll}, d, nil)
	text, isError := callTool(t, cs, "list_targets", map[string]any{"profile": "dev", "type": "rds"})
	if isError {
		t.Fatal(text)
	}
	if reply := decodeText[targetsReply](t, text); len(reply.Targets) != 1 || reply.Targets[0].Name != "orders-db" || reply.Targets[0].Opens != "auto" || reply.Targets[0].Region != "eu-west-3" {
		t.Fatalf("reply = %+v", reply)
	}
	if len(d.searched) != 1 || !d.searched[0].Unattended || d.searched[0].AwsProfile != "dev" {
		t.Fatalf("searched = %+v", d.searched)
	}
	// Review Focus 1: an expired profile asks the agent to have the user sign in.
	d.searchErr = jumphost.ErrSignInRequired
	text, isError = callTool(t, cs, "list_targets", map[string]any{"profile": "dev"})
	if !isError || !strings.Contains(text, "sign in to profile dev again from airlock") {
		t.Fatalf("expired profile = %q", text)
	}
}

func TestListSessions(t *testing.T) {
	runner := sessionstest.NewRunner()
	m, _ := newTestManager(t, runner)
	q := &fakeQuerier{}
	openDirect(t, m, runner, "billing", dbmcpServer(q), false, "claude-code")
	openDirect(t, m, runner, "plain", nil, false, "")
	cs, _ := newToolset(t, m, &Settings{}, &fakeDiscovery{}, nil)
	text, isError := callTool(t, cs, "list_sessions", nil)
	if isError {
		t.Fatal(text)
	}
	reply := decodeText[struct {
		Sessions []struct {
			Target   string `json:"target"`
			OpenedBy string `json:"openedBy"`
			Tools    string `json:"tools"`
			About    string `json:"about"`
			State    string `json:"state"`
			Session  int    `json:"session"`
		} `json:"sessions"`
	}](t, text)
	if len(reply.Sessions) != 2 {
		t.Fatalf("sessions = %+v", reply.Sessions)
	}
	billing, plain := reply.Sessions[0], reply.Sessions[1]
	if billing.OpenedBy != "claude-code" || billing.Tools != "db" || !strings.Contains(billing.About, "billing") || billing.State != "running" {
		t.Fatalf("billing = %+v", billing)
	}
	if plain.OpenedBy != "you" || plain.Tools != "none" {
		t.Fatalf("plain = %+v", plain)
	}
}

func dbmcpServer(q *fakeQuerier) *mcp.Server {
	return dbmcp.NewServer(&dbmcp.ServerConfig{Name: "airlock-billing", Target: "billing", Via: "reached directly at billing:5432."}, q)
}

// decodeObjects decodes the JSON text of a reply into generic maps under key.
func decodeObjects(t *testing.T, text, key string) []map[string]any {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		t.Fatalf("reply is not JSON: %v\n%s", err, text)
	}
	var objects []map[string]any
	if err := json.Unmarshal(raw[key], &objects); err != nil {
		t.Fatalf("%s is not a list of objects: %v\n%s", key, err, text)
	}
	return objects
}

// The agent never sees the environment names: its JSON keeps the production flag only.
func TestAgentJSONKeepsProductionAndHasNoEnvironment(t *testing.T) {
	runner := sessionstest.NewRunner()
	m, _ := newTestManager(t, runner)
	openDirect(t, m, runner, "billing", nil, false, "")
	d := &fakeDiscovery{saved: []jumphost.SavedConnection{savedRDS("orders-db", "shop-live"), {Ref: jumphost.DirectTargetRef("billing")}}}
	cs, _ := newToolset(t, m, &Settings{Environments: mustEnvironments(t, "live!=*-live; staging=*stg*")}, d,
		fakeDatabases{"billing": {Name: "billing", Environment: "staging"}})
	for tool, key := range map[string]string{"list_targets": "targets", "list_sessions": "sessions"} {
		text, isError := callTool(t, cs, tool, nil)
		if isError {
			t.Fatalf("%s: %s", tool, text)
		}
		objects := decodeObjects(t, text, key)
		if len(objects) == 0 {
			t.Fatalf("%s: no %s in %s", tool, key, text)
		}
		for _, object := range objects {
			if _, ok := object["production"]; !ok {
				t.Errorf("%s: %v lacks production", tool, object)
			}
			if _, ok := object["environment"]; ok {
				t.Errorf("%s: %v exposes the environment", tool, object)
			}
		}
	}
}
