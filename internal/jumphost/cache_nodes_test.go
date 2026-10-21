package jumphost

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// installFakeAWS puts on the PATH an aws that records its arguments in the returned file and runs script.
func installFakeAWS(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	body := "#!/bin/sh\necho \"$@\" > " + argsFile + "\n" + script
	// #nosec G306 -- the fake tool must be executable.
	if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return argsFile
}

func TestCacheNodeForwardOpensOneSSMForwardPerNode(t *testing.T) {
	argsFile := installFakeAWS(t, "echo 'Starting session'\necho 'Waiting for connections...'\nexec sleep 30\n")
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	var launched int
	launch := func(args []string, output func(string)) (Process, error) {
		launched++
		return AWSLauncher(args, output)
	}
	forward := runner.cacheNodeForward(launch, "i-jump", []string{"--profile", "dev"}, "ELASTICACHE orders (dev/eu-west-3)",
		"clustercfg.orders.example:6379", 16379)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// The endpoint is the session's own tunnel.
	if local, stop, _, err := forward(ctx, "clustercfg.orders.example:6379"); err != nil || local != "localhost:16379" {
		t.Fatalf("endpoint = %q, %v", local, err)
	} else {
		stop()
	}

	local, stop, done, err := forward(ctx, "10.0.1.5:6380")
	if err != nil {
		t.Fatal(err)
	}
	if launched != 1 {
		t.Fatalf("launched = %d, want the node tunnel started with the given launcher", launched)
	}
	defer stop()
	args, err := os.ReadFile(argsFile) // #nosec G304 -- the test's own file.
	if err != nil {
		t.Fatal(err)
	}
	_, localPort, _ := strings.Cut(local, ":")
	for _, want := range []string{
		"ssm start-session --target i-jump --document-name AWS-StartPortForwardingSessionToRemoteHost",
		`"host":["10.0.1.5"]`, `"portNumber":["6380"]`, `"localPortNumber":["` + localPort + `"]`, "--profile dev",
	} {
		if !strings.Contains(string(args), want) {
			t.Errorf("aws %s; want %s", args, want)
		}
	}
	if _, ok := runner.registry.ports[mustAtoi(t, localPort)]; !ok {
		t.Fatalf("local port %s is not reserved", localPort)
	}
	stop()
	if _, ok := runner.registry.ports[mustAtoi(t, localPort)]; ok {
		t.Fatalf("local port %s is still reserved after stop", localPort)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("done did not close once the tunnel was stopped")
	}
}

func TestCacheNodeForwardReportsATunnelEndingAfterListening(t *testing.T) {
	installFakeAWS(t, "echo 'Waiting for connections...'\nsleep 0.5\n")
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	forward := runner.cacheNodeForward(AWSLauncher, "i-jump", nil, "ELASTICACHE orders (dev/eu-west-3)", "clustercfg:6379", 16379)
	_, stop, done, err := forward(context.Background(), "10.0.1.5:6379")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("done did not close once aws exited")
	}
}

func TestCacheNodeForwardReportsATunnelEndingBeforeListening(t *testing.T) {
	installFakeAWS(t, "echo 'An error occurred (TargetNotConnected)' >&2\nexit 254\n")
	runner := NewRunner(&RunnerOptions{Out: io.Discard, Err: io.Discard})
	forward := runner.cacheNodeForward(AWSLauncher, "i-jump", nil, "ELASTICACHE orders (dev/eu-west-3)", "clustercfg:6379", 16379)
	_, _, _, err := forward(context.Background(), "10.0.1.5:6379")
	if err == nil || !strings.Contains(err.Error(), "10.0.1.5:6379") || !strings.Contains(err.Error(), "TargetNotConnected") {
		t.Fatalf("err = %v; want the node and the aws error", err)
	}
	if len(runner.registry.ports) != 0 {
		t.Fatalf("ports still reserved: %v", runner.registry.ports)
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
