package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/n2jsoft/airlock/internal/daemon"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/spf13/cobra"
)

// openService is a daemon with sessions: it records the openings it is asked for.
type openService struct {
	sessions.Service
	opened   []jumphost.Options
	snapshot sessions.Snapshot
}

func (s *openService) Snapshot(context.Context) (sessions.Snapshot, error) { return s.snapshot, nil }

func (s *openService) Open(_ context.Context, req sessions.OpenRequest) (sessions.ID, error) { //nolint:gocritic // implements sessions.Service.
	s.opened = append(s.opened, req.Options)
	return 99, nil
}

func runningSession(id sessions.ID, profile, region, resourceType, target string) sessions.Session {
	return sessions.Session{ID: id, Profile: profile, Region: region, ResourceType: resourceType, Target: target, State: sessions.StateRunning}
}

func quietCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetErr(io.Discard)
	return cmd
}

func TestOpenInDaemonReusesTheRunningSessionOfTheTarget(t *testing.T) {
	t.Setenv("AWS_PROFILE", "")
	ended := runningSession(1, "dev", "eu-west-3", "rds", "archive-db")
	ended.State = sessions.StateEnded
	service := &openService{snapshot: sessions.Snapshot{Sessions: []sessions.Session{
		ended,
		runningSession(2, "prod", "eu-west-3", "rds", "archive-db"),
		runningSession(3, "dev", "eu-west-3", "rds", "Archive-DB"),
		runningSession(4, "default", "us-east-1", "rds", "orders-db"),
		runningSession(5, "staging", "eu-west-3", "eks", "archive-db"),
	}}}
	cases := []struct {
		name    string
		options jumphost.Options
		want    sessions.ID
	}{
		{name: "same profile and region", options: jumphost.Options{ResourceType: "rds", Name: "archive-db", AwsProfile: "dev", AwsRegion: "eu-west-3"}, want: 3},
		{name: "region not given", options: jumphost.Options{ResourceType: "rds", Name: "archive-db", AwsProfile: "prod"}, want: 2},
		{name: "profile not given is the default one", options: jumphost.Options{ResourceType: "rds", Name: "orders-db"}, want: 4},
		{name: "another region", options: jumphost.Options{ResourceType: "rds", Name: "archive-db", AwsProfile: "dev", AwsRegion: "us-east-1"}, want: 99},
		{name: "another type", options: jumphost.Options{ResourceType: "elasticache", Name: "archive-db", AwsProfile: "dev"}, want: 99},
		{name: "only ended", options: jumphost.Options{ResourceType: "rds", Name: "archive-db", AwsProfile: "staging"}, want: 99},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service.opened = nil
			id, err := openInDaemon(quietCmd(t), service, &tc.options, nil)
			if err != nil || id != tc.want {
				t.Fatalf("id = %d, err = %v, want %d", id, err, tc.want)
			}
			if opened := len(service.opened) == 1; opened != (tc.want == 99) {
				t.Fatalf("openings = %v", service.opened)
			}
		})
	}
}

func TestOpenInDaemonResolvesAMissingProfileFromTheEnvironment(t *testing.T) {
	t.Setenv("AWS_PROFILE", "prod")
	service := &openService{snapshot: sessions.Snapshot{Sessions: []sessions.Session{
		runningSession(1, "default", "eu-west-3", "rds", "archive-db"),
		runningSession(2, "prod", "eu-west-3", "rds", "archive-db"),
	}}}
	if id, err := openInDaemon(quietCmd(t), service, &jumphost.Options{ResourceType: "rds", Name: "archive-db"}, nil); err != nil || id != 2 {
		t.Fatalf("id = %d, err = %v", id, err)
	}
}

func TestDetachedDaemonWorksInItsOwnDirectory(t *testing.T) {
	paths, err := daemon.PathsIn(filepath.Join(shortTempDir(t), "d"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := detachedDaemonCmd("airlock", paths)
	if cmd.Dir != paths.Dir {
		t.Fatalf("dir = %q, want %q", cmd.Dir, paths.Dir)
	}
	if info, err := os.Stat(paths.Dir); err != nil || !info.IsDir() {
		t.Fatalf("the daemon directory must exist: %v", err)
	}
}
