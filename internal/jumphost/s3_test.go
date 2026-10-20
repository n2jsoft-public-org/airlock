package jumphost

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/n2jsoft/airlock/internal/s3mcp"
)

// installS3AWS puts an aws executable on PATH whose list-buckets succeeds when ok.
func installS3AWS(t *testing.T, ok bool) {
	t.Helper()
	listBuckets := "*list-buckets*) echo '{\"Buckets\":[{\"Name\":\"logs\"}]}' ;;\n"
	if !ok {
		listBuckets = "*list-buckets*) echo 'AccessDenied: not authorized' >&2; exit 254 ;;\n"
	}
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"*get-caller-identity*) echo arn:aws:iam::111111111111:user/dev ;;\n" + listBuckets + "esac\n"
	dir := t.TempDir()
	// #nosec G306 -- the fake aws must be executable.
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestS3ListsOneTargetForTheProfile(t *testing.T) {
	installS3AWS(t, true)
	got, err := testRunner(t).ListResourceSummaries(t.Context(), &Options{ResourceType: "s3", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	if err != nil || len(got) != 1 || got[0].Name != "buckets" || got[0].Type != "s3" || got[0].Description != "S3 buckets of dev" {
		t.Fatalf("targets = %+v, %v", got, err)
	}
}

func TestPrepareS3RunsNoTunnelAndStartsTheServer(t *testing.T) {
	awsapitest.Env(t, func(http.ResponseWriter, *http.Request) {}, "dev")
	installS3AWS(t, true)
	var started *s3mcp.SessionConfig
	s3mcpStart = func(_ *mcpserve.Host, cfg *s3mcp.SessionConfig) (*mcpserve.Handle, error) {
		started = cfg
		return nil, nil
	}
	t.Cleanup(func() { s3mcpStart = s3mcp.Start })

	session, err := testRunner(t).Prepare(t.Context(), Options{ResourceType: "s3", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true, MCPAllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	if session.Target != "buckets" || session.Region != "eu-west-3" || session.LocalPort != 0 || len(session.CommandArgs) != 0 ||
		session.Details.ConnectedServer != "s3.eu-west-3.amazonaws.com" {
		t.Fatalf("session = %+v", session)
	}
	if started == nil || started.Region != "eu-west-3" || started.Profile != "dev" || !started.AllowWrites || started.AWS.Credentials == nil {
		t.Fatalf("mcp = %+v", started)
	}
}

func TestPrepareS3FailsWithoutAccess(t *testing.T) {
	installS3AWS(t, false)
	_, err := testRunner(t).Prepare(t.Context(), Options{ResourceType: "s3", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	if err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareS3RefusesNoMCP(t *testing.T) {
	installS3AWS(t, true)
	_, err := testRunner(t).Prepare(t.Context(), Options{ResourceType: "s3", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true, SkipMCP: true})
	if err == nil {
		t.Fatal("expected --no-mcp to be refused")
	}
}

func TestPrepareS3RefusesAnotherTarget(t *testing.T) {
	installS3AWS(t, true)
	_, err := testRunner(t).Prepare(t.Context(), Options{ResourceType: "s3", Name: "logs", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	if err == nil || !strings.Contains(err.Error(), "logs") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareS3RefusesTheSameProfileInAnotherRegion(t *testing.T) {
	installS3AWS(t, true)
	s3mcpStart = func(*mcpserve.Host, *s3mcp.SessionConfig) (*mcpserve.Handle, error) { return nil, nil }
	t.Cleanup(func() { s3mcpStart = s3mcp.Start })

	runner := testRunner(t)
	first, err := runner.Prepare(t.Context(), Options{ResourceType: "s3", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Prepare(t.Context(), Options{ResourceType: "s3", AwsProfile: "dev", AwsRegion: "us-east-1", Unattended: true}); err == nil {
		t.Fatal("a second S3 session of the same profile was opened")
	}
	first.Release()
	second, err := runner.Prepare(t.Context(), Options{ResourceType: "s3", AwsProfile: "dev", AwsRegion: "us-east-1", Unattended: true})
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	second.Release()
}
