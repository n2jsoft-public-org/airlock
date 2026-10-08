package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

const eventMethod = "session.event"

// discard drops an error nobody can act on (a connection ending, a peer already gone).
func discard(_ error) {}

// closeWriteTimeout bounds how long Close waits on a view that does not read.
const closeWriteTimeout = time.Second

// stopAllTimeout bounds waiting for the sessions to end when the daemon is stopped.
const stopAllTimeout = 30 * time.Second

// Hello is the daemon's answer to the handshake.
type Hello struct {
	StartedAt time.Time `json:"startedAt"`
	Version   string    `json:"version"`
	Protocol  int       `json:"protocol"`
	PID       int       `json:"pid"`
}

// Status describes a running daemon.
type Status struct {
	// Gateway describes the MCP gateway: on, with its URL and files, or off and why.
	Gateway string `json:"gateway,omitempty"`
	Hello
	Sessions int `json:"sessions"`
	Views    int `json:"views"`
}

// ServerHooks lets the daemon follow its server.
type ServerHooks struct {
	// StopDaemon is called once daemon.stop stopped the sessions.
	StopDaemon func()
	// ViewsChanged is called with the number of subscribed connections when it changes. It runs under
	// the server's lock: it must not block nor call back into the Server.
	ViewsChanged func(views int)
	// BeforeStop is called by daemon.stop before the sessions are stopped; it must not call back into
	// the Server.
	BeforeStop func()
	// GatewayStatus describes the MCP gateway for daemon.status; nil reports none.
	GatewayStatus func() string
}

// Server serves a sessions.Service on a socket.
type Server struct {
	hooks   ServerHooks
	service sessions.Service
	conns   map[*serverConn]struct{}
	// done is closed by Close.
	done   chan struct{}
	hello  Hello
	mu     sync.Mutex
	views  int
	closed bool
}

type serverConn struct {
	peer   *peer
	ready  atomic.Bool
	viewer bool // guarded by Server.mu
	gone   bool // guarded by Server.mu: the connection ended
}

// NewServer returns a server over service, answering the handshake with version.
func NewServer(service sessions.Service, version string, hooks ServerHooks) *Server {
	return &Server{service: service, hooks: hooks, conns: map[*serverConn]struct{}{}, done: make(chan struct{}),
		hello: Hello{Version: version, Protocol: ProtocolVersion, PID: os.Getpid(), StartedAt: time.Now()}}
}

// Serve accepts connections on l until Close.
func (s *Server) Serve(l net.Listener) error {
	go func() {
		<-s.done
		discard(l.Close())
	}()
	for {
		conn, err := l.Accept()
		if err != nil {
			if s.isClosed() {
				return nil
			}
			return err
		}
		go s.handleConn(conn)
	}
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Server) handleConn(conn net.Conn) {
	sc := &serverConn{}
	sc.peer = newPeer(conn, func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		return s.handle(ctx, sc, method, params)
	}, nil)
	sc.peer.answered = func(method string) {
		// The daemon may exit once the requester has its answer.
		if method == "daemon.stop" && s.hooks.StopDaemon != nil {
			go s.hooks.StopDaemon()
		}
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		discard(conn.Close())
		return
	}
	s.conns[sc] = struct{}{}
	s.mu.Unlock()

	discard(sc.peer.run())

	s.mu.Lock()
	delete(s.conns, sc)
	sc.gone = true
	if sc.viewer {
		s.views--
		// Under the lock so counts arrive in order; the hook does not call back into the server.
		if s.hooks.ViewsChanged != nil {
			s.hooks.ViewsChanged(s.views)
		}
	}
	s.mu.Unlock()
}

type idParams struct {
	ID          sessions.ID `json:"id"`
	Index       int         `json:"index,omitempty"`
	Interactive bool        `json:"interactive,omitempty"`
}

type openParams struct {
	Request     sessions.OpenRequest `json:"request"`
	Interactive bool                 `json:"interactive,omitempty"`
}

type approvalParams struct {
	ID          sessions.ApprovalID `json:"id"`
	Approve     bool                `json:"approve,omitempty"`
	Interactive bool                `json:"interactive,omitempty"`
}

type interactiveParams struct {
	Purpose string   `json:"purpose"`
	Name    string   `json:"name"`
	Args    []string `json:"args"`
	// Request is the ID of the opening the command is for, on the client's side.
	Request uint64 `json:"request"`
}

