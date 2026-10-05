package ui

import (
	"strings"
	"testing"

	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/n2jsoft/airlock/internal/sessions"
)

var (
	integ = jumphost.SignInCandidate{Label: "example-dev-integ", User: "app_ro_integ", SecretARN: "arn:integ", Discovered: true}
	mssql = jumphost.SignInCandidate{Label: "example-dev-mssql", User: "app_ro_mssql", SecretARN: "arn:mssql", Discovered: true}
)

// newSignInTestModel returns a dashboard on a ready session checking its sign-in.
func newSignInTestModel(t *testing.T) (ssmModel, *fakeService) {
	t.Helper()
	s := testSession(1, "example-dev")
	s.HasSignIn = true
	s.Details.Auth = "Secrets Manager · read-only secrets Role=read-only"
	service := newFakeService(s)
	return newServiceModel(t, service), service
}

// choosing makes the sign-in of a session wait for a choice among candidates.
func choosing(candidates ...jumphost.SignInCandidate) func(s *sessions.Session) {
	return func(s *sessions.Session) {
		s.SignIn.State, s.SignIn.Candidates = sessions.SignInChoosing, candidates
	}
}

func TestSignedInSessionShowsItsUser(t *testing.T) {
	m, _ := newSignInTestModel(t)
	m, _ = sendUpdate(t, m, 1, func(s *sessions.Session) {
		s.SignIn.State, s.SignIn.User = sessions.SignInDone, "app_ro_integ"
	})
	if view := plainView(m); m.mode != ssmModeSessions || !strings.Contains(view, "✓ signed in as app_ro_integ") {
		t.Fatalf("mode = %v, view does not report the sign-in:\n%s", m.mode, view)
	}
}

func TestSeveralCandidatesAskToChoose(t *testing.T) {
	m, service := newSignInTestModel(t)

	m, _ = sendUpdate(t, m, 1, choosing(integ, mssql))
	if m.mode != ssmModeSignIn {
		t.Fatalf("mode = %v, want the dialog", m.mode)
	}
	view := plainView(m)
	for _, want := range []string{"Choose the database credentials", "1  example-dev-integ · app_ro_integ", "2  example-dev-mssql · app_ro_mssql"} {
		if !strings.Contains(view, want) {
			t.Fatalf("dialog lacks %q:\n%s", want, view)
		}
	}

	m, cmd := update(t, m, key("2"))
	runServiceCmd(cmd)
	if calls := service.Calls(); m.mode != ssmModeSessions || len(calls) != 1 || calls[0] != "choose 1 1" {
		t.Fatalf("mode = %v, calls = %v; want mssql chosen", m.mode, calls)
	}
}

func TestDialogKeysMoveThenChoose(t *testing.T) {
	m, service := newSignInTestModel(t)
	m, _ = sendUpdate(t, m, 1, choosing(integ, mssql))

	m, _ = update(t, m, key("j"))
	m, _ = update(t, m, key("j"))
	if m.signInCursor != 1 {
		t.Fatalf("cursor = %d, want it kept on the last candidate", m.signInCursor)
	}
	m, _ = update(t, m, key("k"))
	_, cmd := update(t, m, key("enter"))
	runServiceCmd(cmd)
	if calls := service.Calls(); len(calls) != 1 || calls[0] != "choose 1 0" {
		t.Fatalf("calls = %v, want integ chosen", calls)
	}
}

func TestAFailedCandidateReopensTheDialogMarked(t *testing.T) {
	m, _ := newSignInTestModel(t)
	m, _ = sendUpdate(t, m, 1, choosing(integ, mssql))
	m, _ = update(t, m, key("enter"))
	m, _ = sendUpdate(t, m, 1, func(s *sessions.Session) { s.SignIn.State = sessions.SignInChecking })
	if m.mode != ssmModeSessions {
		t.Fatalf("mode = %v, want the dialog closed during the check", m.mode)
	}

	m, _ = sendUpdate(t, m, 1, func(s *sessions.Session) {
		s.SignIn.State = sessions.SignInChoosing
		s.SignIn.Failed = map[string]string{sessions.CandidateKey(&integ): "the database rejected the credentials: Login failed for user 'app_ro_integ'"}
	})
	if m.mode != ssmModeSignIn {
		t.Fatalf("mode = %v, want the dialog again", m.mode)
	}
	view := plainView(m)
	if !strings.Contains(view, "✗") || !strings.Contains(view, "Login failed") {
		t.Fatalf("the failed candidate is not marked:\n%s", view)
	}
	if m.signInCursor != 1 {
		t.Fatalf("cursor = %d, want the next untried candidate", m.signInCursor)
	}
}

