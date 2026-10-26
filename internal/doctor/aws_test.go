package doctor

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
)

const (
	callerIdentityXML = `<GetCallerIdentityResponse><GetCallerIdentityResult><Arn>arn:aws:iam::111111111111:user/dev</Arn>` +
		`<Account>111111111111</Account><UserId>AIDATEST</UserId></GetCallerIdentityResult></GetCallerIdentityResponse>`
	expiredTokenXML = `<ErrorResponse><Error><Type>Sender</Type><Code>ExpiredToken</Code>` +
		`<Message>The security token included in the request is expired</Message></Error></ErrorResponse>`
	instanceXML = `<DescribeInstancesResponse><reservationSet><item><instancesSet><item><instanceId>i-0123</instanceId></item>` +
		`</instancesSet></item></reservationSet></DescribeInstancesResponse>`
	emptyInstancesXML = `<DescribeInstancesResponse><reservationSet/></DescribeInstancesResponse>`
	unauthorizedXML   = `<Response><Errors><Error><Code>UnauthorizedOperation</Code>` +
		`<Message>You are not authorized to perform this operation.</Message></Error></Errors><RequestID>r</RequestID></Response>`
)

// fakeAWS answers the identity check of profile dev with identity (status and body), and the
// jumphost lookup with instances.
func fakeAWS(t *testing.T, identityStatus int, identity string, instancesStatus int, instances string) {
	t.Helper()
	awsapitest.Env(t, func(w http.ResponseWriter, r *http.Request) {
		switch op := awsapitest.Operation(r); op {
		case "GetCallerIdentity":
			awsapitest.RespondXML(t, w, identityStatus, identity)
		case "DescribeInstances":
			awsapitest.RespondXML(t, w, instancesStatus, instances)
		default:
			t.Errorf("unexpected operation %s", op)
			w.WriteHeader(http.StatusBadRequest)
		}
	}, "dev")
}

func checkDev(t *testing.T, withJumphost bool) []Result {
	t.Helper()
	runner := jumphost.NewRunner(&jumphost.RunnerOptions{Out: io.Discard, Err: io.Discard})
	return Profiles(context.Background(), runner, []Profile{{Name: "dev", Region: "eu-west-3"}}, withJumphost, AWSTimeout)
}

func TestProfilesReportsTheAccountAndTheJumphost(t *testing.T) {
	fakeAWS(t, http.StatusOK, callerIdentityXML, http.StatusOK, instanceXML)
	results := checkDev(t, true)
	if len(results) != 2 || results[0].Status != OK || !strings.Contains(results[0].Message, "111111111111") ||
		results[1].Status != OK || !strings.Contains(results[1].Message, "i-0123") {
		t.Fatalf("results = %+v, want the account and the jumphost ok", results)
	}
}

func TestProfilesSkipsTheJumphostWithoutTunnels(t *testing.T) {
	fakeAWS(t, http.StatusOK, callerIdentityXML, http.StatusOK, instanceXML)
	if results := checkDev(t, false); len(results) != 1 {
		t.Fatalf("results = %+v, want the sign-in only", results)
	}
}

func TestProfilesWarnsOfAnExpiredSignInWithoutSigningIn(t *testing.T) {
	fakeAWS(t, http.StatusForbidden, expiredTokenXML, http.StatusOK, instanceXML)
	results := checkDev(t, true)
	if len(results) != 1 || results[0].Status != Warning || !strings.Contains(results[0].Fix, "aws sso login --profile dev") {
		t.Fatalf("results = %+v, want one expired sign-in warning", results)
	}
}

