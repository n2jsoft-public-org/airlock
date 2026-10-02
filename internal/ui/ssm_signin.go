package ui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// signInTimeout bounds finding the credentials and checking one of them.
const signInTimeout = 30 * time.Second

type ssmSignInState int

const (
	// signInIdle: not started (tunnel not ready yet, or no sign-in to check).
	signInIdle ssmSignInState = iota
	signInListing
	signInChoosing
	signInChecking
	signInSkipped
	signInDone
	signInFailed
)

// ssmSignIn is the sign-in check of one session (see jumphost.SignInChecker).
type ssmSignIn struct {
	err error
	// failed holds the error of each candidate tried in vain, by candidateKey.
	failed map[string]error
	// user signed in once done.
	user       string
	candidates []jumphost.SignInCandidate
	// attempt identifies the listing or check in flight; results of an older one are dropped.
	attempt int
	// cursor is the candidate the dialog chooses on enter.
	cursor int
	state  ssmSignInState
}

type ssmSignInListedMsg struct {
	err        error
	candidates []jumphost.SignInCandidate
	id         int
	attempt    int
}

type ssmSignInCheckedMsg struct {
	err       error
	candidate jumphost.SignInCandidate
	id        int
	attempt   int
}

// ssmPrefsSavedMsg reports saving the sign-in of a session to connections.json.
type ssmPrefsSavedMsg struct {
	err error
}

func candidateKey(c *jumphost.SignInCandidate) string {
	return c.SecretARN + "|" + c.Label
}

// untried returns the indexes of the candidates not tried in vain yet.
func (s *ssmSignIn) untried() []int {
	var indexes []int
	for idx := range s.candidates {
		if _, failed := s.failed[candidateKey(&s.candidates[idx])]; !failed {
			indexes = append(indexes, idx)
		}
	}
	return indexes
}

// startSignIn lists the credentials of s the first time its tunnel is ready. Like listSignIn and
// checkSignIn, it takes a pointer to refresh the caller's viewport: call it before returning m,
// never inside the return statement (`return m, m.listSignIn(s)` may return m unrefreshed).
func (m *ssmModel) startSignIn(s *ssmSession) tea.Cmd {
	if s.session.SignIn == nil || s.signIn.state != signInIdle {
		return nil
	}
	return m.listSignIn(s)
}

func (m *ssmModel) listSignIn(s *ssmSession) tea.Cmd {
	s.signIn.attempt++
	s.signIn.state, s.signIn.err = signInListing, nil
	s.appendLines("Looking for the database credentials...")
	m.refreshSelected(s)
	parent, checker, id, attempt := m.ctx, s.session.SignIn, s.id, s.signIn.attempt
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, signInTimeout)
		defer cancel()
		candidates, err := checker.Candidates(ctx)
		return ssmSignInListedMsg{id: id, attempt: attempt, candidates: candidates, err: err}
	}
}

// signInSession returns the session a sign-in result is for, nil when the result is stale.
func (m ssmModel) signInSession(id, attempt int) *ssmSession {
	s := m.session(id)
	if s == nil || !s.active() || s.signIn.attempt != attempt {
		return nil
	}
	return s
}

func (m ssmModel) handleSignInListed(msg ssmSignInListedMsg) (tea.Model, tea.Cmd) {
	s := m.signInSession(msg.id, msg.attempt)
	if s == nil {
		return m, nil
	}
	if msg.err != nil {
		return m.failSignIn(s, msg.err, "Could not find the database credentials: ")
	}
	s.signIn.candidates = msg.candidates
	untried := s.signIn.untried()
	switch {
	case len(msg.candidates) == 0:
		return m.failSignIn(s, errors.New("no database credentials found"), "")
	case len(untried) == 0:
		return m.failSignIn(s, errors.New("none of the database credentials signs in"), "")
	case len(msg.candidates) == 1:
		cmd := m.checkSignIn(s, msg.candidates[0])
		return m, cmd
	default:
		return m.chooseSignIn(s), nil
	}
}

// chooseSignIn waits for the user to choose the credentials of s, in the dialog when s is the
// selected session and no other dialog is open.
func (m ssmModel) chooseSignIn(s *ssmSession) ssmModel {
	s.signIn.state = signInChoosing
	if untried := s.signIn.untried(); len(untried) > 0 {
		s.signIn.cursor = untried[0]
	}
	if m.mode == ssmModeSessions && m.selectedSession() == s {
		m.mode = ssmModeSignIn
	} else {
		s.appendLines("Several database credentials: press a to choose.")
	}
	m.refreshSelected(s)
	return m
}

