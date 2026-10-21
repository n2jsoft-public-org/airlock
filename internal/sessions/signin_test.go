package sessions_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
	"github.com/n2jsoft/airlock/internal/sessions/sessionstest"
)

// fakeSignIn answers Candidates with candidates and Check with failures[c.Label].
type fakeSignIn struct {
	failures map[string]error
	// block, when not nil, makes Check wait for it.
	block      chan struct{}
	candidates []jumphost.SignInCandidate
	// afterForget replaces candidates once Forget applied.
	afterForget []jumphost.SignInCandidate
	used        []string
	listed      int
	mu          sync.Mutex
	forget      bool
}

func (f *fakeSignIn) Candidates(context.Context) ([]jumphost.SignInCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listed++
	return f.candidates, nil
}

func (f *fakeSignIn) Check(_ context.Context, c jumphost.SignInCandidate) error {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failures[c.Label]
}

func (f *fakeSignIn) Use(c jumphost.SignInCandidate, options *jumphost.Options) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.used = append(f.used, c.Label)
	options.DBUser = c.User
}

func (f *fakeSignIn) Forget(_ jumphost.SignInCandidate, options *jumphost.Options) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forget {
		options.DBSecretARN = ""
		if f.afterForget != nil {
			f.candidates = f.afterForget
		}
	}
	return f.forget
}

// usedLabels returns the candidates Use was called with.
func (f *fakeSignIn) usedLabels() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.used...)
}

func openWithSignIn(t *testing.T, checker *fakeSignIn) (*sessions.Manager, *sessionstest.Runner, sessions.ID, <-chan sessions.Event) {
	t.Helper()
	m, runner, _ := newManager(t)
	runner.Direct("billing").SignIn = checker
	events := subscribe(t, m)
	id := mustOpen(t, m, &jumphost.Options{Name: "billing", ResourceType: "direct"})
	return m, runner, id, events
}

func signIn(id sessions.ID, state sessions.SignInState) func(sessions.Event) bool {
	return func(ev sessions.Event) bool {
		return ev.ID == id && ev.Session != nil && ev.Session.SignIn.State == state
	}
}

func TestSingleCandidateIsCheckedAndRememberedWhenDiscovered(t *testing.T) {
	checker := &fakeSignIn{candidates: []jumphost.SignInCandidate{{Label: "ro", User: "app_ro", Discovered: true}}}
	_, runner, id, events := openWithSignIn(t, checker)

	// The log line precedes the state change, and Wait skips what it does not match.
	sessionstest.Wait(t, events, sessionstest.Line(id, "Signed in as app_ro: the test query succeeded."))
	done := sessionstest.Wait(t, events, signIn(id, sessions.SignInDone))
	if done.Session.SignIn.User != "app_ro" || done.Session.Options.DBUser != "app_ro" {
		t.Fatalf("session = %+v", done.Session)
	}
	waitSaved(t, runner)
}

// M6: an agent's opening, or a stack's, never changes the user's remembered choices.
func TestAgentAndStackSignInsAreNotRemembered(t *testing.T) {
	for _, options := range []jumphost.Options{
		{Name: "billing", ResourceType: "direct", OpenedBy: "claude-code"},
		{Name: "billing", ResourceType: "direct", Stack: "/repo/.airlock.json#dev", MCPAllowWrites: true},
	} {
		checker := &fakeSignIn{candidates: []jumphost.SignInCandidate{{Label: "ro", User: "app_ro", Discovered: true}}}
		m, runner, _ := newManager(t)
		runner.Direct("billing").SignIn = checker
		events := subscribe(t, m)
		id := mustOpen(t, m, &options)
		sessionstest.Wait(t, events, signIn(id, sessions.SignInDone))
		for range 20 {
			sleep()
		}
		if saved := runner.SavedOptions(); len(saved) != 0 {
			t.Fatalf("saved = %+v, want nothing saved for %+v", saved, options)
		}
	}
}

