package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// ignore drops an error the test has no use for (teardown, a read loop ending with its connection).
func ignore(_ error) {}

func pipePeers(t *testing.T, serve handler) *peer {
	t.Helper()
	a, b := net.Pipe()
	server := newPeer(b, serve, nil)
	client := newPeer(a, func(context.Context, string, json.RawMessage) (any, error) {
		return nil, errors.New("client serves nothing")
	}, nil)
	go func() { ignore(server.run()) }()
	go func() { ignore(client.run()) }()
	t.Cleanup(func() { ignore(client.close()); ignore(server.close()) })
	return client
}

func TestCallReturnsResultAndErrors(t *testing.T) {
	client := pipePeers(t, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case "echo":
			var s string
			if err := json.Unmarshal(params, &s); err != nil {
				return nil, err
			}
			return s, nil
		case "signin":
			return nil, jumphost.ErrSignInRequired
		default:
			return nil, sessions.ErrUnknownSession
		}
	})
	var out string
	if err := client.call(t.Context(), "echo", "hi", &out); err != nil || out != "hi" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if err := client.call(t.Context(), "signin", nil, nil); !errors.Is(err, jumphost.ErrSignInRequired) {
		t.Fatalf("err = %v", err)
	}
	if err := client.call(t.Context(), "other", nil, nil); !errors.Is(err, sessions.ErrUnknownSession) {
		t.Fatalf("err = %v", err)
	}
}

func TestCanceledCallCancelsTheRemoteHandler(t *testing.T) {
	canceled := make(chan struct{})
	client := pipePeers(t, func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if err := client.call(ctx, "slow", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the remote handler was not canceled")
	}
}

func TestEventsAndClose(t *testing.T) {
	a, b := net.Pipe()
	got := make(chan string, 1)
	receiver := newPeer(a, nil, func(name string, data json.RawMessage) { got <- name + " " + string(data) })
	sender := newPeer(b, nil, nil)
	go func() { ignore(receiver.run()) }()
	go func() { ignore(sender.run()) }()
	if err := sender.notify("session.event", map[string]int{"seq": 3}); err != nil {
		t.Fatal(err)
	}
	if msg := <-got; msg != `session.event {"seq":3}` {
		t.Fatalf("event = %s", msg)
	}
	ignore(sender.close())
	select {
	case <-receiver.done():
	case <-time.After(5 * time.Second):
		t.Fatal("receiver not done after the other side closed")
	}
}

func TestHandlerSeesTheRequestID(t *testing.T) {
	client := pipePeers(t, func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		return requestID(ctx), nil
	})
	var sent, seen uint64
	if err := client.callWithID(t.Context(), "id", nil, &seen, func(id uint64) { sent = id }); err != nil {
		t.Fatal(err)
	}
	if sent == 0 || seen != sent {
		t.Fatalf("sent = %d, seen by the handler = %d", sent, seen)
	}
	if requestID(t.Context()) != 0 {
		t.Fatal("a context outside a handler must have no request ID")
	}
}

func TestPreCanceledCallCancelsTheRemoteHandler(t *testing.T) {
	canceled := make(chan struct{})
	client := pipePeers(t, func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		select {
		case <-ctx.Done():
			close(canceled)
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return nil, errors.New("not canceled")
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := client.call(ctx, "slow", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the remote handler was not canceled")
	}
}

func TestCanceledCallReturnsTheResultOfAHandlerIgnoringCancel(t *testing.T) {
	client := pipePeers(t, func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		time.Sleep(100 * time.Millisecond)
		return "opened", nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	var out string
	if err := client.call(ctx, "open", nil, &out); err != nil || out != "opened" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}