func (m *ssmModel) checkSignIn(s *ssmSession, c jumphost.SignInCandidate) tea.Cmd {
	s.signIn.attempt++
	s.signIn.state = signInChecking
	s.appendLines("Signing in as " + c.User + " (" + c.Label + ")...")
	m.refreshSelected(s)
	parent, checker, id, attempt := m.ctx, s.session.SignIn, s.id, s.signIn.attempt
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, signInTimeout)
		defer cancel()
		return ssmSignInCheckedMsg{id: id, attempt: attempt, candidate: c, err: checker.Check(ctx, c)}
	}
}

func (m ssmModel) handleSignInChecked(msg ssmSignInCheckedMsg) (tea.Model, tea.Cmd) {
	s := m.signInSession(msg.id, msg.attempt)
	if s == nil {
		return m, nil
	}
	c, checker := msg.candidate, s.session.SignIn
	if msg.err == nil {
		checker.Use(c, &s.options)
		s.signIn.state, s.signIn.user, s.signIn.err = signInDone, c.User, nil
		s.appendLines("Signed in as " + c.User + ": the test query succeeded.")
		m.refreshSelected(s)
		if c.Discovered {
			return m, m.saveSignIn(s)
		}
		return m, nil
	}

	if s.signIn.failed == nil {
		s.signIn.failed = map[string]error{}
	}
	s.signIn.failed[candidateKey(&c)] = msg.err
	s.signIn.err = msg.err
	s.appendLines("Could not sign in as " + c.User + ": " + msg.err.Error())
	if errors.Is(msg.err, jumphost.ErrSignInRejected) && s.direct() {
		return m.askDirectPasswordAgain(s, c.User, msg.err)
	}
	if errors.Is(msg.err, jumphost.ErrSignInRejected) && checker.Forget(c, &s.options) {
		s.appendLines("Forgot the saved secret " + c.Label + ".")
		save := m.saveSignIn(s)
		list := m.listSignIn(s)
		return m, tea.Batch(save, list)
	}
	if len(s.signIn.untried()) > 0 {
		return m.chooseSignIn(s), nil
	}
	return m.failSignIn(s, msg.err, "")
}

// failSignIn ends the sign-in of s on err, logged after prefix when prefix is set.
func (m ssmModel) failSignIn(s *ssmSession, err error, prefix string) (tea.Model, tea.Cmd) {
	s.signIn.state, s.signIn.err = signInFailed, err
	if prefix != "" {
		s.appendLines(prefix + err.Error())
	}
	s.appendLines("The tunnel stays open: press a to sign in again.")
	m.refreshSelected(s)
	return m, nil
}

// askDirectPasswordAgain ends the direct session s, whose password of user the database rejected
// with err (the keychain no longer keeps it): there is no tunnel to keep, and its MCP server must
// not hold a wrong password. The wizard then opens on the review of that database, its password
// field empty, to type it again. A wizard already open is kept: the log says how to reopen it.
func (m ssmModel) askDirectPasswordAgain(s *ssmSession, user string, err error) (tea.Model, tea.Cmd) {
	name := s.options.Name
	s.signIn.state = signInFailed
	m = m.endSession(s, err)
	// The review only takes over the sessions list: a wizard, a preparation or a dialog stays, and
	// the log tells how to enter the password again.
	if m.mode != ssmModeSessions {
		s.appendLines("Press n and open " + name + " to enter the password of " + user + " again.")
		m.refreshSelected(s)
		return m, nil
	}
	m = m.showWizard(m.wizardDefaults())
	m.wizard.openDirect = name
	m.wizard.openDirectError = "The database rejected the password of " + user + ": enter it again."
	return m.startWizard()
}

// saveSignIn remembers the sign-in options of s for its target.
func (m ssmModel) saveSignIn(s *ssmSession) tea.Cmd {
	runner, ctx, options := m.runner, m.ctx, restartOptions(s)
	return func() tea.Msg {
		return ssmPrefsSavedMsg{err: runner.SaveConnectionPrefs(ctx, options)}
	}
}

func (m ssmModel) handlePrefsSaved(msg ssmPrefsSavedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.notice = "Could not remember the sign-in: " + msg.err.Error()
		m.refreshViewport()
	}
	return m, nil
}

// endSignIn stops the sign-in of s, which just ended: a listing or check in flight is dropped
// (its result is stale) and its dialog closes.
func (m ssmModel) endSignIn(s *ssmSession) ssmModel {
	switch s.signIn.state {
	case signInListing, signInChecking, signInChoosing:
		s.signIn.state = signInIdle
	}
	if m.mode == ssmModeSignIn && m.selectedSession() == s {
		m.mode = ssmModeSessions
	}
	return m
}

// canRetrySignIn reports whether a (retrySignIn) applies to s: a running tunnel with a sign-in.
func canRetrySignIn(s *ssmSession) bool {
	return s != nil && s.session.SignIn != nil && s.ready && s.active()
}

