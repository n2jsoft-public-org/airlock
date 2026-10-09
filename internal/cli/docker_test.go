package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/dockerdb"
	"github.com/n2jsoft/airlock/internal/jumphost"
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
		{args: []string{"docker", "pg", "5432"}, containers: stubContainers{}, wantErr: "a local port does not apply to docker databases"},
		{args: []string{"docker", "pg"}, flag: "region", containers: stubContainers{}, wantErr: "--region does not apply to docker databases"},
		{args: []string{"docker", "pg"}, wantErr: "docker CLI not found: install Docker to reach local databases"},
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
	if !dockerAllowWrites(cmd, false) {
		t.Fatal("writes must default on for docker")
	}
	if err := cmd.Flags().Set("mcp-allow-writes", "false"); err != nil {
		t.Fatal(err)
	}
	if dockerAllowWrites(cmd, false) {
		t.Fatal("--mcp-allow-writes=false must turn them off")
	}
}

func TestDockerRunError(t *testing.T) {
	err := dockerRunError("pg", fmt.Errorf("%w for pg", jumphost.ErrPasswordRequired))
	if err == nil || !strings.Contains(err.Error(), "no password for pg: set POSTGRES_PASSWORD") {
		t.Fatalf("err = %v", err)
	}
}

func TestDockerOptionsWritesOnlyWithoutTerminal(t *testing.T) {
	cmd := NewRootCmd()
	if dockerOptions(cmd, &jumphost.Options{}, true).MCPAllowWrites {
		t.Fatal("the dashboard's base options must not get MCP writes")
	}
	if !dockerOptions(cmd, &jumphost.Options{}, false).MCPAllowWrites {
		t.Fatal("a non-interactive docker run defaults to writes")
	}
}
