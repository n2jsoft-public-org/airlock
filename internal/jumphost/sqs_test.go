package jumphost

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
	"github.com/n2jsoft-public-org/airlock/internal/sqsmcp"
)

// installSQSAWS answers ListQueues (one queue at most), denied unless ok.
func installSQSAWS(t *testing.T, ok bool) {
	t.Helper()
	installAWSAPI(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct{ MaxResults int }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		switch op := awsapitest.Operation(r); {
		case op != "ListQueues" || body.MaxResults != 1:
			t.Errorf("unexpected operation %s, max results %d", op, body.MaxResults)
			w.WriteHeader(http.StatusBadRequest)
		case ok:
			awsapitest.Respond(t, w, http.StatusOK, map[string]any{"QueueUrls": []string{"https://sqs.eu-west-3.amazonaws.com/1/orders"}})
		default:
			awsapitest.Respond(t, w, http.StatusBadRequest, map[string]string{"__type": "AccessDeniedException", "message": "not authorized"})
		}
	})
}

// stubSQSMCP replaces the start of the SQS MCP server and returns the config it was given.
func stubSQSMCP(t *testing.T) **sqsmcp.SessionConfig {
	t.Helper()
	var started *sqsmcp.SessionConfig
	sqsmcpStart = func(_ *mcpserve.Host, cfg *sqsmcp.SessionConfig) (*mcpserve.Handle, error) {
		started = cfg
		return nil, nil
	}
	t.Cleanup(func() { sqsmcpStart = sqsmcp.Start })
	return &started
}

func TestSQSListsOneTargetNamedAfterTheRegion(t *testing.T) {
	installSQSAWS(t, true)
	got, err := testRunner(t).ListResourceSummaries(t.Context(), &Options{ResourceType: "sqs", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	if err != nil || len(got) != 1 || got[0].Name != "eu-west-3" || got[0].Type != "sqs" {
		t.Fatalf("targets = %+v, %v", got, err)
	}
}

func TestSQSOpensWithWritesAndStartsTheServer(t *testing.T) {
	installSQSAWS(t, true)
	started := stubSQSMCP(t)
	session, err := testRunner(t).Prepare(t.Context(), Options{ResourceType: "sqs", Name: "eu-west-3", AwsProfile: "dev", Unattended: true, MCPAllowWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Release()
	if session.Target != "eu-west-3" || session.LocalPort != 0 || session.Details.ConnectedServer != "sqs.eu-west-3.amazonaws.com" {
		t.Fatalf("session = %+v", session)
	}
	if cfg := *started; cfg == nil || cfg.Region != "eu-west-3" || cfg.Profile != "dev" || !cfg.AllowWrites || cfg.AWS.Credentials == nil {
		t.Fatalf("mcp = %+v", cfg)
	}
}

func TestSQSRefusesNoMCPAndARegionMismatch(t *testing.T) {
	installSQSAWS(t, true)
	stubSQSMCP(t)
	for _, options := range []Options{{SkipMCP: true}, {Name: "eu-west-1", AwsRegion: "eu-west-3"}} {
		options.ResourceType, options.AwsProfile, options.Unattended = "sqs", "dev", true
		if options.AwsRegion == "" {
			options.AwsRegion = "eu-west-3"
		}
		if _, err := testRunner(t).Prepare(t.Context(), options); err == nil {
			t.Fatalf("Prepare(%+v) succeeded", options)
		}
	}
}

func TestSQSUnreachableNamesProfileAndRegion(t *testing.T) {
	installSQSAWS(t, false)
	_, err := testRunner(t).Prepare(t.Context(), Options{ResourceType: "sqs", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	if err == nil || !strings.Contains(err.Error(), "dev") || !strings.Contains(err.Error(), "eu-west-3") {
		t.Fatalf("err = %v", err)
	}
}
