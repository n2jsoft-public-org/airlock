package jumphost

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft/airlock/internal/logsmcp"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// installLogsAWS answers DescribeLogGroups (one group at most), denied unless ok.
func installLogsAWS(t *testing.T, ok bool) {
	t.Helper()
	installAWSAPI(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Limit int }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		switch op := awsapitest.Operation(r); {
		case op != "DescribeLogGroups" || body.Limit != 1:
			t.Errorf("unexpected operation %s, limit %d", op, body.Limit)
			w.WriteHeader(http.StatusBadRequest)
		case ok:
			awsapitest.Respond(t, w, http.StatusOK, map[string]any{"logGroups": []map[string]any{{"logGroupName": "/aws/lambda/orders"}}})
		default:
			awsapitest.Respond(t, w, http.StatusBadRequest, map[string]string{"__type": "AccessDeniedException", "message": "not authorized"})
		}
	})
}

// stubLogsMCP replaces the start of the Logs MCP server and returns the config it was given.
func stubLogsMCP(t *testing.T) **logsmcp.SessionConfig {
	t.Helper()
	var started *logsmcp.SessionConfig
	logsmcpStart = func(_ *mcpserve.Host, cfg *logsmcp.SessionConfig) (*mcpserve.Handle, error) {
		started = cfg
		return nil, nil
	}
	t.Cleanup(func() { logsmcpStart = logsmcp.Start })
	return &started
}

func TestLogsListsOneTargetNamedAfterTheRegion(t *testing.T) {
	installLogsAWS(t, true)
	got, err := testRunner(t).ListResourceSummaries(t.Context(), &Options{ResourceType: "logs", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	if err != nil || len(got) != 1 || got[0].Name != "eu-west-3" || got[0].Type != "logs" {
		t.Fatalf("targets = %+v, %v", got, err)
	}
}

func TestPrepareLogsUsesTheNameAsRegionAndRunsNoTunnel(t *testing.T) {
	installLogsAWS(t, true)
	started := stubLogsMCP(t)
	session, err := testRunner(t).Prepare(t.Context(), Options{ResourceType: "logs", Name: "eu-west-1", AwsProfile: "dev", Unattended: true})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	if session.Target != "eu-west-1" || session.Region != "eu-west-1" || session.LocalPort != 0 || len(session.CommandArgs) != 0 ||
		session.Details.ConnectedServer != "logs.eu-west-1.amazonaws.com" || session.Details.RestartCommand != "airlock logs eu-west-1 --profile dev --region eu-west-1" {
		t.Fatalf("session = %+v", session)
	}
	if cfg := *started; cfg == nil || cfg.Region != "eu-west-1" || cfg.Profile != "dev" || cfg.AWS.Credentials == nil {
		t.Fatalf("mcp = %+v", cfg)
	}
}

func TestPrepareLogsFailsWithoutAccess(t *testing.T) {
	installLogsAWS(t, false)
	_, err := testRunner(t).Prepare(t.Context(), Options{ResourceType: "logs", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	if err == nil || !strings.Contains(err.Error(), "AccessDenied") {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareLogsRefusesNoMCPAndWrites(t *testing.T) {
	installLogsAWS(t, true)
	stubLogsMCP(t)
	for _, options := range []Options{{SkipMCP: true}, {MCPAllowWrites: true}} {
		options.ResourceType, options.AwsProfile, options.AwsRegion, options.Unattended = "logs", "dev", "eu-west-3", true
		if _, err := testRunner(t).Prepare(t.Context(), options); err == nil {
			t.Fatalf("Prepare(%+v) succeeded", options)
		}
	}
}

func TestPrepareLogsRefusesATargetOtherThanTheRegion(t *testing.T) {
	installLogsAWS(t, true)
	_, err := testRunner(t).Prepare(t.Context(), Options{ResourceType: "logs", Name: "eu-west-1", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	if err == nil || !strings.Contains(err.Error(), "eu-west-1") || !strings.Contains(err.Error(), "eu-west-3") {
		t.Fatalf("err = %v", err)
	}
}
