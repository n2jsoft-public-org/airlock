package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

var (
	integ = jumphost.SignInCandidate{Label: "example-dev-integ", User: "app_ro_integ", SecretARN: "arn:integ", Discovered: true}
	mssql = jumphost.SignInCandidate{Label: "example-dev-mssql", User: "app_ro_mssql", SecretARN: "arn:mssql", Discovered: true}
)

// fakeSignIn is a SignInChecker answering from fixed candidates and check results by label.
type fakeSignIn struct {
	listErr    error
	checkErrs  map[string]error
	candidates []jumphost.SignInCandidate
	// afterForget replaces the candidates once Forget was called; forgets reports whether it applies.
	afterForget []jumphost.SignInCandidate
	used        []jumphost.SignInCandidate
	checked     []string
	forgets     bool
}

func (f *fakeSignIn) Candidates(context.Context) ([]jumphost.SignInCandidate, error) {
	return f.candidates, f.listErr
}

func (f *fakeSignIn) Check(_ context.Context, c jumphost.SignInCandidate) error {
	f.checked = append(f.checked, c.Label)
	return f.checkErrs[c.Label]
}

func (f *fakeSignIn) Use(c jumphost.SignInCandidate, options *jumphost.Options) {
	f.used = append(f.used, c)
	options.SignIn, options.DBSecretARN, options.DBUser = jumphost.SignInSecret, c.SecretARN, c.User
}

func (f *fakeSignIn) Forget(_ jumphost.SignInCandidate, options *jumphost.Options) bool {
	if !f.forgets {
		return false
	}
	f.candidates = f.afterForget
	options.SignIn, options.DBSecretARN, options.DBUser = jumphost.SignInAuto, "", ""
	return true
}

// prefsRecorder is a session runner remembering every saved connection.
type prefsRecorder struct {
	fakeSessionRunner
	saved *[]jumphost.Options
}

func (r prefsRecorder) SaveConnectionPrefs(_ context.Context, options jumphost.Options) error {
	*r.saved = append(*r.saved, options)
	return nil
}

func newSignInTestModel(t *testing.T, signIn *fakeSignIn) (m ssmModel, saved *[]jumphost.Options) {
	t.Helper()
	saved = &[]jumphost.Options{}
	m = newTestSSMModel(t, "example-dev")
	m.runner = prefsRecorder{saved: saved}
	m.sessions[0].session.SignIn = signIn
	m.sessions[0].session.Details.Auth = "Secrets Manager · read-only secrets Role=read-only"
	m.sessions[0].options = jumphost.Options{SignIn: jumphost.SignInAuto}
	return m, saved
}

// settle runs cmd and feeds back the sign-in and prefs results it leads to, in order; commands
// waiting for other events (session output, ticks) are left alone.
func settle(t *testing.T, m ssmModel, cmd tea.Cmd) ssmModel {
	t.Helper()
	if cmd == nil {
		return m
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-result:
	case <-time.After(100 * time.Millisecond):
		return m
	}
	switch msg := msg.(type) {
	case tea.BatchMsg:
		for _, inner := range msg {
			m = settle(t, m, inner)
		}
	case ssmSignInListedMsg, ssmSignInCheckedMsg, ssmPrefsSavedMsg:
		var next tea.Cmd
		m, next = update(t, m, msg)
		m = settle(t, m, next)
	}
	return m
}

// becomeReady makes the first session's tunnel ready and settles the sign-in it starts.
func becomeReady(t *testing.T, m ssmModel) ssmModel {
	t.Helper()
	m, cmd := update(t, m, ssmOutputMsg{id: m.sessions[0].id, line: "Waiting for connections..."})
	return settle(t, m, cmd)
}

func TestOneCandidateIsCheckedAndSavedWithoutAsking(t *testing.T) {
	signIn := &fakeSignIn{candidates: []jumphost.SignInCandidate{integ}}
	m, saved := newSignInTestModel(t, signIn)

	m = becomeReady(t, m)
	if m.mode != ssmModeSessions || len(signIn.used) != 1 || signIn.used[0] != integ {
		t.Fatalf("mode = %v, used = %+v, want integ used without a dialog", m.mode, signIn.used)
	}
	if s := m.sessions[0]; s.options.DBSecretARN != "arn:integ" || s.options.SignIn != jumphost.SignInSecret {
		t.Fatalf("options = %+v, want the session switched to the secret", s.options)
	}
	if len(*saved) != 1 || (*saved)[0].DBSecretARN != "arn:integ" || (*saved)[0].Name != "example-dev" || (*saved)[0].DBUser != "app_ro_integ" {
		t.Fatalf("saved = %+v, want the discovered secret remembered for the target", *saved)
	}
	view := plainView(m)
	if !strings.Contains(view, "✓ signed in as app_ro_integ") || !strings.Contains(view, "Signed in as app_ro_integ") {
		t.Fatalf("view does not report the sign-in:\n%s", view)
	}
}

