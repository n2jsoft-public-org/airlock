package sessions_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/sessions"
	"github.com/n2jsoft-public-org/airlock/internal/sessions/sessionstest"
)

func newApprovalManager(t *testing.T, cfg sessions.Config) *sessions.Manager {
	t.Helper()
	cfg.Runner = sessions.Fixed(sessionstest.NewRunner())
	cfg.Launch = sessionstest.NewLauncher().Launch
	m := sessions.NewManager(context.Background(), cfg)
	t.Cleanup(m.Shutdown)
	return m
}

type grantResult struct {
	err   error
	grant sessions.Grant
}

func requestAsync(ctx context.Context, m *sessions.Manager, a *sessions.Approval) <-chan grantResult {
	done := make(chan grantResult, 1)
	go func() {
		grant, err := m.RequestApproval(ctx, *a)
		done <- grantResult{grant: grant, err: err}
	}()
	return done
}

func waitRequested(t *testing.T, events <-chan sessions.Event) sessions.Approval {
	t.Helper()
	ev := sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventApprovalRequested })
	return *ev.Approval
}

func TestApprovalWithoutViewIsRefused(t *testing.T) {
	m := newApprovalManager(t, sessions.Config{})
	if _, err := m.RequestApproval(t.Context(), sessions.Approval{Agent: "claude-code", Target: "orders-db"}); !errors.Is(err, sessions.ErrNoView) {
		t.Fatalf("err = %v, want ErrNoView", err)
	}
}

func TestApprovalFirstAnswerWinsAndWaitsForTheOpening(t *testing.T) {
	m := newApprovalManager(t, sessions.Config{})
	m.ViewsChanged(2)
	_, events, err := m.Subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result := requestAsync(t.Context(), m, &sessions.Approval{Agent: "claude-code", Type: "rds", Target: "orders-db", Writes: true})
	pending := waitRequested(t, events)
	if pending.ID == 0 || pending.ExpiresAt.Sub(pending.RequestedAt) != sessions.ApprovalTimeout || !pending.Writes {
		t.Fatalf("pending = %+v", pending)
	}
	snapshot, err := m.Snapshot(t.Context())
	if err != nil || len(snapshot.Approvals) != 1 {
		t.Fatalf("snapshot approvals = %+v, %v", snapshot.Approvals, err)
	}

	ran := false
	interactive := func(ctx context.Context, _, _ string, _ ...string) error { ran = true; return ctx.Err() }
	resolved := make(chan error, 1)
	go func() { resolved <- m.ResolveApproval(context.Background(), pending.ID, true, interactive) }()
	got := <-result
	if got.err != nil || got.grant.Interactive == nil || got.grant.Done == nil {
		t.Fatalf("grant = %+v, %v", got.grant, got.err)
	}
	if err := got.grant.Interactive(t.Context(), "sign in", "airlock", "sso-login"); err != nil || !ran {
		t.Fatalf("the approving view's interactive must run: ran=%v err=%v", ran, err)
	}
	if err := m.ResolveApproval(t.Context(), pending.ID, false, nil); !errors.Is(err, sessions.ErrApprovalResolved) {
		t.Fatalf("second answer err = %v", err)
	}
	select {
	case err := <-resolved:
		t.Fatalf("the approving answer must wait for the opening (err = %v)", err)
	case <-time.After(50 * time.Millisecond):
	}
	got.grant.Done()
	got.grant.Done() // safe to call twice
	if err := <-resolved; err != nil {
		t.Fatal(err)
	}
	ev := sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventApprovalResolved })
	if ev.ApprovalID != pending.ID || !ev.Approved {
		t.Fatalf("resolved event = %+v", ev)
	}
}

func TestApprovalDenied(t *testing.T) {
	m := newApprovalManager(t, sessions.Config{})
	m.ViewsChanged(1)
	_, events, err := m.Subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result := requestAsync(t.Context(), m, &sessions.Approval{Agent: "claude-code", Target: "orders-db"})
	pending := waitRequested(t, events)
	if err := m.ResolveApproval(t.Context(), pending.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	if got := <-result; !errors.Is(got.err, sessions.ErrApprovalDenied) {
		t.Fatalf("err = %v, want ErrApprovalDenied", got.err)
	}
}

// Review Focus 3: the last view leaving refuses at once.
func TestApprovalRefusedWhenTheLastViewLeaves(t *testing.T) {
	m := newApprovalManager(t, sessions.Config{})
	m.ViewsChanged(1)
	_, events, err := m.Subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result := requestAsync(t.Context(), m, &sessions.Approval{Agent: "claude-code", Target: "orders-db"})
	waitRequested(t, events)
	m.ViewsChanged(0)
	select {
	case got := <-result:
		if !errors.Is(got.err, sessions.ErrNoView) {
			t.Fatalf("err = %v, want ErrNoView", got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("the request must be refused as soon as no view is left")
	}
	if snapshot, err := m.Snapshot(t.Context()); err != nil || len(snapshot.Approvals) != 0 {
		t.Fatalf("approvals left: %+v, %v", snapshot.Approvals, err)
	}
}

func TestApprovalExpires(t *testing.T) {
	m := newApprovalManager(t, sessions.Config{AfterFunc: func(d time.Duration, f func()) func() bool {
		if d == sessions.ApprovalTimeout {
			go f()
		}
		return func() bool { return true }
	}})
	m.ViewsChanged(1)
	if _, err := m.RequestApproval(t.Context(), sessions.Approval{Agent: "claude-code"}); !errors.Is(err, sessions.ErrApprovalExpired) {
		t.Fatalf("err = %v, want ErrApprovalExpired", err)
	}
}

func TestApprovalWithdrawnWhenTheRequesterGivesUp(t *testing.T) {
	m := newApprovalManager(t, sessions.Config{})
	m.ViewsChanged(1)
	_, events, err := m.Subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := requestAsync(ctx, m, &sessions.Approval{Agent: "claude-code"})
	pending := waitRequested(t, events)
	cancel()
	if got := <-result; !errors.Is(got.err, context.Canceled) {
		t.Fatalf("err = %v", got.err)
	}
	if err := m.ResolveApproval(t.Context(), pending.ID, true, nil); !errors.Is(err, sessions.ErrApprovalResolved) {
		t.Fatalf("answering a withdrawn request: %v", err)
	}
}

func TestApplyApproval(t *testing.T) {
	a := sessions.Approval{ID: 4, Target: "orders-db"}
	list := sessions.ApplyApproval(nil, &sessions.Event{Kind: sessions.EventApprovalRequested, Approval: &a})
	list = sessions.ApplyApproval(list, &sessions.Event{Kind: sessions.EventApprovalRequested, Approval: &a})
	if len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if list = sessions.ApplyApproval(list, &sessions.Event{Kind: sessions.EventApprovalResolved, ApprovalID: 4}); len(list) != 0 {
		t.Fatalf("list = %+v", list)
	}
}