// retrySignIn (a) reopens the choice of the selected session, or finds its credentials again.
func (m ssmModel) retrySignIn() (tea.Model, tea.Cmd) {
	s := m.selectedSession()
	if s == nil || !canRetrySignIn(s) {
		return m, nil
	}
	switch s.signIn.state {
	case signInChoosing, signInSkipped:
		m.mode = ssmModeSignIn
		return m, nil
	case signInFailed, signInDone:
		s.signIn.failed = nil
		cmd := m.listSignIn(s)
		return m, cmd
	default:
		return m, nil
	}
}

// handleSignInKey chooses in the credentials dialog.
func (m ssmModel) handleSignInKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.selectedSession()
	if s == nil || (s.signIn.state != signInChoosing && s.signIn.state != signInSkipped) {
		m.mode = ssmModeSessions
		return m, nil
	}
	candidates := s.signIn.candidates
	switch key := msg.String(); key {
	case "up", "k", "shift+tab":
		s.signIn.cursor = max(s.signIn.cursor-1, 0)
	case "down", "j", "tab":
		s.signIn.cursor = min(s.signIn.cursor+1, len(candidates)-1)
	case "enter":
		return m.chooseCandidate(s, s.signIn.cursor)
	case "esc", "q":
		m.mode = ssmModeSessions
		s.signIn.state = signInSkipped
		s.appendLines("Sign-in not checked: press a to choose the credentials.")
		m.refreshViewport()
	case "ctrl+c":
		m.mode = ssmModeSessions
		s.signIn.state = signInSkipped
		return m.handleSessionKey(msg)
	default:
		if index, err := strconv.Atoi(key); err == nil && index >= 1 && index <= len(candidates) {
			return m.chooseCandidate(s, index-1)
		}
	}
	return m, nil
}

func (m ssmModel) chooseCandidate(s *ssmSession, index int) (tea.Model, tea.Cmd) {
	m.mode = ssmModeSessions
	cmd := m.checkSignIn(s, s.signIn.candidates[index])
	return m, cmd
}

// renderSignInDialog draws the credentials to choose from, the failed ones marked.
func (m ssmModel) renderSignInDialog(width int) string {
	s := m.selectedSession()
	if s == nil {
		return ""
	}
	dialogWidth := min(width-4, 90)
	// Modal border (2) and padding (6) around the rows.
	inner := maxInt(dialogWidth-8, 20)

	rows := []string{
		m.styles.Text.Bold(true).Render("Choose the database credentials"),
		m.styles.Muted.Render(truncate(s.session.Target+" · checked with SELECT 1, remembered once it works", inner)),
		"",
	}
	for idx := range s.signIn.candidates {
		c := &s.signIn.candidates[idx]
		marker, label := "  ", m.styles.Text.Render(c.Label)
		if idx == s.signIn.cursor {
			marker, label = m.styles.CardMarker.Render("▌ "), m.styles.Text.Bold(true).Render(c.Label)
		}
		row := marker + m.styles.Faint.Render(strconv.Itoa(idx+1)) + "  " + label + m.styles.Muted.Render(" · "+c.User)
		rows = append(rows, truncate(row, inner))
		if err, failed := s.signIn.failed[candidateKey(c)]; failed {
			// On its own line: after the label and user, the cause would be cut off.
			rows = append(rows, "     "+m.styles.Danger.Render(truncate("✗ "+err.Error(), inner-5)))
		}
	}
	return m.styles.ModalPanel.Width(dialogWidth - 2).Render(strings.Join(rows, "\n"))
}

// signInStatus is the Sign-in row of the details of s; empty when it has no sign-in to check.
func (m ssmModel) signInStatus(s *ssmSession) string {
	if s.session.SignIn == nil {
		return ""
	}
	again := "  " + m.styles.KeyCap.Render("a") + m.styles.Faint.Render(" sign in")
	if !canRetrySignIn(s) {
		again = ""
	}
	switch s.signIn.state {
	case signInListing, signInChecking:
		return m.styles.Warning.Render(m.spinner() + " checking")
	case signInChoosing:
		return m.styles.Warning.Render("choose the credentials") + again
	case signInSkipped:
		return m.styles.Muted.Render("not checked") + again
	case signInDone:
		return m.styles.Success.Render("✓ signed in as "+s.signIn.user) + m.styles.Faint.Render(" · SELECT 1 ok")
	case signInFailed:
		return m.styles.Danger.Render("✗ "+s.signIn.err.Error()) + again
	default:
		if !s.active() {
			return m.styles.Faint.Render("not checked")
		}
		return m.styles.Faint.Render("once the tunnel is ready")
	}
}
