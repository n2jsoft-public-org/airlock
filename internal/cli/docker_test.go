package cli

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/dockerdb"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
)

type stubContainers struct{}

func (stubContainers) List(context.Context) ([]dockerdb.Container, error) { return nil, nil }
func (stubContainers) Get(context.Context, string) (dockerdb.Container, bool, error) {
	return dockerdb.Container{}, false, nil
}

func TestCheckDockerTarget(t *testing.T) {
	cases := []struct {
		flag        string
		want        string
		wantErr     string
		containers  jumphost.DockerContainers
		args        []string
		interactive bool
	}{
		{args: []string{"docker", "pg"}, containers: stubContainers{}, want: "pg"},
		{args: []string{"docker"}, containers: stubContainers{}, interactive: true, want: ""},
		{args: []string{"docker"}, containers: stubContainers{}, wantErr: "a container name is required: airlock docker <container>"},
		{args: []string{"docker", "pg", "5432"}, containers: stubContainers{}, wantErr: "a local port does not apply to docker containers"},
		{args: []string{"docker", "pg"}, flag: "region", containers: stubContainers{}, wantErr: "--region does not apply to docker containers"},
		{args: []string{"docker", "pg"}, wantErr: "docker CLI not found: install Docker to reach local databases and caches"},
	}
	for _, tc := range cases {
		cmd := NewRootCmd()
		if tc.flag != "" {
			if err := cmd.Flags().Set(tc.flag, "eu-west-3"); err != nil {
				t.Fatal(err)
			}
		}
		got, err := checkDockerTarget(cmd, tc.args, tc.containers, tc.interactive)
		switch {
		case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.wantErr)
		case tc.wantErr == "" && (err != nil || got != tc.want):
			t.Errorf("%v: got %q, %v", tc.args, got, err)
		}
	}
}

func TestDockerWritesDefaultOn(t *testing.T) {
	cmd := NewRootCmd()
	if !dockerAllowWrites(cmd, false, false) {
		t.Fatal("writes must default on for docker")
	}
	if err := cmd.Flags().Set("mcp-allow-writes", "false"); err != nil {
		t.Fatal(err)
	}
	if dockerAllowWrites(cmd, false, false) {
		t.Fatal("--mcp-allow-writes=false must turn them off")
	}
}

func TestDockerRunError(t *testing.T) {
	runErr := fmt.Errorf("%w for cache: set REDIS_PASSWORD (or --requirepass) on the container", jumphost.ErrPasswordRequired)
	err := dockerRunError(runErr)
	if err == nil || err.Error() != "password required for cache: set REDIS_PASSWORD (or --requirepass) on the container, or open it from the dashboard" {
		t.Fatalf("err = %v", err)
	}
	rejected := fmt.Errorf("pg: %w", jumphost.ErrSignInRejected)
	if err := dockerRunError(rejected); !errors.Is(err, jumphost.ErrSignInRejected) ||
		err.Error() != "pg: the database rejected the credentials: set the right password on the container, or open it from the dashboard" {
		t.Fatalf("rejected = %v", err)
	}
	if other := errors.New("boom"); !errors.Is(dockerRunError(other), other) || dockerRunError(other).Error() != "boom" {
		t.Fatal("other errors pass through")
	}
}

func TestDirectRunErrorExplainsARejectedPassword(t *testing.T) {
	rejected := fmt.Errorf("billing: %w", jumphost.ErrSignInRejected)
	if err := directRunError("billing", rejected); !errors.Is(err, jumphost.ErrSignInRejected) ||
		err.Error() != "billing: the database rejected the credentials: open it from the dashboard to enter the password again" {
		t.Fatalf("err = %v", err)
	}
}

func TestDockerOptionsWritesOnlyWithoutTerminal(t *testing.T) {
	cmd := NewRootCmd()
	if dockerOptions(cmd, &jumphost.Options{}, true, false).MCPAllowWrites {
		t.Fatal("the dashboard's base options must not get MCP writes")
	}
	if !dockerOptions(cmd, &jumphost.Options{}, false, false).MCPAllowWrites {
		t.Fatal("a non-interactive docker run defaults to writes")
	}
}
