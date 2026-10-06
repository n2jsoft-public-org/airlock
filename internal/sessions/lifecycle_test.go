package sessions_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/sessions/sessionstest"
)

func snapshotOf(t *testing.T, m *sessions.Manager) sessions.Snapshot {
	t.Helper()
	snapshot, err := m.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func subscribe(t *testing.T, m *sessions.Manager) <-chan sessions.Event {
	t.Helper()
	_, events, err := m.Subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func mustOpen(t *testing.T, m *sessions.Manager, options *jumphost.Options) sessions.ID {
	t.Helper()
	id, err := m.Open(t.Context(), sessions.OpenRequest{Options: *options})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newManager(t *testing.T) (*sessions.Manager, *sessionstest.Runner, *sessionstest.Launcher) {
	t.Helper()
	runner, launcher := sessionstest.NewRunner(), sessionstest.NewLauncher()
	m := sessions.NewManager(context.Background(), sessions.Config{Runner: sessions.Fixed(runner), Launch: launcher.Launch})
	t.Cleanup(m.Shutdown)
	return m, runner, launcher
}

func TestOpenRunsTheAWSProcessUntilItExits(t *testing.T) {
	m, runner, launcher := newManager(t)
	runner.AWS("archive-db", "rds")
	events := subscribe(t, m)

	id, err := m.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: "rds", Name: "archive-db"}, Warning: "slow"})
	if err != nil {
		t.Fatal(err)
	}
	added := sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventAdded })
	if added.ID != id || added.Session.State != sessions.StateStarting || added.Session.Lines[len(added.Session.Lines)-1].Text != "Starting session..." {
		t.Fatalf("added = %+v", added.Session)
	}
	process := launcher.Next(t)
	sessionstest.Wait(t, events, sessionstest.State(id, sessions.StateRunning))
	process.Print("Waiting for connections...")
	ready := sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Session != nil && ev.Session.Ready })
	if ready.Session.State != sessions.StateRunning {
		t.Fatalf("ready = %+v", ready.Session)
	}
	process.Exit(errors.New("exit status 255"))
	sessionstest.Wait(t, events, sessionstest.Line(id, "Session ended with error: exit status 255"))
	ended := sessionstest.Wait(t, events, sessionstest.State(id, sessions.StateEnded))
	if ended.Session.Err != "exit status 255" {
		t.Fatalf("ended = %+v", ended.Session)
	}
}

func TestOpenNeverShowsTheTypedPassword(t *testing.T) {
	m, runner, _ := newManager(t)
	runner.Direct("billing")
	id, err := m.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: "direct", Name: "billing", DBPassword: "s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotOf(t, m)
	if snapshot.Sessions[0].ID != id || snapshot.Sessions[0].Options.DBPassword != "" {
		t.Fatalf("snapshot options = %+v", snapshot.Sessions[0].Options)
	}
	if got := runner.PreparedOptions()[0].DBPassword; got != "s3cret" {
		t.Fatalf("the runner must still get the password, got %q", got)
	}
}

func TestDirectSessionRunsAtOnceAndStopsWithoutProcess(t *testing.T) {
	m, runner, _ := newManager(t)
	runner.Direct("billing")
	id, err := m.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: "direct", Name: "billing"}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotOf(t, m)
	if s := snapshot.Sessions[0]; s.State != sessions.StateRunning || !s.Ready {
		t.Fatalf("session = %+v", s)
	}
	if err := m.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	snapshot = snapshotOf(t, m)
	if s := snapshot.Sessions[0]; s.State != sessions.StateEnded || !s.StopRequested {
		t.Fatalf("session = %+v", s)
	}
	if err := m.Remove(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if snapshot := snapshotOf(t, m); len(snapshot.Sessions) != 0 {
		t.Fatalf("sessions = %+v", snapshot.Sessions)
	}
}

func TestStopTerminatesTheProcess(t *testing.T) {
	m, runner, launcher := newManager(t)
	runner.AWS("archive-db", "rds")
	events := subscribe(t, m)
	id := mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db"})
	process := launcher.Next(t)
	sessionstest.Wait(t, events, sessionstest.State(id, sessions.StateRunning))

	if err := m.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	sessionstest.Wait(t, events, sessionstest.Line(id, "Session stopped."))
	if !process.Terminated() {
		t.Fatal("the aws process must be terminated")
	}
	if err := m.Remove(t.Context(), 999); !errors.Is(err, sessions.ErrUnknownSession) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenCanceledDuringPreparationReleasesTheSession(t *testing.T) {
	m, runner, _ := newManager(t)
	runner.AWS("archive-db", "rds")
	runner.Block = make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := m.Open(ctx, sessions.OpenRequest{Options: jumphost.Options{ResourceType: "rds", Name: "archive-db"}})
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if snapshot := snapshotOf(t, m); len(snapshot.Sessions) != 0 {
		t.Fatalf("a canceled opening must add no session: %+v", snapshot.Sessions)
	}
}

func TestStopAllWaitsAndRefusesNewSessions(t *testing.T) {
	m, runner, launcher := newManager(t)
	runner.AWS("a", "rds")
	runner.AWS("b", "rds")
	mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "a"})
	mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "b"})
	launcher.Next(t)
	launcher.Next(t)

	if err := m.StopAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotOf(t, m)
	for _, s := range snapshot.Sessions {
		if s.Active() {
			t.Fatalf("session still active: %+v", s)
		}
	}
	if _, err := m.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: "rds", Name: "a"}}); !errors.Is(err, sessions.ErrClosed) {
		t.Fatalf("err = %v", err)
	}
}

