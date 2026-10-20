package jumphost

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useProfiles points the AWS shared files at a config with profile dev (in region, when not empty)
// and profile prod (without region).
func useProfiles(t *testing.T, region string) {
	t.Helper()
	dir := t.TempDir()
	cfg := "[profile dev]\n"
	if region != "" {
		cfg += "region = " + region + "\n"
	}
	cfg += "\n[profile prod]\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
}

func TestResolveRegionPrefersEnvironment(t *testing.T) {
	useProfiles(t, "us-east-1")
	t.Setenv("AWS_REGION", "ap-northeast-1")

	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	if got := runner.ResolveRegion(context.Background(), "dev"); got != "ap-northeast-1" {
		t.Fatalf("region = %q, want AWS_REGION", got)
	}
}

func TestResolveRegionUsesProfileRegion(t *testing.T) {
	useProfiles(t, "us-east-1")
	t.Setenv("AWS_REGION", "")

	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	if got := runner.ResolveRegion(context.Background(), "dev"); got != "us-east-1" {
		t.Fatalf("region = %q, want the profile region", got)
	}
	if got := runner.ResolveRegion(context.Background(), "prod"); got != "" {
		t.Fatalf("region = %q, want none for a profile without region and no default", got)
	}
}

func TestResolveRegionFallsBackToConfiguredDefault(t *testing.T) {
	useProfiles(t, "us-east-1")
	t.Setenv("AWS_REGION", "")

	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard, DefaultRegion: "eu-west-3"})
	if got := runner.ResolveRegion(context.Background(), "prod"); got != "eu-west-3" {
		t.Fatalf("region = %q, want the configured default", got)
	}
	if got := runner.ResolveRegion(context.Background(), "dev"); got != "us-east-1" {
		t.Fatalf("region = %q, the profile region must win over the configured default", got)
	}
}

func TestResolveAWSConfigWithoutRegionErrors(t *testing.T) {
	useProfiles(t, "")
	t.Setenv("AWS_REGION", "")

	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	_, err := runner.resolveAWSConfig(context.Background(), &Options{AwsProfile: "prod"})
	if err == nil {
		t.Fatal("resolveAWSConfig without any region must fail")
	}
	for _, want := range []string{"--region", "`airlock config set aws.defaultRegion <region>`"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}