func TestSeveralCandidatesAskToChoose(t *testing.T) {
	signIn := &fakeSignIn{candidates: []jumphost.SignInCandidate{integ, mssql}}
	m, saved := newSignInTestModel(t, signIn)

	m = becomeReady(t, m)
	if m.mode != ssmModeSignIn || len(signIn.checked) != 0 {
		t.Fatalf("mode = %v, checked = %v, want the dialog before any check", m.mode, signIn.checked)
	}
	view := plainView(m)
	for _, want := range []string{"Choose the database credentials", "1  example-dev-integ · app_ro_integ", "2  example-dev-mssql · app_ro_mssql"} {
		if !strings.Contains(view, want) {
			t.Fatalf("dialog lacks %q:\n%s", want, view)
		}
	}

	m, cmd := update(t, m, key("2"))
	m = settle(t, m, cmd)
	if m.mode != ssmModeSessions || len(signIn.used) != 1 || signIn.used[0] != mssql || len(*saved) != 1 {
		t.Fatalf("mode = %v, used = %+v, saved = %d; want mssql used and saved", m.mode, signIn.used, len(*saved))
	}
}

func TestAFailedCandidateReopensTheDialogMarked(t *testing.T) {
	signIn := &fakeSignIn{candidates: []jumphost.SignInCandidate{integ, mssql}, checkErrs: map[string]error{
		integ.Label: errors.New("the database rejected the credentials: Login failed for user 'app_ro_integ'"),
	}}
	m, saved := newSignInTestModel(t, signIn)
	m = becomeReady(t, m)

	m, cmd := update(t, m, key("enter"))
	m = settle(t, m, cmd)
	if m.mode != ssmModeSignIn || len(*saved) != 0 || len(signIn.used) != 0 {
		t.Fatalf("mode = %v, saved = %d, used = %d; want the dialog again, nothing used", m.mode, len(*saved), len(signIn.used))
	}
	view := plainView(m)
	if !strings.Contains(view, "✗") || !strings.Contains(view, "Login failed") {
		t.Fatalf("the failed candidate is not marked:\n%s", view)
	}
	if m.sessions[0].signIn.cursor != 1 {
		t.Fatalf("cursor = %d, want the next untried candidate", m.sessions[0].signIn.cursor)
	}
}

func TestLastCandidateFailingLeavesTheTunnelOpen(t *testing.T) {
	signIn := &fakeSignIn{candidates: []jumphost.SignInCandidate{integ}, checkErrs: map[string]error{
		integ.Label: errors.New("dial tcp 127.0.0.1:1433: connection refused"),
	}}
	m, saved := newSignInTestModel(t, signIn)

	m = becomeReady(t, m)
	s := m.sessions[0]
	if s.state != ssmSessionRunning || s.signIn.state != signInFailed || len(*saved) != 0 {
		t.Fatalf("state = %v, sign-in = %v, saved = %d; want a failed sign-in on a running session", s.state, s.signIn.state, len(*saved))
	}
	if view := plainView(m); !strings.Contains(view, "connection refused") || !strings.Contains(view, "sign in") {
		t.Fatalf("view does not report the failure and the a key:\n%s", view)
	}

	// a checks again once the database is reachable.
	signIn.checkErrs = nil
	m, cmd := update(t, m, key("a"))
	m = settle(t, m, cmd)
	if m.sessions[0].signIn.state != signInDone {
		t.Fatalf("sign-in = %v after a, want done", m.sessions[0].signIn.state)
	}
}

func TestARejectedSavedSecretIsForgottenAndTheReadOnlySecretsOffered(t *testing.T) {
	saved := jumphost.SignInCandidate{Label: "example-dev-integ", User: "app_ro_integ", SecretARN: "arn:integ"}
	signIn := &fakeSignIn{
		candidates: []jumphost.SignInCandidate{saved}, afterForget: []jumphost.SignInCandidate{integ, mssql}, forgets: true,
		checkErrs: map[string]error{saved.Label: jumphost.ErrSignInRejected},
	}
	m, savedPrefs := newSignInTestModel(t, signIn)
	m.sessions[0].options = jumphost.Options{SignIn: jumphost.SignInSecret, DBSecretARN: "arn:integ", DBUser: "app_ro_integ"}

	m = becomeReady(t, m)
	if len(*savedPrefs) != 1 || (*savedPrefs)[0].SignIn != jumphost.SignInAuto || (*savedPrefs)[0].DBSecretARN != "" {
		t.Fatalf("saved = %+v, want the Auto sign-in remembered", *savedPrefs)
	}
	if m.mode != ssmModeSignIn || m.sessions[0].signIn.cursor != 1 {
		t.Fatalf("mode = %v, cursor = %d; want the dialog on the untried mssql", m.mode, m.sessions[0].signIn.cursor)
	}
}