func TestShutdownRecordsTheCleanup(t *testing.T) {
	m, runner, launcher := newManager(t)
	runner.AWS("archive-db", "rds")
	events := subscribe(t, m)
	id := mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db"})
	process := launcher.Next(t)
	// Running: the manager holds the process. A Shutdown during the launch also stops it, but
	// from the launch, once Shutdown has returned.
	sessionstest.Wait(t, events, sessionstest.State(id, sessions.StateRunning))
	m.Shutdown()
	if !process.Terminated() {
		t.Fatal("Shutdown must terminate the aws processes")
	}
}

func TestLogIsCapped(t *testing.T) {
	m, runner, launcher := newManager(t)
	runner.AWS("archive-db", "rds")
	mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db"})
	process := launcher.Next(t)
	for range sessions.MaxLogLines + 50 {
		process.Print("noise")
	}
	process.Print("last")
	// Print is synchronous: the log already holds "last" (a subscriber would lag past its buffer here).
	snapshot := snapshotOf(t, m)
	if lines := snapshot.Sessions[0].Lines; len(lines) != sessions.MaxLogLines || lines[len(lines)-1].Text != "last" {
		t.Fatalf("lines = %d", len(lines))
	}
}

func sleep() { time.Sleep(10 * time.Millisecond) }

func TestConnectionAcceptedCountsConnections(t *testing.T) {
	m, runner, launcher := newManager(t)
	runner.AWS("archive-db", "rds")
	mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db"})
	process := launcher.Next(t)

	// Print is synchronous: the snapshot already holds what the line changed.
	process.Print("Connection accepted for session [abc]")
	if s := snapshotOf(t, m).Sessions[0]; !s.Ready || s.Connections != 1 {
		t.Fatalf("ready = %v connections = %d, want ready with 1 connection", s.Ready, s.Connections)
	}
	process.Print("Connection accepted for session [def]")
	if s := snapshotOf(t, m).Sessions[0]; s.Connections != 2 {
		t.Fatalf("connections = %d, want 2", s.Connections)
	}
}

func TestOpeningWarningEndsTheIntro(t *testing.T) {
	m, runner, _ := newManager(t)
	runner.AWS("archive-db", "rds")
	if _, err := m.Open(t.Context(), sessions.OpenRequest{Options: jumphost.Options{ResourceType: "rds", Name: "archive-db"}, Warning: "slow"}); err != nil {
		t.Fatal(err)
	}
	lines := snapshotOf(t, m).Sessions[0].Lines
	texts := make([]string, 0, len(lines))
	for _, line := range lines {
		texts = append(texts, line.Text)
	}
	if want := []string{"Connecting to archive-db", "Warning: slow", "", "Starting session..."}; !slices.Equal(texts, want) {
		t.Fatalf("lines = %q, want %q", texts, want)
	}
}

func TestRestartedDirectSessionRunsAgain(t *testing.T) {
	m, runner, _ := newManager(t)
	runner.Direct("billing")
	events := subscribe(t, m)
	id := mustOpen(t, m, &jumphost.Options{ResourceType: "direct", Name: "billing", DBPassword: "s3cret"})
	if err := m.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	sessionstest.Wait(t, events, sessionstest.State(id, sessions.StateEnded))

	if err := m.Restart(t.Context(), id, nil); err != nil {
		t.Fatal(err)
	}
	sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventReplaced && ev.ID == id })
	s := snapshotOf(t, m).Sessions[0]
	if s.ID != id || s.State != sessions.StateRunning || !s.Ready || s.StopRequested {
		t.Fatalf("session = %+v, want it running again in its slot", s)
	}
	if prepared := runner.PreparedOptions(); len(prepared) != 2 || prepared[1].DBPassword != "s3cret" {
		t.Fatalf("prepared = %+v, want the restart prepared with the typed password", prepared)
	}
}
