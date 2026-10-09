package sessions

import (
	"context"
	"errors"
	"time"

	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// signInTimeout bounds finding the credentials and checking one of them.
const signInTimeout = 30 * time.Second

// startSignInLocked lists the credentials of e the first time its tunnel is ready.
func (m *Manager) startSignInLocked(e *entry) {
	if e.session.SignIn == nil || e.info.SignIn.State != SignInIdle {
		return
	}
	m.listSignInLocked(e)
}

func (m *Manager) listSignInLocked(e *entry) {
	e.signInAttempt++
	e.info.SignIn.State, e.info.SignIn.Err = SignInListing, ""
	m.lines(e, "Looking for the database credentials...")
	m.changed(e)
	checker, attempt := e.session.SignIn, e.signInAttempt
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, signInTimeout)
		defer cancel()
		candidates, err := checker.Candidates(ctx)
		m.signInListed(e, attempt, candidates, err)
	}()
}

// signInCurrentLocked reports whether a sign-in result of attempt is still awaited by e.
func (m *Manager) signInCurrentLocked(e *entry, attempt int) bool {
	return m.currentLocked(e) && e.info.Active() && e.signInAttempt == attempt
}

func (m *Manager) signInListed(e *entry, attempt int, candidates []jumphost.SignInCandidate, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.signInCurrentLocked(e, attempt) {
		return
	}
	if err != nil {
		m.failSignInLocked(e, err, "Could not find the database credentials: ")
		return
	}
	e.info.SignIn.Candidates = candidates
	untried := e.info.SignIn.Untried()
	switch {
	case len(candidates) == 0:
		m.failSignInLocked(e, i18n.NewError("sessions.signin.no_database_credentials_found"), "")
	case len(untried) == 0:
		m.failSignInLocked(e, i18n.NewError("sessions.signin.none_of_database_credentials"), "")
	case len(candidates) == 1:
		m.checkSignInLocked(e, candidates[0])
	default:
		m.chooseSignInLocked(e)
	}
}

// chooseSignInLocked waits for a dashboard to choose the credentials of e.
func (m *Manager) chooseSignInLocked(e *entry) {
	e.info.SignIn.State = SignInChoosing
	m.lines(e, "Several database credentials: press a to choose.")
	m.changed(e)
}

func (m *Manager) checkSignInLocked(e *entry, c jumphost.SignInCandidate) {
	e.signInAttempt++
	e.info.SignIn.State = SignInChecking
	m.lines(e, "Signing in as "+c.User+" ("+c.Label+")...")
	m.changed(e)
	checker, attempt := e.session.SignIn, e.signInAttempt
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, signInTimeout)
		defer cancel()
		m.signInChecked(e, attempt, c, checker.Check(ctx, c))
	}()
}

func (m *Manager) signInChecked(e *entry, attempt int, c jumphost.SignInCandidate, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.signInCurrentLocked(e, attempt) {
		return
	}
	checker := e.session.SignIn
	if err == nil {
		checker.Use(c, &e.options)
		e.info.Options, e.info.Details = withoutPassword(e.options), e.session.Details
		e.info.SignIn.State, e.info.SignIn.User, e.info.SignIn.Err = SignInDone, c.User, ""
		m.lines(e, "Signed in as "+c.User+": the test query succeeded.")
		m.changed(e)
		if c.Discovered {
			m.savePrefsLocked(e)
		}
		return
	}

	if e.info.SignIn.Failed == nil {
		e.info.SignIn.Failed = map[string]string{}
	}
	english, local := errTexts(err)
	e.info.SignIn.Failed[CandidateKey(&c)] = english
	if local != "" {
		if e.info.SignIn.LocalFailed == nil {
			e.info.SignIn.LocalFailed = map[string]string{}
		}
		e.info.SignIn.LocalFailed[CandidateKey(&c)] = local
	}
	e.info.SignIn.Err, e.info.SignIn.LocalErr = errTexts(err)
	m.lines(e, "Could not sign in as "+c.User+": "+err.Error())
	switch {
	case errors.Is(err, jumphost.ErrSignInRejected) && e.info.Direct():
		// No tunnel to keep, and the MCP server must not hold a wrong password.
		e.info.SignIn.State = SignInFailed
		m.endLocked(e, err)
		m.lines(e, "Press n and open "+e.options.Name+" to enter the password of "+c.User+" again.")
		english, local := errTexts(err)
		m.publish(&Event{Kind: EventPasswordRejected, ID: e.info.ID, Name: e.options.Name, User: c.User, Err: english, LocalErr: local})
	case errors.Is(err, jumphost.ErrSignInRejected) && checker.Forget(c, &e.options):
		e.info.Options = withoutPassword(e.options)
		m.lines(e, "Forgot the saved secret "+c.Label+".")
		m.savePrefsLocked(e)
		m.listSignInLocked(e)
	case len(e.info.SignIn.Untried()) > 0:
		m.chooseSignInLocked(e)
	default:
		m.failSignInLocked(e, err, "")
	}
}

