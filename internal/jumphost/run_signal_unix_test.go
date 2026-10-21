//go:build unix

package jumphost

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/awsapi/awsapitest"
	"github.com/n2jsoft/airlock/internal/platform"
)

// lockedBuffer is a bytes.Buffer the test reads while Run writes it.
type lockedBuffer struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRunStopsAWSOnSignals(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			installAWSAPI(t, func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected operation %s", awsapitest.Operation(r))
				w.WriteHeader(http.StatusBadRequest)
			})
			installTunnelTools(t)
			pidFile := filepath.Join(t.TempDir(), "pid")
			// #nosec G306 G703 -- the fake tool, in the test's own PATH directory, must be executable.
			if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), "aws"), []byte("#!/bin/sh\necho $$ > "+pidFile+"\nexec /bin/sleep 30\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			runner := NewRunner(&RunnerOptions{Out: &lockedBuffer{}, Err: io.Discard, ProjectDir: t.TempDir()})
			ended := make(chan error, 1)
			go func() {
				ended <- runner.Run(context.Background(), &Options{ResourceType: "forward", Name: "search.internal:9200", AwsProfile: "dev", AwsRegion: "eu-west-3"})
			}()
			var pid int
			for deadline := time.Now().Add(5 * time.Second); pid == 0; time.Sleep(10 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatal("aws did not start")
				}
				data, err := os.ReadFile(pidFile) // #nosec G304 -- the test's own file.
				if err == nil {
					pid, _ = strconv.Atoi(strings.TrimSpace(string(data))) //nolint:errcheck // 0 until written
				}
			}
			// Run listens to the signal from its start: the test process survives it.
			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-ended:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Run did not end on the signal")
			}
			if platform.ProcessAlive(pid) {
				t.Fatalf("aws (pid %d) still runs", pid)
			}
			if len(runner.registry.ports) != 0 {
				t.Fatalf("ports still reserved: %v", runner.registry.ports)
			}
		})
	}
}
