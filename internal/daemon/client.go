package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"

	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// clientBuffer is how many events a view may lag behind before it gets EventResync.
const clientBuffer = 1024

// VersionMismatchError reports a daemon of another version than this airlock.
type VersionMismatchError struct {
	Daemon string
	Client string
}

func (e *VersionMismatchError) Error() string {
	return fmt.Sprintf("the airlock daemon runs version %s, this airlock is %s", e.Daemon, e.Client)
}

// Client reaches the daemon's sessions; it implements sessions.Service.
type Client struct {
	peer        *peer
	interactive map[uint64]jumphost.InteractiveFunc
	subs        []*clientSub
	hello       Hello
	subscribeMu sync.Mutex
	mu          sync.Mutex
	subscribed  bool
}

type clientSub struct {
	ch chan sessions.Event
	// early holds the events received before the snapshot said where the subscription starts.
	early []sessions.Event
	// after is the Seq of the snapshot the subscription started from.
	after   uint64
	started bool
	lagged  bool
	closed  bool
}

// Dial connects to the daemon listening on socket and shakes hands. A daemon of another version
// gives a *VersionMismatchError with a usable client (Status, StopDaemon).
func Dial(ctx context.Context, socket, version string) (*Client, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	c := &Client{interactive: map[uint64]jumphost.InteractiveFunc{}}
	c.peer = newPeer(conn, c.serve, c.onEvent)
	go func() {
		discard(c.peer.run())
		c.closeSubs("connection to the daemon lost")
	}()
	if err := c.peer.call(ctx, "hello", map[string]any{"version": version, "protocol": ProtocolVersion}, &c.hello); err != nil {
		discard(c.peer.close())
		return nil, err
	}
	if c.hello.Version != version || c.hello.Protocol != ProtocolVersion {
		return c, &VersionMismatchError{Daemon: c.hello.Version, Client: version}
	}
	return c, nil
}

// serve answers the daemon's requests: the terminal commands of this client's openings.
func (c *Client) serve(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method != "interactive.run" {
		return nil, fmt.Errorf("unknown method %s", method)
	}
	var p interactiveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	c.mu.Lock()
	fn := c.interactive[p.Request]
	c.mu.Unlock()
	if fn == nil {
		return nil, jumphost.ErrSignInRequired
	}
	return nil, fn(ctx, p.Purpose, p.Name, p.Args...)
}

func (c *Client) onEvent(name string, data json.RawMessage) {
	if name != eventMethod {
		return
	}
	var ev sessions.Event
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sub := range c.subs {
		if sub.closed {
			continue
		}
		if !sub.started {
			sub.early = append(sub.early, ev)
			continue
		}
		if ev.Kind != sessions.EventClosed {
			deliverLocked(sub, &ev)
		}
	}
	if ev.Kind == sessions.EventClosed {
		c.closeSubsLocked(ev.Err)
	}
}

// deliverLocked gives ev to sub, unless the snapshot it started from already has it.
func deliverLocked(sub *clientSub, ev *sessions.Event) {
	if ev.Seq <= sub.after && ev.Kind != sessions.EventClosed && ev.Kind != sessions.EventResync {
		return
	}
	if sub.lagged {
		select {
		case sub.ch <- sessions.Event{Kind: sessions.EventResync, Seq: ev.Seq}:
			sub.lagged = false
		default:
		}
		return
	}
	select {
	case sub.ch <- *ev:
	default:
		sub.lagged = true
	}
}

func (c *Client) closeSubs(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeSubsLocked(reason)
}

// closeSubsLocked ends every subscription, telling it reason first when not empty.
func (c *Client) closeSubsLocked(reason string) {
	for _, sub := range c.subs {
		if sub.closed {
			continue
		}
		if reason != "" {
			select {
			case sub.ch <- sessions.Event{Kind: sessions.EventClosed, Err: reason}:
			default:
			}
		}
		sub.closed = true
		close(sub.ch)
	}
	c.subs = nil
}

func (c *Client) Hello() Hello          { return c.hello }
func (c *Client) Done() <-chan struct{} { return c.peer.done() }
func (c *Client) Close() error          { return c.peer.close() }

func (c *Client) Subscribe(ctx context.Context) (sessions.Snapshot, <-chan sessions.Event, error) {
	// One subscription goes over the wire; the others are fed locally.
	c.subscribeMu.Lock()
	defer c.subscribeMu.Unlock()
	sub := &clientSub{ch: make(chan sessions.Event, clientBuffer)}
	c.mu.Lock()
	c.subs = append(c.subs, sub)
	first := !c.subscribed
	c.mu.Unlock()
	method := "sessions.snapshot"
	if first {
		method = "sessions.subscribe"
	}
	var snapshot sessions.Snapshot
	if err := c.peer.call(ctx, method, nil, &snapshot); err != nil {
		c.removeSub(sub)
		return sessions.Snapshot{}, nil, err
	}
	c.mu.Lock()
	if first {
		c.subscribed = true
	}
	sub.after = snapshot.Seq
	sub.started = true
	for i := range sub.early {
		if !sub.closed {
			deliverLocked(sub, &sub.early[i])
		}
	}
	sub.early = nil
	c.mu.Unlock()
	context.AfterFunc(ctx, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if !sub.closed {
			sub.closed = true
			close(sub.ch)
		}
		c.removeSubLocked(sub)
	})
	return snapshot, sub.ch, nil
}

