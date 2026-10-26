package jumphost

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft-public-org/airlock/internal/dynamomcp"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// installDynamoAWS answers ListTables (one table at most), denied unless ok.
func installDynamoAWS(t *testing.T, ok bool) {
	t.Helper()
	installAWSAPI(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Limit int }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		switch op := awsapitest.Operation(r); {
		case op != "ListTables" || body.Limit != 1:
			t.Errorf("unexpected operation %s, limit %d", op, body.Limit)
			w.WriteHeader(http.StatusBadRequest)
		case ok:
			awsapitest.Respond(t, w, http.StatusOK, map[string]any{"TableNames": []string{"orders"}})
		default:
			awsapitest.Respond(t, w, http.StatusBadRequest, map[string]string{"__type": "com.amazonaws.dynamodb.v20120810#AccessDeniedException", "message": "not authorized"})
		}
	})
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
	if started == nil || started.Region != "eu-west-1" || started.Profile != "dev" || !started.AllowWrites || started.AWS.Credentials == nil {
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
