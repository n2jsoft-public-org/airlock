package jumphost

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installFakeAWS puts an aws executable on PATH that answers "configure get region" with region.
func installFakeAWS(t *testing.T, region string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n[ \"$1 $2 $3\" = \"configure get region\" ] && [ \"$5\" = \"dev\" ] && printf '%s\\n' '" + region + "'\nexit 0\n"
	// #nosec G306 -- the fake aws must be executable.
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestResolveRegionPrefersEnvironment(t *testing.T) {
	installFakeAWS(t, "us-east-1")
	t.Setenv("AWS_REGION", "ap-northeast-1")

	runner := NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard})
	if got := runner.ResolveRegion(context.Background(), "dev"); got != "ap-northeast-1" {
		t.Fatalf("region = %q, want AWS_REGION", got)
	}
}

func TestResolveRegionUsesProfileRegion(t *testing.T) {
	installFakeAWS(t, "us-east-1")
	t.Setenv("AWS_REGION", "")

	runner := NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard})
	if got := runner.ResolveRegion(context.Background(), "dev"); got != "us-east-1" {
		t.Fatalf("region = %q, want the profile region", got)
	}
	if got := runner.ResolveRegion(context.Background(), "prod"); got != "" {
		t.Fatalf("region = %q, want none for a profile without region and no default", got)
	}
}

func TestResolveRegionFallsBackToConfiguredDefault(t *testing.T) {
	installFakeAWS(t, "us-east-1")
	t.Setenv("AWS_REGION", "")

	runner := NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard, DefaultRegion: "eu-west-3"})
	if got := runner.ResolveRegion(context.Background(), "prod"); got != "eu-west-3" {
		t.Fatalf("region = %q, want the configured default", got)
	}
	if got := runner.ResolveRegion(context.Background(), "dev"); got != "us-east-1" {
		t.Fatalf("region = %q, the profile region must win over the configured default", got)
	}
}

func TestResolveAWSConfigWithoutRegionErrors(t *testing.T) {
	installFakeAWS(t, "")
	t.Setenv("AWS_REGION", "")

	runner := NewRunner(RunnerOptions{Out: io.Discard, Err: io.Discard, ConfigPath: "/cfg/airlock/config.json"})
	_, err := runner.resolveAWSConfig(context.Background(), Options{AwsProfile: "prod"})
	if err == nil {
		t.Fatal("resolveAWSConfig without any region must fail")
	}
	for _, want := range []string{"--region", "defaultRegion", "/cfg/airlock/config.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}
