package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// startTimeout is how long Ensure waits for a daemon it started.
const startTimeout = 5 * time.Second

// dialTimeout bounds the connection and handshake with a daemon.
const dialTimeout = 2 * time.Second

// StartFunc starts the daemon process (internal/cli runs `airlock daemon run` detached).
type StartFunc func() error

// Running returns a client of the daemon, or nil, nil when no daemon answers.
// A daemon that accepts but does not answer within dialTimeout counts as none answering. A socket
// directory of another user is an error: its daemon is not this user's.
func Running(ctx context.Context, paths Paths, version string) (*Client, error) {
	if err := checkSocketDir(paths.Dir); err != nil {
		return nil, err
	}
	type dialed struct {
		client *Client
		err    error
	}
	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan dialed, 1)
	go func() {
		client, err := Dial(dialCtx, paths.Socket, version)
		result <- dialed{client, err}
	}()
	var client *Client
	var err error
	select {
	case d := <-result:
		client, err = d.client, d.err
	case <-time.After(dialTimeout):
		// Dial waits a while for the reply of a canceled call: do not wait for it, and close
		// the client if it comes up after all.
		cancel()
		go func() {
			if d := <-result; d.client != nil {
				discard(d.client.Close())
			}
		}()
		return nil, nil
	}
	var mismatch *VersionMismatchError
	switch {
	case err == nil, errors.As(err, &mismatch):
		return client, err
	default:
		return nil, nil
	}
}

// Ensure returns a client of the daemon, starting it first when none answers. logPath, when
// set, is quoted when the daemon does not come up.
func Ensure(ctx context.Context, paths Paths, version string, start StartFunc, logPath string) (*Client, error) {
	if client, err := Running(ctx, paths, version); client != nil || err != nil {
		return client, err
	}
	if err := start(); err != nil {
		return nil, fmt.Errorf("failed to start the airlock daemon: %w", err)
	}
	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		if client, err := Running(ctx, paths, version); client != nil || err != nil {
			return client, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("the airlock daemon did not start%s (use --mode cli to run without it)", logTail(logPath))
}

// logTail quotes the end of the daemon log.
func logTail(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path) // #nosec G304 -- airlock's own log.
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	return "; " + path + " ends with:\n  " + strings.Join(lines, "\n  ") + "\n"
}
