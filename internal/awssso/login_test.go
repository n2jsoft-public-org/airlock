package awssso

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
)

const startURL = "https://example.awsapps.com/start"

// tokenSteps answers CreateToken with each of steps in turn (the last one repeats), and the other
// calls of the device flow with canned replies.
func tokenSteps(t *testing.T, steps ...func(w http.ResponseWriter)) http.HandlerFunc {
	var calls atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) {
		switch awsapitest.Operation(r) {
		case "POST /client/register":
			awsapitest.Respond(t, w, 200, map[string]any{"clientId": "cid", "clientSecret": "csecret",
				"clientIdIssuedAt": 1, "clientSecretExpiresAt": time.Now().Add(24 * time.Hour).Unix()})
		case "POST /device_authorization":
			awsapitest.Respond(t, w, 200, map[string]any{"deviceCode": "dev", "userCode": "ABCD-EFGH", "interval": 1,
				"expiresIn": 600, "verificationUri": "https://device", "verificationUriComplete": "https://device?code=ABCD-EFGH"})
		case "POST /token":
			i := int(calls.Add(1)) - 1
			steps[min(i, len(steps)-1)](w)
		default:
			t.Errorf("unexpected call %s", awsapitest.Operation(r))
		}
	}
}

func tokenError(t *testing.T, errType, code string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("X-Amzn-Errortype", errType)
		awsapitest.Respond(t, w, 400, map[string]string{"error": code, "error_description": code})
	}
}

func tokenOK(t *testing.T) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		awsapitest.Respond(t, w, 200, map[string]any{"accessToken": "access-1", "refreshToken": "refresh-1", "expiresIn": 3600, "tokenType": "Bearer"})
	}
}

// setup points the AWS files and HOME at a temp dir holding a profile built from lines.
func setup(t *testing.T, handler http.HandlerFunc, lines string) {
	t.Helper()
	awsapitest.Env(t, handler)
	cfg := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(cfg, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))
	noWait(t)
}

func noWait(t *testing.T) *[]time.Duration {
	var waits []time.Duration
	old := wait
	wait = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	t.Cleanup(func() { wait = old })
	return &waits
}

const ssoSessionProfile = "[profile dev]\nsso_session = corp\nsso_account_id = 123456789012\nsso_role_name = Admin\nregion = eu-west-3\n\n" +
	"[sso-session corp]\nsso_start_url = " + startURL + "\nsso_region = eu-west-3\nsso_registration_scopes = sso:account:access\n"

func TestLoginWritesATokenTheSDKReads(t *testing.T) {
	setup(t, tokenSteps(t, tokenError(t, "AuthorizationPendingException", "authorization_pending"), tokenOK(t)), ssoSessionProfile)

	var gotURL, gotCode string
	if err := Login(t.Context(), "dev", func(u, c string) { gotURL, gotCode = u, c }); err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://device?code=ABCD-EFGH" || gotCode != "ABCD-EFGH" {
		t.Errorf("prompt got %q %q", gotURL, gotCode)
	}

	path, err := ssocreds.StandardCachedTokenFilepath("corp")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("cache mode = %v, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the SDK's cache path under a temp HOME.
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]string
	if err = json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"startUrl", "region", "accessToken", "expiresAt", "clientId", "clientSecret", "registrationExpiresAt", "refreshToken"} {
		if file[key] == "" {
			t.Errorf("cache file lacks %s: %s", key, raw)
		}
	}
	if !strings.HasSuffix(file["expiresAt"], "Z") || strings.Contains(file["expiresAt"], ".") {
		t.Errorf("expiresAt = %q, want RFC 3339 UTC without fractions", file["expiresAt"])
	}

	cfg, err := config.LoadDefaultConfig(t.Context(), config.WithSharedConfigProfile("dev"), config.WithCredentialsProvider(aws.AnonymousCredentials{}))
	if err != nil {
		t.Fatal(err)
	}
	token, err := ssocreds.NewSSOTokenProvider(ssooidc.NewFromConfig(cfg), path).RetrieveBearerToken(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if token.Value != "access-1" {
		t.Errorf("token = %q, want access-1", token.Value)
	}
}

func TestLoginLegacyProfileKeysByStartURL(t *testing.T) {
	setup(t, tokenSteps(t, tokenOK(t)), "[profile old]\nsso_start_url = "+startURL+"\nsso_region = eu-west-3\nsso_account_id = 1\nsso_role_name = r\n")

	if err := Login(t.Context(), "old", func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	path, err := ssocreds.StandardCachedTokenFilepath(startURL)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the SDK's cache path under a temp HOME.
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if _, ok := file["refreshToken"]; ok {
		t.Errorf("a legacy cache file has no refreshToken: %s", raw)
	}
	if file["accessToken"] != "access-1" || file["startUrl"] != startURL {
		t.Errorf("cache file = %s", raw)
	}
}

func TestLoginSlowsDownWhenAsked(t *testing.T) {
	setup(t, tokenSteps(t, tokenError(t, "SlowDownException", "slow_down"), tokenOK(t)), ssoSessionProfile)
	waits := noWait(t)

	if err := Login(t.Context(), "dev", func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{time.Second, 6 * time.Second}; len(*waits) != 2 || (*waits)[0] != want[0] || (*waits)[1] != want[1] {
		t.Errorf("waits = %v, want %v", *waits, want)
	}
}

func TestLoginRejectsANonSSOProfile(t *testing.T) {
	setup(t, tokenSteps(t, tokenOK(t)), "[profile plain]\nregion = eu-west-3\n")

	err := Login(t.Context(), "plain", func(string, string) { t.Error("must not prompt") })
	if err == nil || !strings.Contains(err.Error(), "plain") {
		t.Errorf("err = %v, want one naming the profile", err)
	}
}

func TestLoginStopsOnExpiredDeviceCode(t *testing.T) {
	setup(t, tokenSteps(t, tokenError(t, "ExpiredTokenException", "expired_token")), ssoSessionProfile)

	if err := Login(t.Context(), "dev", func(string, string) {}); err == nil {
		t.Fatal("want an error")
	}
	path, err := ssocreds.StandardCachedTokenFilepath("corp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("no token must be written")
	}
}