func TestAFailedSignInLeavesTheTunnelOpenAndARetries(t *testing.T) {
	m, service := newSignInTestModel(t)
	m, _ = sendUpdate(t, m, 1, func(s *sessions.Session) {
		s.SignIn.State, s.SignIn.Err = sessions.SignInFailed, "dial tcp 127.0.0.1:1433: connection refused"
	})
	if view := plainView(m); !strings.Contains(view, "connection refused") || !strings.Contains(view, "a  sign in") {
		t.Fatalf("view does not report the failure and the a key:\n%s", view)
	}

	_, cmd := update(t, m, key("a"))
	runServiceCmd(cmd)
	if calls := service.Calls(); len(calls) != 1 || calls[0] != "retry 1" {
		t.Fatalf("calls = %v, want the credentials found again", calls)
	}
}

func TestEscSkipsTheChoiceAndAReopensIt(t *testing.T) {
	m, service := newSignInTestModel(t)
	m, _ = sendUpdate(t, m, 1, choosing(integ, mssql))

	m, cmd := update(t, m, key("esc"))
	runServiceCmd(cmd)
	if calls := service.Calls(); m.mode != ssmModeSessions || len(calls) != 1 || calls[0] != "skip 1" {
		t.Fatalf("mode = %v, calls = %v, want skipped", m.mode, calls)
	}
	m, _ = sendUpdate(t, m, 1, func(s *sessions.Session) { s.SignIn.State = sessions.SignInSkipped })
	m, _ = update(t, m, key("a"))
	if m.mode != ssmModeSignIn {
		t.Fatalf("mode = %v after a, want the dialog", m.mode)
	}
}

func TestTheDialogWaitsForAnotherSessionOrDialog(t *testing.T) {
	m, _ := newSignInTestModel(t)
	m.mode = ssmModeHelp
	m, _ = sendUpdate(t, m, 1, choosing(integ, mssql))
	if m.mode != ssmModeHelp {
		t.Fatalf("mode = %v; want the help kept and the choice pending", m.mode)
	}

	other, _ := newSessionsModel(t, "example-dev", "archive")
	other, _ = sendUpdate(t, other, 1, choosing(integ, mssql))
	if other.mode != ssmModeSessions {
		t.Fatalf("mode = %v; want no dialog over another selected session", other.mode)
	}
}

func TestSessionsAirlockCannotQueryHaveNoSignIn(t *testing.T) {
	m, _ := newSessionsModel(t, "cache")
	if strings.Contains(plainView(m), "Sign-in") {
		t.Fatalf("a session without SignIn shows a sign-in:\n%s", plainView(m))
	}
}

func TestTheSignInStopsWithItsSession(t *testing.T) {
	// A dialog open when its session ends closes.
	m, _ := newSignInTestModel(t)
	m, _ = sendUpdate(t, m, 1, choosing(integ, mssql))
	m, _ = sendUpdate(t, m, 1, func(s *sessions.Session) {
		s.State, s.SignIn.State = sessions.StateEnded, sessions.SignInIdle
	})
	if m.mode == ssmModeSignIn {
		t.Fatalf("mode = %v; want the dialog closed with the session", m.mode)
	}

	// a on an ended session starts nothing, whatever its sign-in state.
	for _, state := range []sessions.SignInState{sessions.SignInFailed, sessions.SignInDone, sessions.SignInSkipped} {
		next, _ := sendUpdate(t, m, 1, func(s *sessions.Session) {
			s.SignIn.State, s.SignIn.Err = state, "dial tcp 127.0.0.1:1433: connection refused"
		})
		next, cmd := update(t, next, key("a"))
		if cmd != nil || next.mode != ssmModeSessions {
			t.Fatalf("a on an ended session (%v): mode = %v, cmd = %v", state, next.mode, cmd != nil)
		}
		if strings.Contains(plainView(next), "a  sign in") {
			t.Fatalf("an ended session offers a:\n%s", plainView(next))
		}
	}
}
