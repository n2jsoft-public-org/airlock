package jumphost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/dynamomcp"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// installDynamoAWS puts an aws executable on PATH whose list-tables succeeds when ok.
func installDynamoAWS(t *testing.T, ok bool) {
	t.Helper()
	listTables := "*list-tables*) echo '{\"TableNames\":[\"orders\"]}' ;;\n"
	if !ok {
		listTables = "*list-tables*) echo 'AccessDeniedException: not authorized' >&2; exit 254 ;;\n"
	}
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"*get-caller-identity*) echo arn:aws:iam::111111111111:user/dev ;;\n" + listTables + "esac\n"
	dir := t.TempDir()
	// #nosec G306 -- the fake aws must be executable.
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestDynamoDBListsOneTargetNamedAfterTheRegion(t *testing.T) {
	installDynamoAWS(t, true)
	got, err := testRunner(t).ListResourceSummaries(t.Context(), &Options{ResourceType: "dynamodb", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	if err != nil || len(got) != 1 || got[0].Name != "eu-west-3" || got[0].Type != "dynamodb" {
		t.Fatalf("targets = %+v, %v", got, err)
	}
}

func TestPrepareDynamoDBUsesTheNameAsRegionAndRunsNoTunnel(t *testing.T) {
	installDynamoAWS(t, true)
	var started *dynamomcp.SessionConfig
	dynamomcpStart = func(host *mcpserve.Host, cfg *dynamomcp.SessionConfig) (*mcpserve.Handle, error) {
		started = cfg
		return nil, nil
	}
	t.Cleanup(func() { dynamomcpStart = dynamomcp.Start })

	session, err := testRunner(t).Prepare(t.Context(), Options{ResourceType: "dynamodb", Name: "eu-west-1", AwsProfile: "dev", Unattended: true, MCPAllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	if session.Target != "eu-west-1" || session.Region != "eu-west-1" || session.LocalPort != 0 || len(session.CommandArgs) != 0 ||
		session.Details.ConnectedServer != "dynamodb.eu-west-1.amazonaws.com" {
		t.Fatalf("session = %+v", session)
	}
	if started == nil || started.Region != "eu-west-1" || started.Profile != "dev" || !started.AllowWrites || started.AWS == nil {
		t.Fatalf("mcp = %+v", started)
	}
}

func TestPrepareDynamoDBFailsWithoutAccess(t *testing.T) {
	installDynamoAWS(t, false)
	_, err := testRunner(t).Prepare(t.Context(), Options{ResourceType: "dynamodb", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	if err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareDynamoDBRefusesNoMCP(t *testing.T) {
	installDynamoAWS(t, true)
	_, err := testRunner(t).Prepare(t.Context(), Options{ResourceType: "dynamodb", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true, SkipMCP: true})
	if err == nil {
		t.Fatal("expected --no-mcp to be refused")
	}
}

func TestPrepareDynamoDBRefusesATargetOtherThanTheRegion(t *testing.T) {
	installDynamoAWS(t, true)
	_, err := testRunner(t).Prepare(t.Context(), Options{ResourceType: "dynamodb", Name: "eu-west-1", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	if err == nil || !strings.Contains(err.Error(), "eu-west-1") || !strings.Contains(err.Error(), "eu-west-3") {
		t.Fatalf("err = %v", err)
	}
}