func TestProfilesWarnsOfAnMFAProfileWithoutACode(t *testing.T) {
	fakeAWS(t, http.StatusOK, callerIdentityXML, http.StatusOK, instanceXML)
	mfa := "\n[profile mfa]\nregion = eu-west-3\nsource_profile = dev\nrole_arn = arn:aws:iam::111111111111:role/app\nmfa_serial = arn:aws:iam::111111111111:mfa/dev\n" +
		"\n[profile prod]\nregion = eu-west-3\nsource_profile = mfa\nrole_arn = arn:aws:iam::111111111111:role/prod\n"
	f, err := os.OpenFile(os.Getenv("AWS_CONFIG_FILE"), os.O_APPEND|os.O_WRONLY, 0) // #nosec G703 -- the test's own config file.
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(mfa); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	runner := jumphost.NewRunner(&jumphost.RunnerOptions{Out: io.Discard, Err: io.Discard})
	// prod chains a role above mfa: its code is the one of mfa.
	for _, profile := range []string{"mfa", "prod"} {
		results := Profiles(context.Background(), runner, []Profile{{Name: profile, Region: "eu-west-3"}}, true, AWSTimeout)
		if len(results) != 1 || results[0].Status != Warning || !strings.Contains(results[0].Fix, "sso-login --profile "+profile) {
			t.Fatalf("%s: results = %+v, want one warning telling how to type the MFA code", profile, results)
		}
	}
}

func TestProfilesFailsWhenAWSRefusesTheConfiguration(t *testing.T) {
	fakeAWS(t, http.StatusForbidden, `<ErrorResponse><Error><Type>Sender</Type><Code>InvalidClientTokenId</Code>`+
		`<Message>The security token included in the request is invalid.</Message></Error></ErrorResponse>`, http.StatusOK, instanceXML)
	results := checkDev(t, true)
	if len(results) != 1 || results[0].Status != Failure || !strings.Contains(results[0].Message, "InvalidClientTokenId") ||
		!strings.Contains(results[0].Fix, "check profile dev") {
		t.Fatalf("results = %+v, want a configuration failure", results)
	}
}

func TestProfilesNamesTheMissingIAMPermission(t *testing.T) {
	fakeAWS(t, http.StatusOK, callerIdentityXML, http.StatusForbidden, unauthorizedXML)
	results := checkDev(t, true)
	if len(results) != 2 || results[1].Status != Warning || !strings.Contains(results[1].Message, "missing IAM permission ec2:DescribeInstances") {
		t.Fatalf("results = %+v, want the missing permission", results)
	}
}

func TestProfilesWarnsWithoutRunningJumphost(t *testing.T) {
	fakeAWS(t, http.StatusOK, callerIdentityXML, http.StatusOK, emptyInstancesXML)
	results := checkDev(t, true)
	if len(results) != 2 || results[1].Status != Warning || !strings.Contains(results[1].Message, "Usage=jumphost") || results[1].Fix == "" {
		t.Fatalf("results = %+v, want no jumphost with a fix", results)
	}
}

func TestProfilesFailsWhenAWSIsUnreachable(t *testing.T) {
	srv := awsapitest.Env(t, func(http.ResponseWriter, *http.Request) {}, "dev")
	srv.Close()
	results := checkDev(t, true)
	if len(results) != 1 || results[0].Status != Failure || !strings.Contains(results[0].Fix, "network") {
		t.Fatalf("results = %+v, want AWS out of reach", results)
	}
}

func TestProfilesTimesOutEachProfile(t *testing.T) {
	release := make(chan struct{})
	srv := awsapitest.Env(t, func(http.ResponseWriter, *http.Request) { <-release }, "dev")
	t.Cleanup(func() { close(release); srv.Close() })
	runner := jumphost.NewRunner(&jumphost.RunnerOptions{Out: io.Discard, Err: io.Discard})
	results := Profiles(context.Background(), runner, []Profile{{Name: "dev", Region: "eu-west-3"}}, true, 50*time.Millisecond)
	if len(results) != 1 || results[0].Status != Failure || !strings.Contains(results[0].Message, "50ms") {
		t.Fatalf("results = %+v, want a timeout", results)
	}
}

func TestProfilesWarnsOfAProfileWithoutRegion(t *testing.T) {
	results := Profiles(context.Background(), nil, []Profile{{Name: "dev"}}, true, AWSTimeout)
	if len(results) != 1 || results[0].Status != Warning || results[0].Subject != "dev" {
		t.Fatalf("results = %+v, want a missing region warning", results)
	}
}
