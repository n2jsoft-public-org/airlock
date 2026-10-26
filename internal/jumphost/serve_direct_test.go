package jumphost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/cachemcp"
	"github.com/n2jsoft-public-org/airlock/internal/dbmcp"
)

// stubDirectSignIn makes the sign-in check of direct and Docker SQL sessions return err.
func stubDirectSignIn(t *testing.T, err error) {
	t.Helper()
	previous := checkDirectSignIn
	checkDirectSignIn = func(context.Context, dbmcp.Engine, dbmcp.CredentialProvider, dbmcp.Address) error { return err }
	t.Cleanup(func() { checkDirectSignIn = previous })
}

// stubCacheSignIn makes the sign-in check of Docker cache sessions return err.
func stubCacheSignIn(t *testing.T, err error) {
	t.Helper()
	previous := checkCacheSignIn
	checkCacheSignIn = func(context.Context, cachemcp.ConnConfig) error { return err }
	t.Cleanup(func() { checkCacheSignIn = previous })
}

func TestRunDockerRefusesARejectedPasswordBeforeServing(t *testing.T) {
	stubDirectMCP(t)
	stubDirectSignIn(t, fmt.Errorf("%w: password authentication failed", dbmcp.ErrSignInRejected))
	var out bytes.Buffer
	runner := directRunner(t, &RunnerOptions{Out: &out, Containers: fakeContainers{"pg": pgContainer()}})

	err := runner.Run(context.Background(), &Options{ResourceType: "docker", Name: "pg"})
	if err == nil || !errors.Is(err, ErrSignInRejected) || !strings.HasPrefix(err.Error(), "pg: ") {
		t.Fatalf("err = %v, want the rejection of pg", err)
	}
	if strings.Contains(out.String(), "Ctrl+C") {
		t.Fatalf("a rejected session must not be served:\n%s", out.String())
	}
	if claimErr := runner.registry.claim("DOCKER pg", "pg"); claimErr != nil {
		t.Fatalf("the session is still registered: %v", claimErr)
	}
}

func TestRunDockerCacheRefusesARejectedPasswordBeforeServing(t *testing.T) {
	stubCacheMCP(t)
	stubCacheSignIn(t, fmt.Errorf("%w: NOAUTH Authentication required", cachemcp.ErrSignInRejected))
	var out bytes.Buffer
	runner := directRunner(t, &RunnerOptions{Out: &out, Containers: fakeContainers{"cache": redisContainer()}})

	err := runner.Run(context.Background(), &Options{ResourceType: "docker", Name: "cache"})
	if err == nil || !errors.Is(err, ErrSignInRejected) || err.Error() != "cache: the cache rejected the credentials: NOAUTH Authentication required" ||
		strings.Contains(out.String(), "Ctrl+C") {
		t.Fatalf("err = %v, output:\n%s", err, out.String())
	}
	if claimErr := runner.registry.claim("DOCKER cache", "cache"); claimErr != nil {
		t.Fatalf("the session is still registered: %v", claimErr)
	}
}

func TestRunDirectReportsAnUnreachableServerBeforeServing(t *testing.T) {
	stubDirectMCP(t)
	stubDirectSignIn(t, errors.New("dial tcp: connection refused"))
	var out bytes.Buffer
	err := directRunner(t, &RunnerOptions{Out: &out}).Run(context.Background(), &Options{ResourceType: "direct", Name: "legacy-billing", DBPassword: "s3cret"})
	if err == nil || err.Error() != "legacy-billing: dial tcp: connection refused" || strings.Contains(out.String(), "Ctrl+C") {
		t.Fatalf("err = %v, output:\n%s", err, out.String())
	}
}

func TestRunDockerCacheServesAfterAPassingCheck(t *testing.T) {
	stubCacheMCP(t)
	stubCacheSignIn(t, nil)
	var out bytes.Buffer
	runner := directRunner(t, &RunnerOptions{Out: &out, Containers: fakeContainers{"cache": redisContainer()}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Ctrl+C right after the check.

	if err := runner.Run(ctx, &Options{ResourceType: "docker", Name: "cache"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "> Docker container cache (redis) at 127.0.0.1:6379") {
		t.Fatalf("output:\n%s", out.String())
	}
}
