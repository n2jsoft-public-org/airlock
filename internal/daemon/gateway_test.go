package daemon

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
	"github.com/n2jsoft-public-org/airlock/internal/sessions"
	"github.com/n2jsoft-public-org/airlock/internal/sessions/sessionstest"
)

type fakeGateway struct {
	onClose func()
	closed  chan struct{}
	status  string
	// logged is StatusWithoutToken; status when empty.
	logged string
	// unregistered tells that Unregister was called (an explicit stop).
	unregistered atomic.Bool
}

func (g *fakeGateway) Status() string { return g.status }
func (g *fakeGateway) StatusWithoutToken() string {
	if g.logged != "" {
		return g.logged
	}
	return g.status
}
func (g *fakeGateway) Unregister() error {
	g.unregistered.Store(true)
	return nil
}
func (g *fakeGateway) Close() error {
	if g.onClose != nil {
		g.onClose()
	}
	close(g.closed)
	return nil
}

// runDaemonWith runs a daemon with gateway; it returns the env the gateway got and Run's error.
func runDaemonWith(t *testing.T, paths Paths, idle time.Duration, gateway StartGatewayFunc, targets ...string) (envs <-chan GatewayEnv, errs <-chan error, cancel context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	gotEnv := make(chan GatewayEnv, 1)
	result := make(chan error, 1)
	finished := make(chan struct{})
	runner := sessionstest.NewRunner()
	for _, name := range targets {
		runner.Direct(name)
	}
	go func() {
		defer close(finished)
		result <- Run(ctx, &RunConfig{Paths: paths, Version: "v-test", Runner: sessions.Fixed(runner),
			Launch: sessionstest.NewLauncher().Launch, IdleTimeout: idle,
			Gateway: func(ctx context.Context, env GatewayEnv) (Gateway, error) {
				gotEnv <- env
				return gateway(ctx, env)
			}})
	}()
	waitSocket(t, paths)
	t.Cleanup(func() { cancel(); <-finished })
	return gotEnv, result, cancel
}

func TestGatewayStatusAndCloseOnStop(t *testing.T) {
	paths := testPaths(t)
	gw := &fakeGateway{status: "on http://127.0.0.1:7821/mcp", closed: make(chan struct{})}
	envs, errs, cancel := runDaemonWith(t, paths, time.Hour, func(context.Context, GatewayEnv) (Gateway, error) { return gw, nil })
	env := <-envs
	client, err := Dial(t.Context(), paths.Socket, "v-test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { ignore(client.Close()) }()
	status, err := client.Status(t.Context())
	if err != nil || status.Gateway != gw.status {
		t.Fatalf("status = %+v, %v", status, err)
	}
	if _, _, err := client.Subscribe(t.Context()); err != nil {
		t.Fatal(err)
	}
	for env.Manager.Views() != 1 {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-gw.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the gateway must be closed when the daemon stops")
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if gw.unregistered.Load() {
		t.Fatal("a daemon stopping on its own (idle, signal) must keep the gateway entry")
	}
}

func TestGatewayStartFailureKeepsTheDaemon(t *testing.T) {
	paths := testPaths(t)
	runDaemonWith(t, paths, time.Hour, func(context.Context, GatewayEnv) (Gateway, error) { return nil, errors.New("port 7821 in use") })
	client, err := Dial(t.Context(), paths.Socket, "v-test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { ignore(client.Close()) }()
	status, err := client.Status(t.Context())
	if err != nil || status.Gateway != "off: port 7821 in use" {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

func TestGatewayActivityKeepsTheDaemonUp(t *testing.T) {
	paths := testPaths(t)
	gw := &fakeGateway{status: "on", closed: make(chan struct{})}
	envs, errs, _ := runDaemonWith(t, paths, 300*time.Millisecond, func(context.Context, GatewayEnv) (Gateway, error) { return gw, nil })
	env := <-envs
	until := time.Now().Add(900 * time.Millisecond)
	for time.Now().Before(until) {
		env.Activity()
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case err := <-errs:
		t.Fatalf("the daemon stopped while its gateway was used: %v", err)
	default:
	}
	select {
	case <-errs: //nolint:errcheck // the test checks that the daemon stops, not how.
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon must stop once the gateway is idle too")
	}
}

func TestGatewayClosesBeforeSessionsOnDaemonStop(t *testing.T) {
	paths := testPaths(t)
	gw := &fakeGateway{status: "on", closed: make(chan struct{})}
	var env GatewayEnv
	activeAtClose := make(chan bool, 1)
	gw.onClose = func() {
		snapshot, err := env.Manager.Snapshot(context.Background())
		activeAtClose <- err == nil && hasActiveSession(snapshot.Sessions)
	}
	envs, errs, _ := runDaemonWith(t, paths, time.Hour, func(context.Context, GatewayEnv) (Gateway, error) { return gw, nil }, "a")
	env = <-envs
	client, err := Dial(t.Context(), paths.Socket, "v-test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { ignore(client.Close()) }()
	if _, err := client.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: "direct", Name: "a"}}); err != nil {
		t.Fatal(err)
	}
	if err := client.StopDaemon(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case active := <-activeAtClose:
		if !active {
			t.Fatal("the gateway must be closed before the sessions are stopped")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the gateway was not closed")
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if !gw.unregistered.Load() {
		t.Fatal("airlock daemon stop must remove the gateway entry")
	}
}

// lockedBuffer is a daemon.log the test reads while the daemon writes it.
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

// M4: daemon.log tells the gateway started without its bearer token; the status keeps it.
func TestGatewayTokenStaysOutOfTheDaemonLog(t *testing.T) {
	paths := testPaths(t)
	token := strings.Repeat("c", 64)
	gw := &fakeGateway{closed: make(chan struct{}), status: "on http://127.0.0.1:7821/mcp, not registered; header Authorization: Bearer " + token,
		logged: "on http://127.0.0.1:7821/mcp, not registered"}
	var log lockedBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, &RunConfig{Paths: paths, Version: "v-test", Log: &log, Runner: sessions.Fixed(sessionstest.NewRunner()),
			Launch: sessionstest.NewLauncher().Launch, IdleTimeout: time.Hour,
			Gateway: func(context.Context, GatewayEnv) (Gateway, error) { return gw, nil }})
	}()
	waitSocket(t, paths)
	client, err := Dial(t.Context(), paths.Socket, "v-test")
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(t.Context())
	ignore(client.Close())
	cancel()
	if runErr := <-done; runErr != nil {
		t.Fatal(runErr)
	}
	if err != nil || !strings.Contains(status.Gateway, token) {
		t.Fatalf("status = %+v, %v: airlock daemon status keeps the token", status, err)
	}
	if got := log.String(); strings.Contains(got, token) || !strings.Contains(got, "MCP gateway started: on http://127.0.0.1:7821/mcp, not registered") {
		t.Fatalf("daemon.log = %q", got)
	}
}