func (c *Client) removeSub(sub *clientSub) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeSubLocked(sub)
}

func (c *Client) removeSubLocked(sub *clientSub) {
	for i, s := range c.subs {
		if s == sub {
			c.subs = append(c.subs[:i], c.subs[i+1:]...)
			return
		}
	}
}

func (c *Client) Snapshot(ctx context.Context) (sessions.Snapshot, error) {
	var snapshot sessions.Snapshot
	err := c.peer.call(ctx, "sessions.snapshot", nil, &snapshot)
	return snapshot, err
}

func (c *Client) Open(ctx context.Context, req sessions.OpenRequest) (sessions.ID, error) { //nolint:gocritic // implements sessions.Service.
	var id sessions.ID
	var requestID uint64
	err := c.peer.callWithID(ctx, "sessions.open", openParams{Request: req, Interactive: req.Interactive != nil}, &id,
		c.registerInteractive(req.Interactive, &requestID))
	c.forgetInteractive(requestID)
	return id, err
}

func (c *Client) Restart(ctx context.Context, id sessions.ID, interactive jumphost.InteractiveFunc) error {
	return c.callInteractive(ctx, "sessions.restart", id, interactive)
}

func (c *Client) SignInAWS(ctx context.Context, id sessions.ID, interactive jumphost.InteractiveFunc) error {
	return c.callInteractive(ctx, "aws.signin", id, interactive)
}

// callInteractive calls method for session id, its terminal commands running with interactive.
func (c *Client) callInteractive(ctx context.Context, method string, id sessions.ID, interactive jumphost.InteractiveFunc) error {
	var requestID uint64
	err := c.peer.callWithID(ctx, method, idParams{ID: id, Interactive: interactive != nil}, nil,
		c.registerInteractive(interactive, &requestID))
	c.forgetInteractive(requestID)
	return err
}

// registerInteractive returns the onID hook recording fn for the request about to be sent.
func (c *Client) registerInteractive(fn jumphost.InteractiveFunc, requestID *uint64) func(uint64) {
	return func(id uint64) {
		*requestID = id
		if fn != nil {
			c.mu.Lock()
			c.interactive[id] = fn
			c.mu.Unlock()
		}
	}
}

func (c *Client) forgetInteractive(id uint64) {
	c.mu.Lock()
	delete(c.interactive, id)
	c.mu.Unlock()
}

func (c *Client) Stop(ctx context.Context, id sessions.ID) error {
	return c.peer.call(ctx, "sessions.stop", idParams{ID: id}, nil)
}

func (c *Client) Remove(ctx context.Context, id sessions.ID) error {
	return c.peer.call(ctx, "sessions.remove", idParams{ID: id}, nil)
}

func (c *Client) ChooseSignIn(ctx context.Context, id sessions.ID, index int) error {
	return c.peer.call(ctx, "signin.choose", idParams{ID: id, Index: index}, nil)
}

func (c *Client) SkipSignIn(ctx context.Context, id sessions.ID) error {
	return c.peer.call(ctx, "signin.skip", idParams{ID: id}, nil)
}

func (c *Client) RetrySignIn(ctx context.Context, id sessions.ID) error {
	return c.peer.call(ctx, "signin.retry", idParams{ID: id}, nil)
}

func (c *Client) Credentials(ctx context.Context, id sessions.ID) ([]jumphost.DBCredentials, error) {
	var creds []jumphost.DBCredentials
	err := c.peer.call(ctx, "credentials.read", idParams{ID: id}, &creds)
	return creds, err
}

func (c *Client) ResolveApproval(ctx context.Context, id sessions.ApprovalID, approve bool, interactive jumphost.InteractiveFunc) error {
	var requestID uint64
	err := c.peer.callWithID(ctx, "approval.resolve", approvalParams{ID: id, Approve: approve, Interactive: interactive != nil}, nil,
		c.registerInteractive(interactive, &requestID))
	c.forgetInteractive(requestID)
	return err
}

// StopAll stops the sessions only; StopDaemon also stops the daemon.
func (c *Client) StopAll(ctx context.Context) error { return c.StopDaemon(ctx) }

// Status describes the daemon.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var status Status
	err := c.peer.call(ctx, "daemon.status", nil, &status)
	return status, err
}

// StopDaemon stops every session, then the daemon.
func (c *Client) StopDaemon(ctx context.Context) error {
	return c.peer.call(ctx, "daemon.stop", nil, nil)
}
