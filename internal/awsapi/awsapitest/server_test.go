package awsapitest_test

import (
	"net/http"
	"os"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/awsapi/awsapitest"
)

func TestEnvBlanksTheServiceEndpointsOfTheShell(t *testing.T) {
	names := []string{"AWS_ENDPOINT_URL_EC2", "AWS_ENDPOINT_URL_RDS", "AWS_ENDPOINT_URL_EKS"}
	for _, name := range names {
		t.Setenv(name, "https://example.invalid")
	}
	awsapitest.Env(t, func(http.ResponseWriter, *http.Request) {}, "dev")
	for _, name := range names {
		if got := os.Getenv(name); got != "" {
			t.Errorf("%s = %q, want it blank so the shell cannot redirect tests", name, got)
		}
	}
}
