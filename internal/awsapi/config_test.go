package awsapi_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/n2jsoft/airlock/internal/awsapi"
	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
)

func noop(http.ResponseWriter, *http.Request) {}

func TestConfigsGetUsesProfileAndRegion(t *testing.T) {
	awsapitest.Env(t, noop, "dev")
	cfg, err := awsapi.NewConfigs().Get(context.Background(), "dev", "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Region != "us-east-1" {
		t.Fatalf("region = %q", cfg.Region)
	}
	creds, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil || creds.AccessKeyID != "AKIDTEST" {
		t.Fatalf("creds = %+v, err = %v", creds, err)
	}
}

func TestConfigsGetCachesPerProfileAndRegion(t *testing.T) {
	awsapitest.Env(t, noop, "dev")
	c := awsapi.NewConfigs()
	ctx := context.Background()
	get := func(region string) aws.Config {
		cfg, err := c.Get(ctx, "dev", region)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	a, b, other := get("us-east-1"), get("us-east-1"), get("eu-west-1")
	if a.Credentials != b.Credentials {
		t.Fatal("same profile and region must share credentials")
	}
	if a.Credentials == other.Credentials {
		t.Fatal("another region must not share credentials")
	}
}

func TestConfigsGetUnknownProfileFails(t *testing.T) {
	awsapitest.Env(t, noop, "dev")
	c := awsapi.NewConfigs()
	for range 2 {
		if _, err := c.Get(context.Background(), "nope", ""); err == nil {
			t.Fatal("want error for unknown profile")
		}
	}
}

func TestEnvRoutesSDKRequestsToServer(t *testing.T) {
	var op string
	awsapitest.Env(t, func(w http.ResponseWriter, r *http.Request) {
		op = awsapitest.Operation(r)
		w.Header().Set("Content-Type", "text/xml")
		if _, err := w.Write([]byte(`<GetCallerIdentityResponse><GetCallerIdentityResult><Account>1</Account></GetCallerIdentityResult></GetCallerIdentityResponse>`)); err != nil {
			t.Error(err)
		}
	}, "dev")
	cfg, err := awsapi.NewConfigs().Get(context.Background(), "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sts.NewFromConfig(cfg).GetCallerIdentity(context.Background(), &sts.GetCallerIdentityInput{}); err != nil {
		t.Fatal(err)
	}
	if op != "GetCallerIdentity" {
		t.Fatalf("operation = %q", op)
	}
}

func TestConfigsGetReloadsWhenTheSharedFilesChange(t *testing.T) {
	awsapitest.Env(t, noop, "dev")
	c := awsapi.NewConfigs()
	ctx := context.Background()
	key := func() (aws.Config, string) {
		cfg, err := c.Get(ctx, "dev", "")
		if err != nil {
			t.Fatal(err)
		}
		creds, err := cfg.Credentials.Retrieve(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return cfg, creds.AccessKeyID
	}
	first, id := key()
	if again, _ := key(); again.Credentials != first.Credentials || id != "AKIDTEST" {
		t.Fatal("unchanged files must keep the cached config")
	}

	path := os.Getenv("AWS_SHARED_CREDENTIALS_FILE")
	// #nosec G703 -- a temp file of the test.
	if err := os.WriteFile(path, []byte("[dev]\naws_access_key_id = AKIDNEW\naws_secret_access_key = secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if _, id = key(); id != "AKIDNEW" {
		t.Fatalf("access key = %q, want the rewritten credentials", id)
	}
}
