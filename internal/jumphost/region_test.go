package jumphost

import (
	"context"
	"io"
	"os"
	"path/filepath"
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
	if got := runner.ResolveRegion(context.Background(), "prod"); got != defaultRegion {
		t.Fatalf("region = %q, want %s for a profile without region", got, defaultRegion)
	}
}