func TestSeveralCandidatesWaitForAChoice(t *testing.T) {
	checker := &fakeSignIn{candidates: []jumphost.SignInCandidate{{Label: "a", User: "u1"}, {Label: "b", User: "u2"}},
		failures: map[string]error{"a": errors.New("bad password")}}
	m, _, id, events := openWithSignIn(t, checker)

	sessionstest.Wait(t, events, signIn(id, sessions.SignInChoosing))
	if err := m.ChooseSignIn(t.Context(), id, 0); err != nil {
		t.Fatal(err)
	}
	// a fails, b is left: the choice is asked again.
	choosing := sessionstest.Wait(t, events, signIn(id, sessions.SignInChoosing))
	if choosing.Session.SignIn.Failed[sessions.CandidateKey(&checker.candidates[0])] != "bad password" {
		t.Fatalf("failed = %+v", choosing.Session.SignIn.Failed)
	}
	if err := m.SkipSignIn(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	sessionstest.Wait(t, events, signIn(id, sessions.SignInSkipped))
	if err := m.ChooseSignIn(t.Context(), id, 1); err != nil {
		t.Fatal(err)
	}
	sessionstest.Wait(t, events, signIn(id, sessions.SignInDone))
	if err := m.ChooseSignIn(t.Context(), id, 0); err == nil {
		t.Fatal("a choice is refused once signed in")
	}
}

func TestRejectedDirectPasswordEndsTheSession(t *testing.T) {
	checker := &fakeSignIn{candidates: []jumphost.SignInCandidate{{Label: "password", User: "app"}},
		failures: map[string]error{"password": jumphost.ErrSignInRejected}}
	_, _, id, events := openWithSignIn(t, checker)

	rejected := sessionstest.Wait(t, events, func(ev sessions.Event) bool { return ev.Kind == sessions.EventPasswordRejected })
	if rejected.ID != id || rejected.Name != "billing" || rejected.User != "app" {
		t.Fatalf("event = %+v", rejected)
	}
}

func TestAllCandidatesFailing(t *testing.T) {
	checker := &fakeSignIn{candidates: []jumphost.SignInCandidate{{Label: "a", User: "u"}},
		failures: map[string]error{"a": errors.New("timeout")}}
	m, _, id, events := openWithSignIn(t, checker)

	sessionstest.Wait(t, events, sessionstest.Line(id, "The tunnel stays open: press a to sign in again."))
	failed := sessionstest.Wait(t, events, signIn(id, sessions.SignInFailed))
	if failed.Session.SignIn.Err != "timeout" {
		t.Fatalf("session = %+v", failed.Session)
	}
	checker.mu.Lock()
	checker.failures = nil
	checker.mu.Unlock()
	if err := m.RetrySignIn(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	sessionstest.Wait(t, events, signIn(id, sessions.SignInDone))
}

func TestTunnelSignInWaitsForReadiness(t *testing.T) {
	checker := &fakeSignIn{candidates: []jumphost.SignInCandidate{{Label: "iam", User: "app"}}}
	m, runner, launcher := newManager(t)
	s := runner.AWS("archive-db", "rds")
	s.SignIn = checker
	events := subscribe(t, m)
	id := mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db"})
	process := launcher.Next(t)
	if snapshotOf(t, m).Sessions[0].SignIn.State != sessions.SignInIdle {
		t.Fatal("the sign-in must wait for the tunnel")
	}
	process.Print("Waiting for connections...")
	sessionstest.Wait(t, events, signIn(id, sessions.SignInDone))
}

func TestChoiceIsRefusedOnceTheSessionEnded(t *testing.T) {
	checker := &fakeSignIn{candidates: []jumphost.SignInCandidate{{Label: "a", User: "u1"}, {Label: "b", User: "u2"}}}
	m, runner, launcher := newManager(t)
	runner.AWS("archive-db", "rds").SignIn = checker
	events := subscribe(t, m)
	id := mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db"})
	process := launcher.Next(t)
	process.Print("Waiting for connections...")
	sessionstest.Wait(t, events, signIn(id, sessions.SignInChoosing))
	if err := m.SkipSignIn(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	sessionstest.Wait(t, events, signIn(id, sessions.SignInSkipped))
	process.Exit(nil)
	sessionstest.Wait(t, events, sessionstest.State(id, sessions.StateEnded))
	if err := m.ChooseSignIn(t.Context(), id, 0); err == nil {
		t.Fatal("a choice is refused on an ended session")
	}
	if got := snapshotOf(t, m).Sessions[0].SignIn.State; got != sessions.SignInSkipped {
		t.Fatalf("sign-in state = %v", got)
	}
}

func waitSaved(t *testing.T, runner *sessionstest.Runner) {
	t.Helper()
	for range 100 {
		if runner.SavedCount() > 0 {
			return
		}
		sleep()
	}
	t.Fatal("the sign-in was not remembered")
}

func TestRejectedSavedSecretIsForgottenAndTheCredentialsListedAgain(t *testing.T) {
	saved := jumphost.SignInCandidate{Label: "saved", User: "app_rw", SecretARN: "arn:saved"}
	checker := &fakeSignIn{
		candidates: []jumphost.SignInCandidate{saved}, forget: true,
		afterForget: []jumphost.SignInCandidate{saved, {Label: "ro-a", User: "ro"}, {Label: "ro-b", User: "ro"}},
		failures:    map[string]error{"saved": jumphost.ErrSignInRejected},
	}
	m, runner, launcher := newManager(t)
	runner.AWS("archive-db", "rds").SignIn = checker
	events := subscribe(t, m)
	id := mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db", DBSecretARN: "arn:saved"})
	launcher.Next(t).Print("Waiting for connections...")

	sessionstest.Wait(t, events, sessionstest.Line(id, "Forgot the saved secret saved."))
	choosing := sessionstest.Wait(t, events, signIn(id, sessions.SignInChoosing))
	if got := choosing.Session.SignIn; len(got.Candidates) != 3 || len(got.Untried()) != 2 {
		t.Fatalf("sign-in = %+v, want the read-only secrets offered, the saved one marked failed", got)
	}
	if choosing.Session.Options.DBSecretARN != "" {
		t.Fatalf("options = %+v, want the forgotten secret gone", choosing.Session.Options)
	}
	waitSaved(t, runner)
	if all := runner.SavedOptions(); all[len(all)-1].DBSecretARN != "" || all[len(all)-1].Name != "archive-db" {
		t.Fatalf("saved = %+v, want the target remembered without its secret", all)
	}
}

func TestLateCheckResultIsDropped(t *testing.T) {
	checker := &fakeSignIn{candidates: []jumphost.SignInCandidate{{Label: "ro", User: "app_ro", Discovered: true}}, block: make(chan struct{})}
	m, runner, launcher := newManager(t)
	runner.AWS("archive-db", "rds").SignIn = checker
	events := subscribe(t, m)
	id := mustOpen(t, m, &jumphost.Options{ResourceType: "rds", Name: "archive-db"})
	launcher.Next(t).Print("Waiting for connections...")
	sessionstest.Wait(t, events, signIn(id, sessions.SignInChecking))

	if err := m.Stop(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	sessionstest.Wait(t, events, sessionstest.State(id, sessions.StateEnded))
	close(checker.block) // the check succeeds, too late
	sleep()
	s := snapshotOf(t, m).Sessions[0]
	if s.SignIn.State != sessions.SignInIdle || s.SignIn.User != "" || len(checker.usedLabels()) != 0 || runner.SavedCount() != 0 {
		t.Fatalf("sign-in = %+v used = %v saved = %d, want the late result dropped", s.SignIn, checker.usedLabels(), runner.SavedCount())
	}
	for _, line := range s.Lines {
		if strings.HasPrefix(line.Text, "Signed in as") {
			t.Fatalf("a late result was logged: %q", line.Text)
		}
	}
}

func TestChoiceOfTheFirstSubscriberReachesTheSecond(t *testing.T) {
	checker := &fakeSignIn{candidates: []jumphost.SignInCandidate{{Label: "a", User: "u1"}, {Label: "b", User: "u2"}}}
	m, _, id, first := openWithSignIn(t, checker)
	second := subscribe(t, m)

	sessionstest.Wait(t, first, signIn(id, sessions.SignInChoosing))
	if err := m.ChooseSignIn(t.Context(), id, 1); err != nil {
		t.Fatal(err)
	}
	// The second view never chose: it sees the outcome of the first one's choice.
	done := sessionstest.Wait(t, second, signIn(id, sessions.SignInDone))
	if done.Session.SignIn.User != "u2" || done.Session.SignIn.Err != "" {
		t.Fatalf("sign-in = %+v", done.Session.SignIn)
	}
	sessionstest.Wait(t, first, signIn(id, sessions.SignInDone))
}

// airlock up takes over an agent's session: the gateway no longer sees it as the agent's, and a
// reconnection keeps it the stack's.
func TestTakeOverGivesAnAgentSessionToTheStack(t *testing.T) {
	m, runner, _ := newManager(t)
	runner.Direct("billing")
	id := mustOpen(t, m, &jumphost.Options{Name: "billing", ResourceType: "direct", OpenedBy: "claude-code"})
	if err := m.TakeOver(t.Context(), id, "/repo/.airlock.json#dev"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := m.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s := snapshot.Sessions[0]; s.OpenedBy != "" || s.Options.OpenedBy != "" || s.Options.Stack != "/repo/.airlock.json#dev" {
		t.Fatalf("session = %+v", s)
	}
	if err := m.TakeOver(t.Context(), id+1, ""); !errors.Is(err, sessions.ErrUnknownSession) {
		t.Fatalf("unknown session: %v", err)
	}
}
