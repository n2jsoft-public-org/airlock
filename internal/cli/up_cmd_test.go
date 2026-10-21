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
	}}}
	var out bytes.Buffer
	err := closeStack(t.Context(), &out, service, "/repo/.airlock.json#dev", []jumphost.Options{
		{ResourceType: "rds", Name: "orders-db"}, {ResourceType: "eks", Name: "platform"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(service.stopped, []sessions.ID{1, 4}) {
		t.Fatalf("stopped = %v, want the dev sessions up opened", service.stopped)
	}
	if !strings.Contains(out.String(), "eks platform: kept") {
		t.Fatalf("output:\n%s", out.String())
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
	}}}
	var out bytes.Buffer
	err := closeStack(t.Context(), &out, service, "", []jumphost.Options{
		{ResourceType: "rds", Name: "orders-db"}, {ResourceType: "eks", Name: "platform"}, {ResourceType: "docker", Name: "redis"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(service.stopped) != 2 || service.stopped[0] != 1 || service.stopped[1] != 3 {
		t.Fatalf("stopped = %v", service.stopped)
	}
	if !strings.Contains(out.String(), "eks platform: not running") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestConfirmStackAsksOnlyForProduction(t *testing.T) {
	t.Setenv("AWS_PROFILE", "")
	envs, err := environment.Parse("prod!=*prod*; dev=*dev*")
	if err != nil {
		t.Fatal(err)
	}
	ask := func(answer string, targets ...jumphost.Options) (bool, string) {
		cmd := &cobra.Command{}
		var out bytes.Buffer
		cmd.SetIn(strings.NewReader(answer))
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		return confirmStack(cmd, ".airlock.json", "dev", targets, envs, nil), out.String()
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
	if want := "/repo/.airlock.json\ndev (default)\n  rds orders-db (p/r) → localhost:5432\nqa\n  forward h:443\n"; out.String() != want {
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
