package sessions_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
	"github.com/n2jsoft-public-org/airlock/internal/sessions"
	"github.com/n2jsoft-public-org/airlock/internal/sessions/sessionstest"
)

// fakeTimers fires the scheduled functions when the test says so.
type fakeTimers struct {
	pending []func()
	delays  []time.Duration
	mu      sync.Mutex
}

func (f *fakeTimers) AfterFunc(d time.Duration, fn func()) func() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending, f.delays = append(f.pending, fn), append(f.delays, d)
	index := len(f.pending) - 1
	return func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		stopped := f.pending[index] != nil
		f.pending[index] = nil
		return stopped
	}
}

// fire runs the last scheduled function, if not stopped.
func (f *fakeTimers) fire() {
	f.mu.Lock()
	fn := f.pending[len(f.pending)-1]
	f.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (f *fakeTimers) scheduled() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.delays...)
}

func newReconnectManager(t *testing.T) (*sessions.Manager, *sessionstest.Runner, *sessionstest.Launcher, *fakeTimers) {
	t.Helper()
	runner, launcher, timers := sessionstest.NewRunner(), sessionstest.NewLauncher(), &fakeTimers{}
	m := sessions.NewManager(context.Background(), sessions.Config{Runner: sessions.Fixed(runner), Launch: launcher.Launch, AfterFunc: timers.AfterFunc})
	t.Cleanup(m.Shutdown)
	return m, runner, launcher, timers
}

func TestDroppedTunnelReconnectsInPlace(t *testing.T) {
	m, runner, launcher, timers := newReconnectManager(t)
	runner.AWS("archive-db", "rds")
	events := subscribe(t, m)
	id := mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db"})
	first := launcher.Next(t)
	first.Print("Waiting for connections...")
	sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Session != nil && ev.Session.Ready })
	first.Exit(errors.New("connection lost"))
	sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Session != nil && !ev.Session.ReconnectAt.IsZero() })
	if delays := timers.scheduled(); delays[0] != sessions.ReconnectDelays[0] {
		t.Fatalf("delay = %v", delays[0])
	}

	timers.fire()
	replaced := sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventReplaced })
	if replaced.ID != id || replaced.Session.Reconnects != 1 {
		t.Fatalf("replaced = %+v", replaced.Session)
	}
	if prepared := runner.PreparedOptions(); !prepared[len(prepared)-1].Unattended {
		t.Fatal("an automatic reconnection never signs in interactively")
	}
	launcher.Next(t)
}

func TestReconnectionNeedingSignInWaitsForTheUser(t *testing.T) {
	m, runner, launcher, timers := newReconnectManager(t)
	runner.AWS("archive-db", "rds")
	events := subscribe(t, m)
	id := mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db"})
	process := launcher.Next(t)
	process.Print("Waiting for connections...")
	sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Session != nil && ev.Session.Ready })
	runner.Errors["archive-db"] = jumphost.ErrSignInRequired
	process.Exit(errors.New("connection lost"))
	sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Session != nil && !ev.Session.ReconnectAt.IsZero() })

	timers.fire()
	sessionstest.Wait(t, events, sessionstest.Line(id, "Signing in again needs you: press r to reconnect."))
}

func TestStoppedSessionIsNotReconnected(t *testing.T) {
	m, runner, launcher, timers := newReconnectManager(t)
	runner.AWS("archive-db", "rds")
	events := subscribe(t, m)
	id := mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db"})
	process := launcher.Next(t)
	process.Print("Waiting for connections...")
	sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Session != nil && ev.Session.Ready })
	if err := m.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	sessionstest.Wait(t, events, sessionstest.State(id, sessions.StateEnded))
	sleep()
	if delays := timers.scheduled(); len(delays) != 0 {
		t.Fatal("a stopped session must not be reconnected")
	}
}

