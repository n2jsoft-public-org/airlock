package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft/airlock/internal/doctor"
	"github.com/n2jsoft/airlock/internal/keychain"
)

func TestDoctorPrintsJSONAndFailsWithoutTunnelTools(t *testing.T) {
	awsapitest.Env(t, func(w http.ResponseWriter, r *http.Request) {
		switch op := awsapitest.Operation(r); op {
		case "GetCallerIdentity":
			awsapitest.RespondXML(t, w, http.StatusOK, `<GetCallerIdentityResponse><GetCallerIdentityResult>`+
				`<Arn>arn:aws:iam::111111111111:user/dev</Arn><Account>111111111111</Account><UserId>AIDATEST</UserId>`+
				`</GetCallerIdentityResult></GetCallerIdentityResponse>`)
		case "DescribeInstances":
			awsapitest.RespondXML(t, w, http.StatusOK, `<DescribeInstancesResponse><reservationSet><item><instancesSet><item>`+
				`<instanceId>i-0123</instanceId></item></instancesSet></item></reservationSet></DescribeInstancesResponse>`)
		default:
			t.Errorf("unexpected operation %s", op)
			w.WriteHeader(http.StatusBadRequest)
		}
	}, "dev", "other")
	testConfigFile(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Setenv(noUpdateCheckEnv, "1")
	previous := newKeychain
	newKeychain = func() keychain.Store { return keychain.NewMemoryStore() }
	t.Cleanup(func() { newKeychain = previous })

	out, err := runAirlock(t, "doctor", "--json", "--profile", "dev")
	if exit, ok := errors.AsType[*ExitCodeError](err); !ok || exit.Code != 1 {
		t.Fatalf("err = %v, want exit status 1 (no AWS CLI)", err)
	}
	var results []doctor.Result
	if err = json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	statuses := map[string]doctor.Status{}
	for _, r := range results {
		if r.Subject == "other" {
			t.Errorf("--profile dev checked %+v", r)
		}
		if statuses[r.Check] != doctor.Failure {
			statuses[r.Check] = r.Status
		}
	}
	want := map[string]doctor.Status{
		"tunnel-tools": doctor.Failure, "aws-profile": doctor.OK, "jumphost": doctor.OK,
		"config": doctor.OK, "daemon": doctor.OK, "keychain": doctor.OK, "version": doctor.OK,
	}
	for check, status := range want {
		if statuses[check] != status {
			t.Errorf("%s = %q, want %q (results %+v)", check, statuses[check], status, results)
		}
	}
}
