package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/sessions/sessionstest"
)

// fixedSnapshot answers the idle watch with its sessions.
type fixedSnapshot struct{ sessions []sessions.Session }

func (f fixedSnapshot) Snapshot(context.Context) (sessions.Snapshot, error) {
	return sessions.Snapshot{Sessions: f.sessions}, nil
}

// idleStops reports whether an idle watch over list, without view, stops the daemon within wait.
func idleStops(t *testing.T, list []sessions.Session, wait time.Duration) bool {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopped := make(chan struct{})
	w := newIdleWatch(100*time.Millisecond, func() { close(stopped) })
	done := make(chan struct{})
	go func() { defer close(done); w.watch(ctx, fixedSnapshot{sessions: list}) }()
	defer func() { cancel(); <-done }()
	select {
	case <-stopped:
		return true
	case <-time.After(wait):
		return false
	}
}

func TestIdleStopIgnoresEndedSessions(t *testing.T) {
	ended := sessions.Session{ID: 1, Target: "archive-db", State: sessions.StateEnded}
	if !idleStops(t, []sessions.Session{ended}, 5*time.Second) {
		t.Fatal("a daemon left with ended sessions only must stop once idle")
	}
}

func TestIdleStopWaitsForRunningSessions(t *testing.T) {
	running := sessions.Session{ID: 1, Target: "archive-db", State: sessions.StateRunning}
	if idleStops(t, []sessions.Session{running}, 500*time.Millisecond) {
		t.Fatal("a daemon with a running session must keep running")
	}
}

func TestTwoConcurrentStartsGiveOneDaemon(t *testing.T) {
	paths := testPaths(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- Run(ctx, &RunConfig{Paths: paths, Version: "v-test", Runner: sessions.Fixed(sessionstest.NewRunner()),
				Launch: sessionstest.NewLauncher().Launch, IdleTimeout: time.Hour})
		}()
	}
	close(start)
	select {
	case err := <-results:
		if !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("first result = %v, want ErrAlreadyRunning", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("neither start returned: one must refuse at once")
	}
	waitSocket(t, paths)
	cancel()
	wg.Wait()
	if err := <-results; err != nil {
		t.Fatalf("the serving daemon returned %v", err)
	}
}

// interactiveOpener is a service whose Open runs the opening's terminal command once, when told.
type interactiveOpener struct {
	sessions.Service
	// run tells the opening of target to run its command; the others wait for their context.
	run map[string]chan struct{}
}

func (s *interactiveOpener) Open(ctx context.Context, req sessions.OpenRequest) (sessions.ID, error) { //nolint:gocritic // implements sessions.Service.
	select {
	case <-s.run[req.Options.Name]:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	if req.Interactive == nil {
		return 0, errors.New("no terminal")
	}
	return 1, req.Interactive(ctx, "sign in", "airlock", "sso-login")
}

func TestInteractiveReachesOnlyTheClientThatOpened(t *testing.T) {
	service := &interactiveOpener{Service: newManager(t), run: map[string]chan struct{}{
		"asker": make(chan struct{}), "other": make(chan struct{}),
	}}
	_, asker, socket := serve(t, service, ServerHooks{})
	other, err := Dial(t.Context(), socket, "v-test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { ignore(other.Close()) }()

	var mu sync.Mutex
	var calls []string
	recordAs := func(who string) jumphost.InteractiveFunc {
		return func(_ context.Context, _, name string, args ...string) error {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, who+" "+name+" "+strings.Join(args, " "))
			return nil
		}
	}
	// Both openings are request 2 of their connection (after hello): the IDs alone cannot tell them apart.
	otherCtx, cancelOther := context.WithCancel(t.Context())
	otherDone := make(chan error, 1)
	go func() {
		_, openErr := other.Open(otherCtx, sessions.OpenRequest{Options: jumphost.Options{Name: "other"}, Interactive: recordAs("other")})
		otherDone <- openErr
	}()
	// The other opening waits in the daemon, its terminal command registered under the same ID.
	deadline := time.Now().Add(5 * time.Second)
	for !hasInteractive(other) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(service.run["asker"])
	if _, err := asker.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{Name: "asker"}, Interactive: recordAs("asker")}); err != nil {
		t.Fatal(err)
	}
	cancelOther()
	<-otherDone
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != "asker airlock sso-login" {
		t.Fatalf("interactive calls = %v", calls)
	}
}

func hasInteractive(c *Client) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.interactive) > 0
}

func TestUnknownMethodGetsAnErrorReply(t *testing.T) {
	_, client, _ := serve(t, newManager(t), ServerHooks{})
	err := client.peer.call(t.Context(), "sessions.teleport", idParams{ID: 1}, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown method sessions.teleport") {
		t.Fatalf("err = %v", err)
	}
	// The connection still works.
	if _, err := client.Status(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedLineEndsTheServerConnection(t *testing.T) {
	_, _, socket := serve(t, newManager(t), ServerHooks{})
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { ignore(conn.Close()) }()
	if _, err := conn.Write([]byte("{not json\n")); err != nil {
		t.Fatal(err)
	}
	ignore(conn.SetReadDeadline(time.Now().Add(5 * time.Second)))
	var netErr net.Error
	if _, err := conn.Read(make([]byte, 64)); err == nil || errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("read = %v, want the daemon to close the connection", err)
	}
}

func TestMalformedLineFailsTheCallsOfTheClient(t *testing.T) {
	a, b := net.Pipe()
	client := newPeer(a, nil, nil)
	ran := make(chan error, 1)
	go func() { ran <- client.run() }()
	defer func() { ignore(client.close()); ignore(b.Close()) }()
	called := make(chan error, 1)
	go func() { called <- client.call(t.Context(), "sessions.snapshot", nil, nil) }()
	// The "daemon" reads the request, then answers with a line that is no message.
	if err := json.NewDecoder(b).Decode(&message{}); err != nil {
		t.Fatal(err)
	}
	go func() { _, err := b.Write([]byte("]garbage\n")); ignore(err) }()
	select {
	case err := <-called:
		if err == nil {
			t.Fatal("a call on a connection that broke must fail")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call never returned")
	}
	if err := <-ran; err == nil {
		t.Fatal("run must return the decode error")
	}
	select {
	case <-client.done():
	default:
		t.Fatal("the peer must be done")
	}
}