func TestRestartKeepsTheIDAndTheLog(t *testing.T) {
	m, runner, launcher, _ := newReconnectManager(t)
	runner.AWS("archive-db", "rds")
	events := subscribe(t, m)
	id := mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db", DBUser: "app_ro"})
	launcher.Next(t).Exit(errors.New("exit status 1"))
	sessionstest.Wait(t, events, sessionstest.State(id, sessions.StateEnded))

	if err := m.Restart(t.Context(), id, nil); err != nil {
		t.Fatal(err)
	}
	replaced := sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventReplaced })
	lines := replaced.Session.Lines
	if replaced.ID != id || lines[0].Text == "Restarting session..." {
		t.Fatalf("the previous log must stay above the restart: %+v", lines)
	}
	prepared := runner.PreparedOptions()
	if last := prepared[len(prepared)-1]; last.LocalPortNumber != 15432 || last.DBUser != "app_ro" || last.AwsProfile != "dev" {
		t.Fatalf("restart options = %+v", last)
	}
	if err := m.Restart(t.Context(), id, nil); err == nil {
		t.Fatal("a running session cannot be restarted")
	}
	if err := m.Restart(t.Context(), 999, nil); !errors.Is(err, sessions.ErrUnknownSession) {
		t.Fatalf("err = %v", err)
	}
}

func TestRestartRefusesATargetConnectedElsewhere(t *testing.T) {
	m, runner, launcher, _ := newReconnectManager(t)
	runner.AWS("archive-db", "rds")
	events := subscribe(t, m)
	first := mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db"})
	launcher.Next(t).Exit(errors.New("exit status 1"))
	sessionstest.Wait(t, events, sessionstest.State(first, sessions.StateEnded))
	mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db"})
	launcher.Next(t)

	if err := m.Restart(t.Context(), first, nil); err == nil || err.Error() != "archive-db is already connected in another session" {
		t.Fatalf("err = %v", err)
	}
}

// fakeClock is a Config.Now the test moves forward.
type fakeClock struct {
	now time.Time
	mu  sync.Mutex
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// waitScheduled waits until n reconnections were scheduled: the manager schedules them after it
// published what the test waits for.
func waitScheduled(t *testing.T, timers *fakeTimers, n int) []time.Duration {
	t.Helper()
	for range 500 {
		if delays := timers.scheduled(); len(delays) >= n {
			return delays
		}
		sleep()
	}
	t.Fatalf("scheduled = %v, want %d reconnections", timers.scheduled(), n)
	return nil
}

// dropReady makes the next aws process listen, then exit on its own.
func dropReady(t *testing.T, launcher *sessionstest.Launcher, events <-chan sessions.Event) {
	t.Helper()
	process := launcher.Next(t)
	process.Print("Waiting for connections...")
	sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Session != nil && ev.Session.Ready })
	process.Exit(errors.New("connection lost"))
}

// dropped is archive-db, which worked then dropped: its first reconnection is scheduled.
type dropped struct {
	m        *sessions.Manager
	runner   *sessionstest.Runner
	launcher *sessionstest.Launcher
	timers   *fakeTimers
	events   <-chan sessions.Event
	id       sessions.ID
}

func openDropped(t *testing.T) dropped {
	t.Helper()
	m, runner, launcher, timers := newReconnectManager(t)
	runner.AWS("archive-db", "rds")
	events := subscribe(t, m)
	id := mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db"})
	dropReady(t, launcher, events)
	waitScheduled(t, timers, 1)
	return dropped{m: m, runner: runner, launcher: launcher, timers: timers, events: events, id: id}
}

func TestDirectSessionIsNeverReconnected(t *testing.T) {
	m, runner, _, timers := newReconnectManager(t)
	runner.Direct("billing").SignIn = &fakeSignIn{candidates: []jumphost.SignInCandidate{{Label: "password", User: "app"}},
		failures: map[string]error{"password": jumphost.ErrSignInRejected}}
	events := subscribe(t, m)
	mustOpen(t, m, &jumphost.Options{ResourceType: "direct", Name: "billing"})

	// The rejected password ends the session on its own, which drops nothing to reconnect.
	sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventPasswordRejected })
	sleep()
	if s := snapshotOf(t, m).Sessions[0]; s.Active() || s.ReconnectPending() || len(timers.scheduled()) != 0 {
		t.Fatalf("session = %+v scheduled = %v, a direct session has no tunnel to reconnect", s, timers.scheduled())
	}
}

