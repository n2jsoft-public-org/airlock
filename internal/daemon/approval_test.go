package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/sessions/sessionstest"
)

func TestApprovalThroughTheSocket(t *testing.T) {
	m := sessions.NewManager(context.Background(), sessions.Config{Runner: sessions.Fixed(sessionstest.NewRunner()), Launch: sessionstest.NewLauncher().Launch})
	t.Cleanup(m.Shutdown)
	_, client, _ := serve(t, m, ServerHooks{ViewsChanged: m.ViewsChanged})
	snapshot, events, err := client.Subscribe(t.Context())
	if err != nil || len(snapshot.Approvals) != 0 {
		t.Fatalf("snapshot = %+v, %v", snapshot, err)
	}
	for m.Views() != 1 {
		time.Sleep(10 * time.Millisecond)
	}
	type result struct {
		err   error
		grant sessions.Grant
	}
	requested := make(chan result, 1)
	go func() {
		grant, err := m.RequestApproval(context.Background(), sessions.Approval{Agent: "claude-code", Type: "rds", Target: "orders-db"})
		requested <- result{grant: grant, err: err}
	}()
	ev := sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventApprovalRequested })

	var ran []string
	answered := make(chan error, 1)
	go func() {
		answered <- client.ResolveApproval(context.Background(), ev.Approval.ID, true, func(_ context.Context, _, name string, args ...string) error {
			ran = append([]string{name}, args...)
			return nil
		})
	}()
	got := <-requested
	if got.err != nil || got.grant.Interactive == nil {
		t.Fatalf("grant = %+v, %v", got.grant, got.err)
	}
	if err := got.grant.Interactive(t.Context(), "sign in", "airlock", "sso-login"); err != nil || len(ran) != 2 {
		t.Fatalf("the interactive must run in the answering view: %v %v", ran, err)
	}
	got.grant.Done()
	if err := <-answered; err != nil {
		t.Fatal(err)
	}
	if err := client.ResolveApproval(t.Context(), ev.Approval.ID, false, nil); !errors.Is(err, sessions.ErrApprovalResolved) {
		t.Fatalf("err = %v, want ErrApprovalResolved", err)
	}
}
