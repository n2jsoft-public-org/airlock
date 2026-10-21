package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/projectstack"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/spf13/cobra"
)

// stackService is a session service whose openings of the names in fail fail.
type stackService struct {
	sessions.Service
	fail     map[string]bool
	opened   []jumphost.Options
	stopped  []sessions.ID
	taken    map[sessions.ID]string
	snapshot sessions.Snapshot
	nextID   sessions.ID
}

func (s *stackService) Snapshot(context.Context) (sessions.Snapshot, error) { return s.snapshot, nil }

func (s *stackService) Open(_ context.Context, req sessions.OpenRequest) (sessions.ID, error) { //nolint:gocritic // implements sessions.Service.
	s.opened = append(s.opened, req.Options)
	if s.fail[req.Options.Name] {
		return 0, errors.New("no jumphost")
	}
	s.nextID++
	return s.nextID, nil
}

func (s *stackService) TakeOver(_ context.Context, id sessions.ID, stack string) error {
	if s.taken == nil {
		s.taken = map[sessions.ID]string{}
	}
	s.taken[id] = stack
	return nil
}

func (s *stackService) Stop(_ context.Context, id sessions.ID) error {
	s.stopped = append(s.stopped, id)
	return nil
}

func TestOpenStackReusesRunningSessionsAndGoesOnAfterAFailure(t *testing.T) {
	t.Setenv("AWS_PROFILE", "")
	service := &stackService{
		nextID: 10,
		fail:   map[string]bool{"broken-db": true},
		snapshot: sessions.Snapshot{Sessions: []sessions.Session{
			runningSession(3, "default", "eu-west-3", "eks", "platform"),
			{ID: 4, ResourceType: "direct", Target: "legacy-billing", State: sessions.StateRunning},
		}},
	}
	targets := []jumphost.Options{
		{ResourceType: "rds", Name: "broken-db"},
		{ResourceType: "eks", Name: "platform"},
		{ResourceType: "direct", Name: "legacy-billing"},
		{ResourceType: "forward", Name: "search.internal:443", LocalPortNumber: 9200},
	}
	var out bytes.Buffer
	ids, failed, err := openStack(t.Context(), &out, service, targets, nil)
	if err != nil || !failed {
		t.Fatalf("openStack() = %v, failed %v", err, failed)
	}
	if !slices.Equal(ids, []sessions.ID{0, 3, 4, 11}) {
		t.Fatalf("ids = %v, want the failure, the reused sessions and the new one", ids)
	}
	if len(service.opened) != 2 || service.opened[0].Name != "broken-db" || service.opened[1].LocalPortNumber != 9200 {
		t.Fatalf("opened = %+v", service.opened)
	}
	for _, want := range []string{"failed: no jumphost", "eks platform: already running", "direct legacy-billing: already running", "3 of 4 targets open"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// An agent's session up reuses becomes the stack's: the gateway would close it under the user.
func TestOpenStackTakesOverAnAgentSession(t *testing.T) {
	t.Setenv("AWS_PROFILE", "")
	agents := runningSession(3, "default", "eu-west-3", "rds", "orders-db")
	agents.OpenedBy = "claude-code"
	service := &stackService{snapshot: sessions.Snapshot{Sessions: []sessions.Session{agents, runningSession(4, "default", "eu-west-3", "eks", "platform")}}}
	targets := []jumphost.Options{{ResourceType: "rds", Name: "orders-db", Stack: "/repo/.airlock.json#dev"}, {ResourceType: "eks", Name: "platform", Stack: "/repo/.airlock.json#dev"}}
	var out bytes.Buffer
	if _, _, err := openStack(t.Context(), &out, service, targets, nil); err != nil {
		t.Fatal(err)
	}
	if len(service.taken) != 1 || service.taken[3] != "/repo/.airlock.json#dev" {
		t.Fatalf("taken = %v, want the agent's session only", service.taken)
	}
	if !strings.Contains(out.String(), "rds orders-db: already running, opened by claude-code") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestOpenStackFailsWhenNothingOpens(t *testing.T) {
	service := &stackService{fail: map[string]bool{"a": true, "b": true}}
	_, _, err := openStack(t.Context(), &bytes.Buffer{}, service, []jumphost.Options{{ResourceType: "rds", Name: "a"}, {ResourceType: "rds", Name: "b"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "rds a: no jumphost") || !strings.Contains(err.Error(), "rds b: no jumphost") {
		t.Fatalf("openStack() error = %v", err)
	}
}

func TestCloseStackStopsOnlyTheSessionsUpOpenedForTheStack(t *testing.T) {
	t.Setenv("AWS_PROFILE", "")
	owned := func(s sessions.Session, key string) sessions.Session {
		s.Options.Stack = key
		return s
	}
	service := &stackService{snapshot: sessions.Snapshot{Sessions: []sessions.Session{
		owned(runningSession(1, "default", "eu-west-3", "rds", "orders-db"), "/repo/.airlock.json#dev"),
		runningSession(2, "default", "eu-west-3", "eks", "platform"),
		owned(runningSession(3, "default", "eu-west-3", "rds", "orders-db"), "/repo/.airlock.json#staging"),
		owned(runningSession(4, "default", "eu-west-3", "rds", "dropped-from-file"), "/repo/.airlock.json#dev"),
		owned(sessions.Session{ID: 5, ResourceType: "rds", Target: "ended", State: sessions.StateEnded}, "/repo/.airlock.json#dev"),
		owned(sessions.Session{ID: 6, ResourceType: "rds", Target: "asleep", State: sessions.StateEnded, ReconnectAt: time.Now()}, "/repo/.airlock.json#dev"),
		owned(runningSession(7, "default", "eu-west-3", "rds", "shared-db"), "/other/.airlock.json#dev"),
	}}}
	var out bytes.Buffer
	err := closeStack(t.Context(), &out, service, "/repo/.airlock.json#dev", []jumphost.Options{
		{ResourceType: "rds", Name: "orders-db"}, {ResourceType: "eks", Name: "platform"}, {ResourceType: "rds", Name: "shared-db"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(service.stopped, []sessions.ID{1, 4, 6}) {
		t.Fatalf("stopped = %v, want the dev sessions up opened, also the one waiting to reconnect", service.stopped)
	}
	for _, want := range []string{"eks platform: kept, airlock up did not open it", "rds shared-db: kept, airlock up opened it for /other/.airlock.json#dev"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if err := closeStack(t.Context(), &out, &stackService{}, "/repo/.airlock.json#dev", nil, false); err != nil || !strings.Contains(out.String(), "airlock up opened nothing") {
		t.Fatalf("closeStack() = %v\n%s", err, out.String())
	}
}

func TestPrintAddressesOfTheOpenedTargets(t *testing.T) {
	list := []sessions.Session{
		{ID: 1, ResourceType: "rds", Target: "orders-db", LocalPort: 5432},
		{ID: 2, ResourceType: "direct", Target: "legacy", Details: jumphost.SessionDetails{ConnectedServer: "db.internal:1433"}},
	}
	var out bytes.Buffer
	printAddresses(&out, list, []sessions.ID{2, 0, 1})
	if want := "direct legacy → db.internal:1433\nrds orders-db → localhost:5432\n"; out.String() != want {
		t.Fatalf("printAddresses() =\n%s", out.String())
	}
}

func TestCloseStackWithAllStopsTheRunningSessionsOfTheStack(t *testing.T) {
	t.Setenv("AWS_PROFILE", "")
	service := &stackService{snapshot: sessions.Snapshot{Sessions: []sessions.Session{
		runningSession(1, "default", "eu-west-3", "rds", "orders-db"),
		runningSession(2, "default", "eu-west-3", "rds", "other-db"),
		{ID: 3, ResourceType: sessions.DockerCacheResourceType, Target: "redis", State: sessions.StateRunning},
		{ID: 4, ResourceType: "eks", Target: "platform", Profile: "default", State: sessions.StateEnded, Reconnecting: true},
	}}}
	var out bytes.Buffer
	err := closeStack(t.Context(), &out, service, "", []jumphost.Options{
		{ResourceType: "rds", Name: "orders-db"}, {ResourceType: "eks", Name: "platform"}, {ResourceType: "docker", Name: "redis"}, {ResourceType: "eks", Name: "other"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(service.stopped, []sessions.ID{1, 4, 3}) {
		t.Fatalf("stopped = %v", service.stopped)
	}
	if !strings.Contains(out.String(), "eks other: not running") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestConfirmStackAsksOnlyForProduction(t *testing.T) {
	t.Setenv("AWS_PROFILE", "")
	envs, err := environment.Parse("prod!=*prod*; dev=*dev*")
	if err != nil {
		t.Fatal(err)
	}
	interactive := true
	ask := func(answer string, targets ...jumphost.Options) (bool, string) {
		cmd := &cobra.Command{}
		var out bytes.Buffer
		cmd.SetIn(strings.NewReader(answer))
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		return confirmStack(cmd, ".airlock.json", "dev", targets, envs, nil, interactive), out.String()
	}
	if ok, out := ask("", jumphost.Options{ResourceType: "rds", Name: "a", AwsProfile: "app-dev"}); !ok || strings.Contains(out, "[y/N]") || !strings.Contains(out, "rds a (app-dev/) [dev]") {
		t.Fatalf("no production: %v\n%s", ok, out)
	}
	prod := jumphost.Options{ResourceType: "rds", Name: "a", AwsProfile: "app-prod"}
	if ok, out := ask("n\n", prod); ok || !strings.Contains(out, "[prod]") {
		t.Fatalf("production refused: %v\n%s", ok, out)
	}
	if ok, _ := ask("y\n", prod); !ok {
		t.Fatal("production accepted")
	}
	// airlock exec --stack in a script: production is refused, it cannot be confirmed.
	interactive = false
	if ok, out := ask("y\n", prod); ok || strings.Contains(out, "[y/N]") || !strings.Contains(out, "run in a terminal to confirm") {
		t.Fatalf("production without a terminal: %v\n%s", ok, out)
	}
	if ok, _ := ask("", jumphost.Options{ResourceType: "rds", Name: "a", AwsProfile: "app-dev"}); !ok {
		t.Fatal("no production without a terminal")
	}
}

func TestStackOptionsAndList(t *testing.T) {
	file, err := projectstack.Parse("/repo/.airlock.json", []byte(`{"default":"dev","stacks":{
		"dev":[{"type":"rds","name":"orders-db","port":5432,"dbUser":"app_ro","dbSecret":"s","mcpAllowWrites":true,"profile":"p","region":"r"}],
		"qa":[{"type":"forward","host":"h","remotePort":443}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	_, targets, err := file.Stack("dev")
	if err != nil {
		t.Fatal(err)
	}
	got := stackOptions(targets)[0]
	want := jumphost.Options{ResourceType: "rds", Name: "orders-db", AwsProfile: "p", AwsRegion: "r", DBUser: "app_ro", DBSecretARN: "s", LocalPortNumber: 5432, MCPAllowWrites: true}
	if got != want {
		t.Fatalf("stackOptions() = %+v", got)
	}
	var out bytes.Buffer
	printStacks(&out, file)
	if want := "/repo/.airlock.json\ndev (default)\n  rds orders-db (p/r) → localhost:5432 [MCP writes]\nqa\n  forward h:443\n"; out.String() != want {
		t.Fatalf("printStacks() =\n%s", out.String())
	}
}

func TestCompleteStacks(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	data := `{"default":"dev","stacks":{"dev":[{"type":"eks","name":"a"}],"qa":[{"type":"eks","name":"b"}]}}`
	if err := os.WriteFile(filepath.Join(dir, projectstack.FileName), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	got, _ := completeStacks(nil, nil, "")
	if strings.Join(got, ",") != "dev\tdefault,qa" {
		t.Fatalf("completeStacks() = %q", got)
	}
}

// up and down name the stack's sessions by the file's path: a symbolic link gives the same one.
func TestLoadStackFileResolvesSymbolicLinks(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "repo")
	if err = os.MkdirAll(filepath.Join(repo, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repo, projectstack.FileName), []byte(`{"stacks":{"dev":[{"type":"eks","name":"a"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err = os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(link)
	file, err := loadStackFile()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(repo, projectstack.FileName); file.Path != want {
		t.Fatalf("path = %q, want %q", file.Path, want)
	}
}

// A reused session keeps its port: up says so rather than close the user's session.
func TestOpenStackWarnsWhenAReusedSessionRunsOnAnotherPort(t *testing.T) {
	t.Setenv("AWS_PROFILE", "")
	search := runningSession(3, "default", "eu-west-3", "forward", "search.internal:443")
	search.LocalPort = 9201
	orders := runningSession(4, "default", "eu-west-3", "rds", "orders-db")
	orders.LocalPort = 5432
	service := &stackService{snapshot: sessions.Snapshot{Sessions: []sessions.Session{search, orders}}}
	targets := []jumphost.Options{
		{ResourceType: "forward", Name: "search.internal:443", LocalPortNumber: 9200},
		{ResourceType: "rds", Name: "orders-db", LocalPortNumber: 5432},
	}
	var out bytes.Buffer
	if _, _, err := openStack(t.Context(), &out, service, targets, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "forward search.internal:443: warning: the running session listens on localhost:9201, not on port 9200") {
		t.Fatalf("output lacks the port warning:\n%s", out.String())
	}
	if strings.Count(out.String(), "warning") != 1 {
		t.Fatalf("only the session on another port is warned about:\n%s", out.String())
	}
	direct := sessions.Session{ResourceType: "direct", State: sessions.StateRunning}
	if got := otherPortWarning(&direct, &jumphost.Options{LocalPortNumber: 5432}); got != "" {
		t.Fatalf("a direct session has no local port, warning %q", got)
	}
	if got := otherPortWarning(&search, &jumphost.Options{}); got != "" {
		t.Fatalf("no port asked for, warning %q", got)
	}
}