func TestSessionThatNeverWorkedIsNotReconnected(t *testing.T) {
	m, runner, launcher, timers := newReconnectManager(t)
	runner.AWS("archive-db", "rds")
	events := subscribe(t, m)
	id := mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db"})
	launcher.Next(t).Exit(errors.New("TargetNotConnected"))
	sessionstest.Wait(t, events, sessionstest.State(id, sessions.StateEnded))
	sleep()
	if s := snapshotOf(t, m).Sessions[0]; s.ReconnectPending() || len(timers.scheduled()) != 0 {
		t.Fatal("a session that never listened was scheduled for reconnection: its settings are likely wrong")
	}
}

func TestFailedReconnectionIsRetriedThenGivesUp(t *testing.T) {
	d := openDropped(t)
	runner, launcher, timers, id, events := d.runner, d.launcher, d.timers, d.id, d.events
	runner.Errors["archive-db"] = errors.New("could not connect to the endpoint URL")

	for attempt := 1; attempt <= len(sessions.ReconnectDelays); attempt++ {
		timers.fire()
		sessionstest.Wait(t, events, sessionstest.Line(id, "Automatic reconnection failed: could not connect to the endpoint URL"))
		if attempt < len(sessions.ReconnectDelays) {
			// A network still down, as after a laptop wakes up, is retried after a longer wait.
			if delays := waitScheduled(t, timers, attempt+1); delays[attempt] != sessions.ReconnectDelays[attempt] {
				t.Fatalf("attempt %d: delays = %v, want %v next", attempt, delays, sessions.ReconnectDelays[attempt])
			}
		}
	}
	sessionstest.Wait(t, events, sessionstest.Line(id, "Automatic reconnection gave up after 5 attempts: press r to reconnect."))
	sleep()
	if delays := timers.scheduled(); len(delays) != len(sessions.ReconnectDelays) || launcher.Pending() != 0 {
		t.Fatalf("delays = %v, want no attempt past the last one", delays)
	}
}

func TestSessionThatLastedGetsFreshAttempts(t *testing.T) {
	for _, tc := range []struct {
		uptime time.Duration
		want   time.Duration
	}{{0, sessions.ReconnectDelays[1]}, {2 * time.Minute, sessions.ReconnectDelays[0]}} {
		clock := &fakeClock{now: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)}
		runner, launcher, timers := sessionstest.NewRunner(), sessionstest.NewLauncher(), &fakeTimers{}
		m := sessions.NewManager(context.Background(), sessions.Config{Runner: sessions.Fixed(runner), Launch: launcher.Launch, AfterFunc: timers.AfterFunc, Now: clock.Now})
		t.Cleanup(m.Shutdown)
		runner.AWS("archive-db", "rds")
		events := subscribe(t, m)
		mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db"})
		dropReady(t, launcher, events)
		waitScheduled(t, timers, 1)
		timers.fire()
		sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventReplaced })

		// The reconnected session drops again, after running for tc.uptime.
		process := launcher.Next(t)
		process.Print("Waiting for connections...")
		clock.advance(tc.uptime)
		process.Exit(errors.New("connection lost"))
		if delays := waitScheduled(t, timers, 2); delays[1] != tc.want {
			t.Fatalf("uptime %v: next delay = %v, want %v", tc.uptime, delays[1], tc.want)
		}
	}
}

