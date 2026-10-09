package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/directdb"
	"github.com/n2jsoft/airlock/internal/dockerdb"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/sessions/sessionstest"
)

// fakeContainers lists containers, or fails with err; lists counts the listings.
type fakeContainers struct {
	err   error
	list  []dockerdb.Container
	lists int
}

func (f *fakeContainers) List(context.Context) ([]dockerdb.Container, error) {
	f.lists++
	return f.list, f.err
}

func (f *fakeContainers) Get(ctx context.Context, name string) (dockerdb.Container, bool, error) {
	list, err := f.List(ctx)
	for idx := range list {
		if list[idx].Name == name {
			return list[idx], true, err
		}
	}
	return dockerdb.Container{}, false, err
}

func dockerContainers() *fakeContainers {
	return &fakeContainers{list: []dockerdb.Container{
		{Name: "pg", Image: "postgres:16", Engine: directdb.EnginePostgres, Host: "127.0.0.1", Port: 55432, User: "postgres", Password: "secret", Database: "appdb"},
		{Name: "unpublished", Image: "postgres:16", Engine: directdb.EnginePostgres, User: "postgres", Password: "pw", Problem: "port 5432 is not published: run the container with -p <host-port>:5432"},
		{Name: "secretfile", Image: "postgres:16", Engine: directdb.EnginePostgres, Host: "127.0.0.1", Port: 55433, User: "postgres"},
	}}
}

func TestKindOfDocker(t *testing.T) {
	if kindOf(sessions.DockerResourceType) != kindDB {
		t.Fatal("a docker session must serve the db_ tools")
	}
}

func TestListTargetsListsDockerContainers(t *testing.T) {
	m, _ := newTestManager(t, sessionstest.NewRunner())
	m.ViewsChanged(1)
	cs, _ := newToolsetWith(t, m, &Settings{AutoConnect: AutoConnectNonProd}, &fakeDiscovery{}, nil, dockerContainers())
	text, isError := callTool(t, cs, "list_targets", nil)
	if isError {
		t.Fatal(text)
	}
	reply := decodeText[targetsReply](t, text)
	var names []string
	for idx := range reply.Targets {
		target := &reply.Targets[idx]
		if target.Type != "docker" {
			continue
		}
		names = append(names, target.Name)
		if target.Name == "pg" && (target.Description != "postgres · 127.0.0.1:55432 · appdb · postgres:16" || target.Opens != "auto" || target.Production || target.Saved) {
			t.Fatalf("pg = %+v", *target)
		}
	}
	if strings.Join(names, ",") != "pg,secretfile" {
		t.Fatalf("docker targets = %v, want the openable containers", names)
	}
}

func TestListTargetsReportsADockerFailure(t *testing.T) {
	m, _ := newTestManager(t, sessionstest.NewRunner())
	cs, _ := newToolsetWith(t, m, &Settings{}, &fakeDiscovery{}, nil, &fakeContainers{err: dockerdb.ErrDockerNotFound})
	text, isError := callTool(t, cs, "list_targets", nil)
	if isError || decodeText[targetsReply](t, text).Failures["docker"] != "docker CLI not found" {
		t.Fatalf("reply = %q", text)
	}
}

func TestListTargetsOfAnotherTypeSkipsDocker(t *testing.T) {
	m, _ := newTestManager(t, sessionstest.NewRunner())
	containers := dockerContainers()
	cs, _ := newToolsetWith(t, m, &Settings{}, &fakeDiscovery{}, nil, containers)
	if text, isError := callTool(t, cs, "list_targets", map[string]any{"type": "rds"}); isError {
		t.Fatal(text)
	}
	if containers.lists != 0 {
		t.Fatal("an rds listing must not run docker")
	}
}

func TestOpenSessionDocker(t *testing.T) {
	runner := sessionstest.NewRunner()
	runner.Direct("pg").ResourceType = sessions.DockerResourceType
	m, _ := newTestManager(t, runner)
	cs, _ := newToolsetWith(t, m, &Settings{AutoConnect: AutoConnectNonProd}, &fakeDiscovery{}, nil, dockerContainers())
	text, isError := callTool(t, cs, "open_session", map[string]any{"type": "docker", "name": "pg"})
	if isError {
		t.Fatal(text)
	}
	prepared := runner.PreparedOptions()
	if len(prepared) != 1 || prepared[0].ResourceType != "docker" || prepared[0].Name != "pg" || prepared[0].MCPAllowWrites ||
		prepared[0].OpenedBy != "agent" || prepared[0].AwsProfile != "" {
		t.Fatalf("prepared = %+v", prepared)
	}
	if text, isError = callTool(t, cs, "open_session", map[string]any{"type": "docker", "name": "pg", "writes": true}); !isError {
		t.Fatalf("writes without agentWrites = %q, want a refusal", text)
	}
}

func TestOpenSessionDockerRefusals(t *testing.T) {
	m, _ := newTestManager(t, sessionstest.NewRunner())
	cs, _ := newToolsetWith(t, m, &Settings{AutoConnect: AutoConnectAll}, &fakeDiscovery{}, nil, dockerContainers())
	cases := map[string]string{
		"nope":        "no running postgres or sqlserver container named",
		"unpublished": "is not published",
		"secretfile":  "the password of secretfile is unknown",
	}
	for name, want := range cases {
		if text, isError := callTool(t, cs, "open_session", map[string]any{"type": "docker", "name": name}); !isError || !strings.Contains(text, want) {
			t.Errorf("%s = %q, want %q", name, text, want)
		}
	}
}