func (s *Server) handle(ctx context.Context, sc *serverConn, method string, params json.RawMessage) (any, error) {
	if method == "hello" {
		sc.ready.Store(true)
		return s.hello, nil
	}
	if !sc.ready.Load() {
		return nil, errors.New("hello first")
	}
	var p idParams
	switch method {
	case "sessions.subscribe":
		return s.subscribe(sc)
	case "sessions.snapshot":
		return s.service.Snapshot(ctx)
	case "approval.resolve":
		var ap approvalParams
		if err := json.Unmarshal(params, &ap); err != nil {
			return nil, err
		}
		var interactive jumphost.InteractiveFunc
		if ap.Approve && ap.Interactive {
			// The opening this answer allows signs in on the answering view's terminal.
			interactive = s.relayInteractive(sc, requestID(ctx))
		}
		return nil, s.service.ResolveApproval(ctx, ap.ID, ap.Approve, interactive)
	case "sessions.open":
		var op openParams
		if err := json.Unmarshal(params, &op); err != nil {
			return nil, err
		}
		if op.Interactive {
			op.Request.Interactive = s.relayInteractive(sc, requestID(ctx))
		}
		return s.service.Open(ctx, op.Request)
	case "sessions.restart", "aws.signin":
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		var interactive jumphost.InteractiveFunc
		if p.Interactive {
			interactive = s.relayInteractive(sc, requestID(ctx))
		}
		if method == "aws.signin" {
			return nil, s.service.SignInAWS(ctx, p.ID, interactive)
		}
		return nil, s.service.Restart(ctx, p.ID, interactive)
	case "daemon.status":
		snapshot, err := s.service.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		status := Status{Hello: s.hello, Sessions: len(snapshot.Sessions), Views: s.Views()}
		if s.hooks.GatewayStatus != nil {
			status.Gateway = s.hooks.GatewayStatus()
		}
		return status, nil
	case "daemon.stop":
		stopCtx, cancel := context.WithTimeout(context.Background(), stopAllTimeout)
		defer cancel()
		if s.hooks.BeforeStop != nil {
			s.hooks.BeforeStop()
		}
		// The StopDaemon hook runs once the reply was written (see handleConn).
		return nil, s.service.StopAll(stopCtx)
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	switch method {
	case "sessions.stop":
		return nil, s.service.Stop(ctx, p.ID)
	case "sessions.remove":
		return nil, s.service.Remove(ctx, p.ID)
	case "signin.choose":
		return nil, s.service.ChooseSignIn(ctx, p.ID, p.Index)
	case "signin.skip":
		return nil, s.service.SkipSignIn(ctx, p.ID)
	case "signin.retry":
		return nil, s.service.RetrySignIn(ctx, p.ID)
	case "credentials.read":
		return s.service.Credentials(ctx, p.ID)
	default:
		return nil, errors.New("unknown method " + method)
	}
}

// subscribe makes sc a view: its snapshot is the answer, its events follow as notifications.
func (s *Server) subscribe(sc *serverConn) (sessions.Snapshot, error) {
	s.mu.Lock()
	gone, already := sc.gone, sc.viewer
	s.mu.Unlock()
	if gone {
		return sessions.Snapshot{}, errors.New("connection closed")
	}
	if already {
		// One event stream per connection.
		return s.service.Snapshot(sc.peer.ctx)
	}
	snapshot, events, err := s.service.Subscribe(sc.peer.ctx)
	if err != nil {
		return sessions.Snapshot{}, err
	}
	s.mu.Lock()
	if sc.gone || sc.viewer {
		s.mu.Unlock()
		return sessions.Snapshot{}, errors.New("connection closed")
	}
	sc.viewer = true
	s.views++
	if s.hooks.ViewsChanged != nil {
		s.hooks.ViewsChanged(s.views)
	}
	s.mu.Unlock()
	go func() {
		for ev := range events {
			if sc.peer.notify(eventMethod, ev) != nil {
				return
			}
		}
	}()
	return snapshot, nil
}

// relayInteractive runs the terminal commands of request on the terminal of sc.
func (s *Server) relayInteractive(sc *serverConn, request uint64) jumphost.InteractiveFunc {
	return func(ctx context.Context, purpose, name string, args ...string) error {
		err := sc.peer.call(ctx, "interactive.run", interactiveParams{Purpose: purpose, Name: name, Args: args, Request: request}, nil)
		select {
		case <-sc.peer.done():
			// The terminal that asked for the opening is gone: nobody can sign in.
			return jumphost.ErrSignInRequired
		default:
			return err
		}
	}
}

// Views returns the number of subscribed connections.
func (s *Server) Views() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.views
}

// Close tells every view why the daemon stops, then closes the connections. Safe to call repeatedly.
func (s *Server) Close(reason string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	conns := make(map[*serverConn]bool, len(s.conns))
	for sc := range s.conns {
		conns[sc] = sc.viewer
	}
	s.mu.Unlock()
	// A view that does not read must not block the others: pending and new writes fail fast.
	deadline := time.Now().Add(closeWriteTimeout)
	for sc := range conns {
		discard(sc.peer.conn.SetWriteDeadline(deadline))
	}
	for sc, viewer := range conns {
		if viewer {
			discard(sc.peer.notify(eventMethod, sessions.Event{Kind: sessions.EventClosed, Err: reason}))
		}
		discard(sc.peer.close())
	}
}
