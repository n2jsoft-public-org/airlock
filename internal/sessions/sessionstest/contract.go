package sessionstest

import (
	"context"
	"errors"
	"testing"

	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// ServiceFactory returns a service over runner and launcher, cleaned up with t.
type ServiceFactory func(t *testing.T, runner *Runner, launcher *Launcher) sessions.Service

// RunContract checks what every sessions.Service does, whether in-process or through the daemon.
func RunContract(t *testing.T, newService ServiceFactory) {
	t.Run("open, follow and stop", func(t *testing.T) {
		runner, launcher := NewRunner(), NewLauncher()
		runner.AWS("archive-db", "rds")
		service := newService(t, runner, launcher)
		snapshot, events, err := service.Subscribe(t.Context())
		if err != nil || len(snapshot.Sessions) != 0 {
			t.Fatalf("snapshot = %+v, %v", snapshot, err)
		}
		id, err := service.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: "rds", Name: "archive-db", DBPassword: "pw"}})
		if err != nil {
			t.Fatal(err)
		}
		added := Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventAdded })
		if added.ID != id || added.Session.Options.DBPassword != "" {
			t.Fatalf("added = %+v", added.Session)
		}
		process := launcher.Next(t)
		process.Print("Waiting for connections...")
		Wait(t, events, Line(id, "Waiting for connections..."))
		if err := service.Stop(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		Wait(t, events, State(id, sessions.StateEnded))
		if err := service.Remove(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventRemoved && ev.ID == id })
	})

	t.Run("errors keep their identity", func(t *testing.T) {
		runner, launcher := NewRunner(), NewLauncher()
		runner.Errors["expired"] = jumphost.ErrSignInRequired
		service := newService(t, runner, launcher)
		if _, err := service.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{Name: "expired"}}); !errors.Is(err, jumphost.ErrSignInRequired) {
			t.Fatalf("err = %v", err)
		}
		if err := service.Stop(t.Context(), 999); !errors.Is(err, sessions.ErrUnknownSession) {
			t.Fatalf("err = %v", err)
		}
		if _, err := service.Credentials(t.Context(), 999); !errors.Is(err, sessions.ErrUnknownSession) {
			t.Fatalf("err = %v", err)
		}
		if err := service.SignInAWS(t.Context(), 999, nil); !errors.Is(err, sessions.ErrUnknownSession) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("canceled opening adds nothing", func(t *testing.T) {
		runner, launcher := NewRunner(), NewLauncher()
		runner.AWS("slow", "rds")
		runner.Block = make(chan struct{})
		service := newService(t, runner, launcher)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := service.Open(ctx, sessions.OpenRequest{Options: jumphost.Options{ResourceType: "rds", Name: "slow"}})
			done <- err
		}()
		for len(runner.PreparedOptions()) == 0 {
			sleepBriefly()
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
		snapshot, err := service.Snapshot(t.Context())
		if err != nil || len(snapshot.Sessions) != 0 {
			t.Fatalf("sessions = %+v", snapshot.Sessions)
		}
	})

	t.Run("interactive runs on the caller's side", func(t *testing.T) {
		runner, launcher := NewRunner(), NewLauncher()
		runner.Direct("billing")
		var ran []string
		runner.OnPrepare = func(ctx context.Context) error {
			return jumphost.RunInteractive(ctx, "sign in", "aws", "sso", "login")
		}
		service := newService(t, runner, launcher)
		_, err := service.Open(t.Context(), sessions.OpenRequest{
			Options: jumphost.Options{ResourceType: "direct", Name: "billing"},
			Interactive: func(_ context.Context, _, name string, args ...string) error {
				ran = append([]string{name}, args...)
				return nil
			},
		})
		if err != nil || len(ran) != 3 || ran[0] != "aws" {
			t.Fatalf("ran = %v, err = %v", ran, err)
		}
	})

	t.Run("stop all", func(t *testing.T) {
		runner, launcher := NewRunner(), NewLauncher()
		runner.Direct("a")
		runner.Direct("b")
		service := newService(t, runner, launcher)
		for _, name := range []string{"a", "b"} {
			if _, err := service.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: "direct", Name: name}}); err != nil {
				t.Fatal(err)
			}
		}
		if err := service.StopAll(t.Context()); err != nil {
			t.Fatal(err)
		}
		snapshot, err := service.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for i := range snapshot.Sessions {
			if snapshot.Sessions[i].Active() {
				t.Fatalf("active after StopAll: %+v", snapshot.Sessions[i])
			}
		}
	})

	t.Run("two subscriptions each see an event once", func(t *testing.T) {
		runner, launcher := NewRunner(), NewLauncher()
		runner.Direct("a")
		runner.Direct("b")
		service := newService(t, runner, launcher)
		ctxA, cancelA := context.WithCancel(t.Context())
		defer cancelA()
		_, eventsA, err := service.Subscribe(ctxA)
		if err != nil {
			t.Fatal(err)
		}
		_, eventsB, err := service.Subscribe(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		id, err := service.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: "direct", Name: "a"}})
		if err != nil {
			t.Fatal(err)
		}
		for _, events := range []<-chan sessions.Event{eventsA, eventsB} {
			Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventAdded && ev.ID == id })
		}
		cancelA()
		id2, err := service.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: "direct", Name: "b"}})
		if err != nil {
			t.Fatal(err)
		}
		adds := 0
		Wait(t, eventsB, func(ev sessions.Event) bool {
			if ev.Kind == sessions.EventAdded {
				if ev.ID == id {
					t.Fatalf("session %d added twice", id)
				}
				adds++
			}
			return ev.Kind == sessions.EventAdded && ev.ID == id2
		})
		if adds != 1 {
			t.Fatalf("adds = %d", adds)
		}
	})
}
