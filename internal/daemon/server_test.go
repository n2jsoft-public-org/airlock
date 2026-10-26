package daemon

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/sessions"
	"github.com/n2jsoft-public-org/airlock/internal/sessions/sessionstest"
)

func newManager(t *testing.T) *sessions.Manager {
	t.Helper()
	m := sessions.NewManager(context.Background(), sessions.Config{Runner: sessions.Fixed(sessionstest.NewRunner()), Launch: sessionstest.NewLauncher().Launch})
	t.Cleanup(m.Shutdown)
	return m
}

func TestVersionMismatchIsReportedAndStatusStillWorks(t *testing.T) {
	_, _, socket := serve(t, newManager(t), ServerHooks{})
	client, err := Dial(t.Context(), socket, "v-other")
	mismatch, ok := errors.AsType[*VersionMismatchError](err)
	if !ok || mismatch.Daemon != "v-test" || mismatch.Client != "v-other" {
		t.Fatalf("err = %v", err)
	}
	defer func() { ignore(client.Close()) }()
	if status, err := client.Status(t.Context()); err != nil || status.Version != "v-test" {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

func TestRequestsBeforeHelloAreRefused(t *testing.T) {
	_, _, socket := serve(t, newManager(t), ServerHooks{})
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	p := newPeer(conn, nil, nil)
	go func() { ignore(p.run()) }()
	defer func() { ignore(p.close()) }()
	if err := p.call(t.Context(), "sessions.snapshot", nil, nil); err == nil {
		t.Fatal("a request before hello must be refused")
	}
}

func TestViewsAreCountedAndToldWhenTheDaemonStops(t *testing.T) {
	views := make(chan int, 8)
	server, client, _ := serve(t, newManager(t), ServerHooks{ViewsChanged: func(n int) { views <- n }})
	_, events, err := client.Subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n := <-views; n != 1 || server.Views() != 1 {
		t.Fatalf("views = %d", n)
	}
	server.Close("daemon stopped")
	ev := sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventClosed })
	if ev.Err != "daemon stopped" {
		t.Fatalf("event = %+v", ev)
	}
	select {
	case <-client.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client not done")
	}
}

func TestStopDaemonStopsSessionsThenCallsTheHook(t *testing.T) {
	stopped := make(chan struct{})
	_, client, _ := serve(t, newManager(t), ServerHooks{StopDaemon: func() { close(stopped) }})
	if err := client.StopDaemon(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("StopDaemon hook not called")
	}
}

func TestCloseDoesNotWaitForAViewThatDoesNotRead(t *testing.T) {
	service := newManager(t)
	server, _, socket := serve(t, service, ServerHooks{})
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { ignore(conn.Close()) }()
	// Raw requests, and nothing is ever read back: the connection stalls once its buffers are full.
	if _, err := conn.Write([]byte(`{"id":1,"method":"hello"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); err != nil { // the hello reply started: the handshake is done
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(`{"id":2,"method":"sessions.subscribe"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for server.Views() != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	var sc *serverConn
	server.mu.Lock()
	for c := range server.conns {
		if c.viewer {
			sc = c
		}
	}
	server.mu.Unlock()
	if sc == nil {
		t.Fatal("no view")
	}
	// Fill the socket buffer so the view's writes block: the connection is stalled by hand.
	filler := make(chan struct{})
	go func() {
		defer close(filler)
		blob := make([]byte, 1<<16)
		for sc.peer.conn.SetWriteDeadline(time.Now().Add(200*time.Millisecond)) == nil {
			if _, err := sc.peer.conn.Write(blob); err != nil {
				return
			}
		}
	}()
	<-filler
	ignore(sc.peer.conn.SetWriteDeadline(time.Time{}))
	stuck := make(chan struct{})
	go func() { defer close(stuck); ignore(sc.peer.send(&message{Event: "x", Data: make([]byte, 1<<16)})) }()
	closed := make(chan struct{})
	go func() { server.Close("bye"); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked on a view that does not read")
	}
	<-stuck
}

func TestViewsReturnToZeroWhenASubscriberDisconnectsAtOnce(t *testing.T) {
	server, _, socket := serve(t, newManager(t), ServerHooks{})
	for range 20 {
		conn, err := net.Dial("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		p := newPeer(conn, nil, nil)
		go func() { ignore(p.run()) }()
		ctx, cancel := context.WithCancel(t.Context())
		go func() { ignore(p.call(ctx, "hello", nil, nil)) }()
		go func() { ignore(p.call(ctx, "sessions.subscribe", nil, nil)) }()
		ignore(conn.Close())
		cancel()
	}
	deadline := time.Now().Add(2 * time.Second)
	for server.Views() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := server.Views(); n != 0 {
		t.Fatalf("views = %d", n)
	}
}

func TestStopDaemonReplyArrivesEvenWhenTheHookClosesTheServer(t *testing.T) {
	var server *Server
	var client *Client
	server, client, _ = serve(t, newManager(t), ServerHooks{StopDaemon: func() { server.Close("stopping") }})
	if err := client.StopDaemon(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// slowStop is a service whose StopAll ends once its context does or the test releases it.
type slowStop struct {
	sessions.Service
	release chan struct{}
}

func (s *slowStop) StopAll(ctx context.Context) error {
	select {
	case <-s.release:
	case <-ctx.Done():
	}
	return nil
}

func TestStopDaemonHookFiresWhenTheRequesterDisconnects(t *testing.T) {
	service := &slowStop{Service: newManager(t), release: make(chan struct{})}
	stopped := make(chan struct{})
	_, _, socket := serve(t, service, ServerHooks{StopDaemon: func() { close(stopped) }})
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(`{"id":1,"method":"hello"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(`{"id":2,"method":"daemon.stop"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	ignore(conn.Close())
	close(service.release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("StopDaemon hook not called")
	}
}

func TestSubscriptionSeesTheClosedEventOnce(t *testing.T) {
	server, client, _ := serve(t, newManager(t), ServerHooks{})
	_, events, err := client.Subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	server.Close("bye")
	closed := 0
	timeout := time.After(5 * time.Second)
	for done := false; !done; {
		select {
		case ev, ok := <-events:
			if !ok {
				done = true
			} else if ev.Kind == sessions.EventClosed {
				closed++
			}
		case <-timeout:
			t.Fatal("events not closed")
		}
	}
	if closed != 1 {
		t.Fatalf("closed events = %d", closed)
	}
}
