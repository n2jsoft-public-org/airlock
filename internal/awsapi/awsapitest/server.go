// Package awsapitest points the AWS SDK at a local httptest server.
package awsapitest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Env starts a server running handler and configures the environment so SDK
// clients loaded for any of profiles use static credentials against it.
// It uses t.Setenv, so the calling test cannot be parallel.
func Env(t testing.TB, handler http.HandlerFunc, profiles ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	var cfg, creds strings.Builder
	for _, p := range profiles {
		_, _ = fmt.Fprintf(&cfg, "[profile %s]\nregion = eu-west-3\n\n", p)
		_, _ = fmt.Fprintf(&creds, "[%s]\naws_access_key_id = AKIDTEST\naws_secret_access_key = secret\n\n", p)
	}
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Setenv("AWS_CONFIG_FILE", write("config", cfg.String()))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", write("credentials", creds.String()))
	t.Setenv("AWS_ENDPOINT_URL", srv.URL)
	// The SDK builds the STS (assume-role) and SSO clients of credential providers before it reads
	// AWS_ENDPOINT_URL: only the service variables point them at the server.
	for _, name := range []string{"AWS_ENDPOINT_URL_STS", "AWS_ENDPOINT_URL_SSO", "AWS_ENDPOINT_URL_SSO_OIDC"} {
		t.Setenv(name, srv.URL)
	}
	t.Setenv("AWS_MAX_ATTEMPTS", "1")
	// Hermetic: a developer's shell or CI credentials must never reach a test.
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION",
		"AWS_IGNORE_CONFIGURED_ENDPOINT_URLS", "AWS_CA_BUNDLE", "AWS_ENDPOINT_URL_DYNAMODB",
		"AWS_ENDPOINT_URL_S3", "AWS_ENDPOINT_URL_SECRETS_MANAGER", "AWS_ENDPOINT_URL_ELASTICACHE",
		"AWS_ENDPOINT_URL_EC2", "AWS_ENDPOINT_URL_RDS", "AWS_ENDPOINT_URL_EKS"} {
		t.Setenv(name, "")
	}
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	return srv
}

// Operation names the AWS operation of r: the X-Amz-Target suffix (JSON
// protocols), the Action form value (query protocols), else method and path.
func Operation(r *http.Request) string {
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		return target[strings.LastIndex(target, ".")+1:]
	}
	if action := r.FormValue("Action"); action != "" {
		return action
	}
	return r.Method + " " + r.URL.Path
}

// Respond writes the JSON reply of a fake AWS API.
func Respond(t testing.TB, w http.ResponseWriter, status int, body any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Error(err)
		return
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	if _, err := w.Write(raw); err != nil {
		t.Error(err)
	}
}

// RespondXML writes the XML reply of a fake AWS API speaking a query protocol (STS, EC2…).
func RespondXML(t testing.TB, w http.ResponseWriter, status int, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	if _, err := w.Write([]byte(body)); err != nil {
		t.Error(err)
	}
}

// DropConnection answers every request by closing its connection before any response, like a
// network dropping it.
func DropConnection(t testing.TB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Unread request bytes would make the close a reset (a net.OpError): the client must see EOF.
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	}
}