func TestStopOrCloseCancelsTheScheduledReconnection(t *testing.T) {
	d := openDropped(t)
	m, runner, launcher, timers, id, events := d.m, d.runner, d.launcher, d.timers, d.id, d.events
	sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Session != nil && ev.Session.ReconnectPending() })
	if err := m.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	// The dashboards learn that the reconnection is off.
	sessionstest.Wait(t, events, func(ev sessions.Event) bool {
		return ev.ID == id && ev.Session != nil && !ev.Session.ReconnectPending()
	})
	timers.fire()
	sleep()
	if len(runner.PreparedOptions()) != 1 || launcher.Pending() != 0 {
		t.Fatalf("prepared = %d, a stopped session was reconnected", len(runner.PreparedOptions()))
	}

	d = openDropped(t)
	m, runner, launcher, timers, id = d.m, d.runner, d.launcher, d.timers, d.id
	if err := m.Remove(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	timers.fire()
	sleep()
	if len(runner.PreparedOptions()) != 1 || launcher.Pending() != 0 || len(snapshotOf(t, m).Sessions) != 0 {
		t.Fatal("a closed session came back")
	}
}

func TestManualRestartTakesOverTheScheduledReconnection(t *testing.T) {
	d := openDropped(t)
	m, runner, launcher, timers, id, events := d.m, d.runner, d.launcher, d.timers, d.id, d.events
	if err := m.Restart(t.Context(), id, nil); err != nil {
		t.Fatal(err)
	}
	replaced := sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventReplaced })
	if replaced.Session.Reconnects != 0 {
		t.Fatalf("replaced = %+v, want the restart by hand", replaced.Session)
	}
	launcher.Next(t)
	timers.fire()
	sleep()
	if len(runner.PreparedOptions()) != 2 || launcher.Pending() != 0 {
		t.Fatalf("prepared = %d, the scheduled reconnection still ran", len(runner.PreparedOptions()))
	}
}

// reconnecting fires the scheduled reconnection, whose preparation blocks until block closes.
func reconnecting(t *testing.T, runner *sessionstest.Runner, timers *fakeTimers, id sessions.ID, events <-chan sessions.Event) chan struct{} {
	t.Helper()
	block := make(chan struct{})
	runner.Block = block
	timers.fire()
	sessionstest.Wait(t, events, func(ev sessions.Event) bool {
		return ev.ID == id && ev.Session != nil && ev.Session.Reconnecting
	})
	return block
}

func TestClosingCancelsTheReconnectionInFlight(t *testing.T) {
	d := openDropped(t)
	m, runner, launcher, timers, id, events := d.m, d.runner, d.launcher, d.timers, d.id, d.events
	block := reconnecting(t, runner, timers, id, events)
	if err := m.Remove(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	close(block)
	sleep()
	if launcher.Pending() != 0 || len(snapshotOf(t, m).Sessions) != 0 {
		t.Fatal("the reconnection of a closed session went on")
	}
}

func TestManualRestartTakesOverTheReconnectionInFlight(t *testing.T) {
	d := openDropped(t)
	m, runner, launcher, timers, id, events := d.m, d.runner, d.launcher, d.timers, d.id, d.events
	block := reconnecting(t, runner, timers, id, events)
	restarted := make(chan error, 1)
	go func() { restarted <- m.Restart(t.Context(), id, nil) }()
	for len(runner.PreparedOptions()) < 3 {
		sleep()
	}
	close(block)
	if err := <-restarted; err != nil {
		t.Fatal(err)
	}
	replaced := sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventReplaced })
	if replaced.Session.Reconnects != 0 || replaced.Session.Reconnecting {
		t.Fatalf("replaced = %+v, want the restart by hand", replaced.Session)
	}
	launcher.Next(t)
	sleep()
	if launcher.Pending() != 0 {
		t.Fatal("the canceled reconnection opened a second session")
	}
	for _, line := range snapshotOf(t, m).Sessions[0].Lines {
		if strings.HasPrefix(line.Text, "Reconnected automatically") {
			t.Fatalf("the canceled reconnection replaced the restart: %q", line.Text)
		}
	}
}