func TestEscSkipsTheChoiceAndAReopensIt(t *testing.T) {
	m, _ := newSignInTestModel(t, &fakeSignIn{candidates: []jumphost.SignInCandidate{integ, mssql}})
	m = becomeReady(t, m)

	m, _ = update(t, m, key("esc"))
	if m.mode != ssmModeSessions || m.sessions[0].signIn.state != signInSkipped {
		t.Fatalf("mode = %v, sign-in = %v, want skipped", m.mode, m.sessions[0].signIn.state)
	}
	m, _ = update(t, m, key("a"))
	if m.mode != ssmModeSignIn {
		t.Fatalf("mode = %v after a, want the dialog", m.mode)
	}
}

func TestTheDialogWaitsForAnotherSessionOrDialog(t *testing.T) {
	m, _ := newSignInTestModel(t, &fakeSignIn{candidates: []jumphost.SignInCandidate{integ, mssql}})
	m.mode = ssmModeHelp

	m = becomeReady(t, m)
	if m.mode != ssmModeHelp || m.sessions[0].signIn.state != signInChoosing {
		t.Fatalf("mode = %v, sign-in = %v; want the help kept and the choice pending", m.mode, m.sessions[0].signIn.state)
	}
}

func TestALateCheckResultIsDropped(t *testing.T) {
	signIn := &fakeSignIn{candidates: []jumphost.SignInCandidate{integ}}
	m, saved := newSignInTestModel(t, signIn)
	m, _ = update(t, m, ssmOutputMsg{id: m.sessions[0].id, line: "Waiting for connections..."})
	attempt := m.sessions[0].signIn.attempt

	m, _ = update(t, m, ssmFinishedMsg{id: m.sessions[0].id})
	m, _ = update(t, m, ssmSignInCheckedMsg{id: m.sessions[0].id, attempt: attempt, candidate: integ})
	if len(signIn.used) != 0 || len(*saved) != 0 {
		t.Fatalf("used = %+v, saved = %d after the session ended", signIn.used, len(*saved))
	}
}

func TestSessionsAirlockCannotQueryHaveNoSignIn(t *testing.T) {
	m := newTestSSMModel(t, "cache")
	m, cmd := update(t, m, ssmOutputMsg{id: m.sessions[0].id, line: "Waiting for connections..."})
	m = settle(t, m, cmd)
	if m.sessions[0].signIn.state != signInIdle || strings.Contains(plainView(m), "Sign-in") {
		t.Fatalf("a session without SignIn shows a sign-in:\n%s", plainView(m))
	}
}

func TestTheSignInStopsWithItsSession(t *testing.T) {
	// A dialog open when its session ends closes, and nothing is left "checking".
	m, _ := newSignInTestModel(t, &fakeSignIn{candidates: []jumphost.SignInCandidate{integ, mssql}})
	m = becomeReady(t, m)
	m, _ = update(t, m, ssmFinishedMsg{id: m.sessions[0].id})
	if m.mode == ssmModeSignIn || m.sessions[0].signIn.state == signInChoosing {
		t.Fatalf("mode = %v, sign-in = %v; want the dialog closed with the session", m.mode, m.sessions[0].signIn.state)
	}

	// a on an ended session starts nothing, whatever its sign-in state.
	for _, state := range []ssmSignInState{signInFailed, signInDone, signInSkipped} {
		m.sessions[0].signIn.state = state
		m.sessions[0].signIn.err = errors.New("dial tcp 127.0.0.1:1433: connection refused")
		next, cmd := update(t, m, key("a"))
		if cmd != nil || next.mode != ssmModeSessions || next.sessions[0].signIn.state != state {
			t.Fatalf("a on an ended session (%v): mode = %v, sign-in = %v, cmd = %v", state, next.mode, next.sessions[0].signIn.state, cmd != nil)
		}
		if strings.Contains(plainView(next), "a  sign in") {
			t.Fatalf("an ended session offers a:\n%s", plainView(next))
		}
	}
}
