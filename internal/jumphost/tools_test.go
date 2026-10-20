package jumphost

import (
	"net/http"
	"testing"

	"github.com/n2jsoft/airlock/internal/i18n"
)

func TestListAWSProfileSummariesWithoutTheAWSCLI(t *testing.T) {
	installAWSAPI(t, func(http.ResponseWriter, *http.Request) {})
	got, err := testRunner(t).ListAWSProfileSummaries(t.Context())
	if err != nil || len(got) != 1 || got[0].Name != "dev" || got[0].Description != "region eu-west-3" {
		t.Fatalf("profiles = %+v, %v; want dev in eu-west-3", got, err)
	}
}

// failAWS fails the test on any call to AWS: the missing tunnel tools are checked first.
func failAWS(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected AWS call %s", r.URL)
		w.WriteHeader(http.StatusBadRequest)
	}
}

func TestPrepareTunnelRequiresTheAWSCLI(t *testing.T) {
	installAWSAPI(t, failAWS(t))
	installTunnelTools(t, "session-manager-plugin")
	_, err := testRunner(t).Prepare(t.Context(), Options{ResourceType: "rds", Name: "orders", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	if want := i18n.NewError("jumphost.runner.aws_cli_is_not").Error(); err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestPrepareTunnelRequiresTheSessionManagerPlugin(t *testing.T) {
	installAWSAPI(t, failAWS(t))
	installTunnelTools(t, "aws")
	_, err := testRunner(t).Prepare(t.Context(), Options{ResourceType: "rds", Name: "orders", AwsProfile: "dev", AwsRegion: "eu-west-3", Unattended: true})
	if want := i18n.NewError("jumphost.runner.session_manager_plugin_is_not").Error(); err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
