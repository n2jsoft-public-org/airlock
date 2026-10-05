// Package daemon serves the sessions of airlock's daemon mode on a local socket, and lets the
// dashboards of each terminal reach them.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// ProtocolVersion changes when messages change incompatibly.
const ProtocolVersion = 2

// cancelMethod tells the other side to cancel the request whose ID it carries.
const cancelMethod = "cancel"

// cancelReplyTimeout bounds how long a canceled call waits for the other side's reply.
const cancelReplyTimeout = 5 * time.Second

// message is one line of the protocol: a request (ID, Method), a response (ID, Result or Error)
// or an event (Event, Data).
type message struct {
	Error  *wireError      `json:"error,omitempty"`
	Method string          `json:"method,omitempty"`
	Event  string          `json:"event,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
	ID     uint64          `json:"id,omitempty"`
}

type wireError struct {
	Message string `json:"message"`
	// Code keeps the identity of the errors callers test with errors.Is.
	Code string `json:"code,omitempty"`
}

var errorCodes = map[string]error{
	"sign-in-required":  jumphost.ErrSignInRequired,
	"unknown-session":   sessions.ErrUnknownSession,
	"closed":            sessions.ErrClosed,
	"approval-resolved": sessions.ErrApprovalResolved,
	"canceled":          context.Canceled,
	"deadline":          context.DeadlineExceeded,
}

func encodeError(err error) *wireError {
	for code, known := range errorCodes {
		if errors.Is(err, known) {
			return &wireError{Message: err.Error(), Code: code}
		}
	}
	return &wireError{Message: err.Error()}
}

// remoteError is an error of the other side, keeping the identity of known errors.
type remoteError struct {
	known   error
	message string
}

func (e *remoteError) Error() string { return e.message }
func (e *remoteError) Unwrap() error { return e.known }

func decodeError(e *wireError) error {
	return &remoteError{message: e.Message, known: errorCodes[e.Code]}
}

// handler serves one request of the other side.
type handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

type requestIDKey struct{}

// requestID is the ID of the request ctx serves (0 outside a handler).
func requestID(ctx context.Context) uint64 {
	id, ok := ctx.Value(requestIDKey{}).(uint64)
	if !ok {
		return 0
	}
	return id
}

// peer is one side of a connection: it sends requests and events, and serves the other side's.
type peer struct {
	conn    net.Conn
	enc     *json.Encoder
	handle  handler
	onEvent func(name string, data json.RawMessage)
	// answered, when set, is called with the method of each request once its response was written
	// or could not be (the other side is gone).
	answered func(method string)
	pending  map[uint64]chan message
	serving  map[uint64]context.CancelFunc
	finished chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	nextID   uint64
	writeMu  sync.Mutex
	mu       sync.Mutex
	once     sync.Once
}

func newPeer(conn net.Conn, handle handler, onEvent func(name string, data json.RawMessage)) *peer {
	ctx, cancel := context.WithCancel(context.Background())
	return &peer{
		conn: conn, enc: json.NewEncoder(conn), handle: handle, onEvent: onEvent,
		pending: map[uint64]chan message{}, serving: map[uint64]context.CancelFunc{},
		finished: make(chan struct{}), ctx: ctx, cancel: cancel,
	}
}

func (p *peer) send(msg *message) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return p.enc.Encode(msg)
}

// run reads messages until the connection ends; it returns the read error. A line that is no
// message ends the connection: the calls waiting on it fail.
func (p *peer) run() error {
	defer func() {
		discard(p.conn.Close())
		p.shutdown()
	}()
	dec := json.NewDecoder(p.conn)
	for {
		var msg message
		if err := dec.Decode(&msg); err != nil {
			return err
		}
		switch {
		case msg.Event != "":
			if p.onEvent != nil {
				p.onEvent(msg.Event, msg.Data)
			}
		case msg.Method == cancelMethod:
			var id uint64
			if json.Unmarshal(msg.Params, &id) == nil {
				p.cancelServing(id)
			}
		case msg.Method != "":
			// Register the request before reading on, so a following cancel cannot miss it.
			ctx, cancel := context.WithCancel(context.WithValue(p.ctx, requestIDKey{}, msg.ID))
			p.mu.Lock()
			p.serving[msg.ID] = cancel
			p.mu.Unlock()
			go p.serve(ctx, cancel, &msg)
		default:
			p.mu.Lock()
			reply := p.pending[msg.ID]
			delete(p.pending, msg.ID)
			p.mu.Unlock()
			if reply != nil {
				reply <- msg
			}
		}
	}
}

func (p *peer) cancelServing(id uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cancel := p.serving[id]; cancel != nil {
		cancel()
	}
}

func (p *peer) serve(ctx context.Context, cancel context.CancelFunc, req *message) {
	defer func() {
		p.mu.Lock()
		delete(p.serving, req.ID)
		p.mu.Unlock()
		cancel()
	}()

	resp := message{ID: req.ID}
	if p.handle == nil {
		resp.Error = &wireError{Message: "unknown method " + req.Method}
	} else if result, err := p.handle(ctx, req.Method, req.Params); err != nil {
		resp.Error = encodeError(err)
	} else if resp.Result, err = json.Marshal(result); err != nil {
		resp.Error = encodeError(err)
	}
	if p.send(&resp) != nil {
		// The response cannot reach the other side: the connection is gone.
		p.abort()
	}
	if p.answered != nil {
		p.answered(req.Method)
	}
}

func (p *peer) call(ctx context.Context, method string, params, result any) error {
	return p.callWithID(ctx, method, params, result, nil)
}

// callWithID sends a request and waits for its response; onID, when set, gets the request's ID
// before it is sent. Canceling ctx asks the other side to cancel the request.
func (p *peer) callWithID(ctx context.Context, method string, params, result any, onID func(uint64)) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	reply := make(chan message, 1)
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	p.pending[id] = reply
	p.mu.Unlock()
	if onID != nil {
		onID(id)
	}
	if err := p.send(&message{ID: id, Method: method, Params: raw}); err != nil {
		p.forget(id)
		return fmt.Errorf("connection to the daemon lost: %w", err)
	}
	select {
	case resp := <-reply:
		return decodeResponse(new(resp), result)
	case <-ctx.Done():
		return p.awaitAfterCancel(ctx, id, reply, result)
	case <-p.finished:
		p.forget(id)
		return errors.New("connection to the daemon lost")
	}
}

// awaitAfterCancel asks the other side to cancel request id, then waits a little for its reply:
// the request may have completed anyway, and the caller must learn it.
func (p *peer) awaitAfterCancel(ctx context.Context, id uint64, reply <-chan message, result any) error {
	if p.send(&message{Method: cancelMethod, Params: json.RawMessage(strconv.FormatUint(id, 10))}) != nil {
		p.forget(id)
		p.abort()
		return ctx.Err()
	}
	timer := time.NewTimer(cancelReplyTimeout)
	defer timer.Stop()
	select {
	case resp := <-reply:
		err := decodeResponse(new(resp), result)
		if errors.Is(err, context.Canceled) {
			return ctx.Err()
		}
		return err
	case <-timer.C:
	case <-p.finished:
	}
	p.forget(id)
	return ctx.Err()
}

func (p *peer) forget(id uint64) {
	p.mu.Lock()
	delete(p.pending, id)
	p.mu.Unlock()
}

func decodeResponse(resp *message, result any) error {
	if resp.Error != nil {
		return decodeError(resp.Error)
	}
	if result != nil && len(resp.Result) > 0 {
		return json.Unmarshal(resp.Result, result)
	}
	return nil
}

func (p *peer) notify(event string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return p.send(&message{Event: event, Data: raw})
}

func (p *peer) done() <-chan struct{} { return p.finished }

func (p *peer) close() error {
	err := p.conn.Close()
	p.shutdown()
	return err
}

// abort drops the connection after a failed write; the read loop then ends.
func (p *peer) abort() {
	if p.conn.Close() != nil {
		p.shutdown()
	}
}

func (p *peer) shutdown() {
	p.once.Do(func() {
		p.cancel()
		close(p.finished)
	})
}