func (m *Manager) failSignInLocked(e *entry, err error, prefix string) {
	e.info.SignIn.State = SignInFailed
	e.info.SignIn.Err, e.info.SignIn.LocalErr = errTexts(err)
	if prefix != "" {
		m.lines(e, prefix+err.Error())
	}
	m.lines(e, "The tunnel stays open: press a to sign in again.")
	m.changed(e)
}

// savePrefsLocked remembers the sign-in options of e for its target; a failure is a notice. An
// agent's session never changes the user's remembered choices.
func (m *Manager) savePrefsLocked(e *entry) {
	if e.info.OpenedBy != "" {
		return
	}
	options := restartOptions(e)
	go func() {
		runner, err := m.runner()
		if err == nil {
			err = runner.SaveConnectionPrefs(m.ctx, options)
		}
		if err != nil {
			m.mu.Lock()
			m.publish(&Event{Kind: EventNotice, Notice: "Could not remember the sign-in: " + err.Error()})
			m.mu.Unlock()
		}
	}()
}

// endSignInLocked stops the sign-in of e, which just ended: a listing or check in flight is dropped.
func (m *Manager) endSignInLocked(e *entry) {
	switch e.info.SignIn.State {
	case SignInListing, SignInChecking, SignInChoosing:
		e.info.SignIn.State = SignInIdle
	default:
		// Idle, skipped, done or failed: nothing in flight to drop.
	}
	e.signInAttempt++
}

// ChooseSignIn implements Service.
func (m *Manager) ChooseSignIn(_ context.Context, id ID, index int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.findLocked(id)
	if e == nil {
		return ErrUnknownSession
	}
	state := e.info.SignIn.State
	if !e.info.Active() || !e.info.Ready || e.session.SignIn == nil ||
		(state != SignInChoosing && state != SignInSkipped) || index < 0 || index >= len(e.info.SignIn.Candidates) {
		return i18n.NewError("sessions.signin.no_credentials_to_choose", e.info.Target)
	}
	m.checkSignInLocked(e, e.info.SignIn.Candidates[index])
	return nil
}

// SkipSignIn implements Service.
func (m *Manager) SkipSignIn(_ context.Context, id ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.findLocked(id)
	if e == nil {
		return ErrUnknownSession
	}
	if e.info.SignIn.State != SignInChoosing {
		return nil
	}
	e.info.SignIn.State = SignInSkipped
	m.lines(e, "Sign-in not checked: press a to choose the credentials.")
	m.changed(e)
	return nil
}

// RetrySignIn implements Service.
func (m *Manager) RetrySignIn(_ context.Context, id ID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.findLocked(id)
	if e == nil {
		return ErrUnknownSession
	}
	if e.session.SignIn == nil || !e.info.Ready || !e.info.Active() {
		return nil
	}
	switch e.info.SignIn.State {
	case SignInFailed, SignInDone:
		e.info.SignIn.Failed, e.info.SignIn.LocalFailed = nil, nil
		m.listSignInLocked(e)
	default:
		// Idle, skipped, or a sign-in in flight: nothing to retry.
	}
	return nil
}
