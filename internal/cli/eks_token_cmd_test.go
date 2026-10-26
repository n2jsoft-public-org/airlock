package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft-public-org/airlock/internal/kubeconfig"
)

func TestEKSTokenCmdPrintsExecCredential(t *testing.T) {
	awsapitest.Env(t, func(http.ResponseWriter, *http.Request) {}, "dev")

	out, err := runAirlock(t, "eks-token", "--cluster-name", "platform", "--profile", "dev", "--region", "eu-west-3")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Status     struct {
			Token               string `json:"token"`
			ExpirationTimestamp string `json:"expirationTimestamp"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("out = %q, not JSON: %v", out, err)
	}
	if got.APIVersion != kubeconfig.ExecAPIVersion || got.Kind != "ExecCredential" ||
		!strings.HasPrefix(got.Status.Token, "k8s-aws-v1.") || got.Status.ExpirationTimestamp == "" {
		t.Fatalf("credential = %+v, want an ExecCredential holding a k8s-aws-v1 token", got)
	}
	if expires, err := time.Parse(time.RFC3339, got.Status.ExpirationTimestamp); err != nil || !expires.After(time.Now()) {
		t.Fatalf("expirationTimestamp = %q (%v), want a future RFC 3339 time", got.Status.ExpirationTimestamp, err)
	}
}
