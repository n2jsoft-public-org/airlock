package jumphost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
)

func TestForwardTarget(t *testing.T) {
	valid := map[[2]string]string{
		{"vpc-search.eu-west-3.es.amazonaws.com", "443"}: "vpc-search.eu-west-3.es.amazonaws.com:443",
		{" 10.0.1.12 ", "9092"}:                          "10.0.1.12:9092",
		{"fd00::1", "27017"}:                             "[fd00::1]:27017",
		{"[fd00::1]", "65535"}:                           "[fd00::1]:65535",
	}
	for in, want := range valid {
		if got, err := ForwardTarget(in[0], in[1]); err != nil || got != want {
			t.Errorf("ForwardTarget(%q, %q) = %q, %v; want %q", in[0], in[1], got, err, want)
		}
	}
	for _, in := range [][2]string{
		{"", "443"}, {"https://search.internal", "443"}, {"search.internal/path", "443"}, {"search.internal:443", "443"},
		{"user@search.internal", "443"}, {"search internal", "443"}, {"search.internal", "0"}, {"search.internal", "65536"},
		{"search.internal", "https"},
	} {
		if got, err := ForwardTarget(in[0], in[1]); err == nil {
			t.Errorf("ForwardTarget(%q, %q) = %q, want an error", in[0], in[1], got)
		}
	}
}

func TestPrepareForwardTunnelsToTheHostThroughTheJumphost(t *testing.T) {
	installAWSAPI(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected operation %s", awsapitest.Operation(r))
		w.WriteHeader(http.StatusBadRequest)
	})
	installTunnelTools(t)
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, ProjectDir: t.TempDir()})
	options := Options{ResourceType: "forward", Name: "search.internal:9200", AwsProfile: "dev", AwsRegion: "eu-west-3"}

	resources, err := runner.ListResourceSummaries(context.Background(), &options)
	if err != nil || len(resources) != 1 || resources[0].Name != options.Name || resources[0].Type != "forward" {
		t.Fatalf("ListResourceSummaries = %+v, %v; want the target itself", resources, err)
	}
	if port, portErr := runner.ResolveDefaultLocalPort(context.Background(), options); portErr != nil || port < 9200 {
		t.Fatalf("ResolveDefaultLocalPort = %d, %v; want the remote port or the next free one", port, portErr)
	}

	session, err := runner.Prepare(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Release() })
	var params map[string][]string
	if err := json.Unmarshal([]byte(session.CommandArgs[len(session.CommandArgs)-1]), &params); err != nil {
		t.Fatal(err)
	}
	local := strconv.Itoa(session.LocalPort)
	if params["host"][0] != "search.internal" || params["portNumber"][0] != "9200" || params["localPortNumber"][0] != local {
		t.Fatalf("SSM parameters = %v", params)
	}
	if want := "airlock forward search.internal 9200 " + local + " --profile dev --region eu-west-3"; session.Details.RestartCommand != want {
		t.Fatalf("restart command = %q, want %q", session.Details.RestartCommand, want)
	}
	if session.ResourceType != "forward" || session.Details.ConnectedServer != "search.internal:9200" ||
		session.MCPServer() != nil || session.Details.IDEDataSource != "" || session.SignIn != nil {
		t.Fatalf("session = %+v, want a plain tunnel without MCP server, IDE data source nor sign-in", session)
	}

	options.Name = "https://search.internal:9200"
	if _, err := runner.Prepare(context.Background(), options); err == nil {
		t.Fatal("Prepare accepted a target with a scheme")
	}
}

func TestForwardIsRememberedByAccountRegionHostAndPort(t *testing.T) {
	installAWSAPI(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected operation %s", awsapitest.Operation(r))
		w.WriteHeader(http.StatusBadRequest)
	})
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, PrefsPath: filepath.Join(t.TempDir(), "saved-connections.json")})
	options := Options{ResourceType: "forward", Name: "10.0.1.12:9092", AwsProfile: "dev", AwsRegion: "eu-west-3", LocalPortNumber: 19092}
	if err := runner.SaveConnectionPrefs(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	saved, err := runner.ListSavedConnections(context.Background(), "", "")
	if err != nil || len(saved) != 1 {
		t.Fatalf("ListSavedConnections = %+v, %v; want the forward", saved, err)
	}
	if ref, prefs := saved[0].Ref, saved[0].Prefs; ref.Type != "forward" || ref.Name != "10.0.1.12:9092" || ref.Region != "eu-west-3" ||
		ref.Account == "" || prefs.Profile != "dev" || prefs.LocalPort != 19092 {
		t.Fatalf("saved = %+v", saved[0])
	}
}
