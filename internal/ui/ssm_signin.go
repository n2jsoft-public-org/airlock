package ui

import (
	"context"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// openSignInDialog opens the credentials dialog when s just started waiting for a choice and is
// the selected session with no other dialog open.
func (m *ssmModel) openSignInDialog(before, s *sessions.Session) {
	waiting := s.SignIn.State == sessions.SignInChoosing
	wasWaiting := before != nil && before.SignIn.State == sessions.SignInChoosing
	if !waiting || wasWaiting || m.mode != ssmModeSessions || m.selectedSession() != s {
		return
	}
	m.mode = ssmModeSignIn
	m.signInCursor = firstUntried(s)
}

// closeSignInDialog closes the credentials dialog of s once there is nothing left to choose: its
// session ended, or another dashboard chose meanwhile.
func (m *ssmModel) closeSignInDialog(s *sessions.Session) {
	if m.mode != ssmModeSignIn || m.selectedSession() != s {
		return
	}
	if state := s.SignIn.State; !s.Active() || (state != sessions.SignInChoosing && state != sessions.SignInSkipped) {
		m.mode = ssmModeSessions
	}
}

func firstUntried(s *sessions.Session) int {
	if untried := s.SignIn.Untried(); len(untried) > 0 {
		return untried[0]
	}
	return 0
}

// askDirectPasswordAgain opens the wizard on the review of the direct database whose password ev
// says was rejected, its password field empty, to type it again. It only takes over the sessions
// list showing that session: a wizard, a preparation, a dialog or another session stays, and the
// log tells how to enter the password again.
func (m ssmModel) askDirectPasswordAgain(ev sessions.Event) (tea.Model, tea.Cmd) { //nolint:gocritic // the event as the service sent it.
	s := m.selectedSession()
	if m.mode != ssmModeSessions || s == nil || s.ID != ev.ID {
		return m, nil
	}
	docker := s.ResourceType == sessions.DockerResourceType || s.ResourceType == sessions.DockerCacheResourceType
	if docker && m.docker == nil {
		m.errorMsg = i18n.Format("ui.signin.docker_again", ev.Name)
		return m, nil
	}
	m = m.showWizard(m.wizardDefaults())
	reason := i18n.Format("ui.signin.db_rejected", ev.User)
	if s.ResourceType == sessions.DockerCacheResourceType {
		reason = i18n.Format("ui.signin.cache_rejected", ev.User)
	}
	if docker {
		m.wizard.dockerAtStart, m.wizard.openDocker, m.wizard.openDockerError = true, ev.Name, reason
	} else {
		m.wizard.openDirect, m.wizard.openDirectError = ev.Name, reason
	}
	return m.startWizard()
}

// canRetrySignIn reports whether a (retrySignIn) applies to s: a running tunnel with a sign-in.
func canRetrySignIn(s *sessions.Session) bool {
	return s != nil && s.HasSignIn && s.Ready && s.Active()
}

// retrySignIn (a) reopens the choice of the selected session, or finds its credentials again.
func (m ssmModel) retrySignIn() (tea.Model, tea.Cmd) {
	s := m.selectedSession()
	if s == nil || !canRetrySignIn(s) {
		return m, nil
	}
	switch s.SignIn.State {
	case sessions.SignInChoosing, sessions.SignInSkipped:
		m.mode = ssmModeSignIn
		m.signInCursor = firstUntried(s)
		return m, nil
	case sessions.SignInFailed, sessions.SignInDone:
		service, id := m.service, s.ID
		return m, m.act("", func(ctx context.Context) error { return service.RetrySignIn(ctx, id) })
	default:
		return m, nil
	}
}

// handleSignInKey chooses in the credentials dialog.
func (m ssmModel) handleSignInKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := m.selectedSession()
	if s == nil || (s.SignIn.State != sessions.SignInChoosing && s.SignIn.State != sessions.SignInSkipped) {
		m.mode = ssmModeSessions
		return m, nil
	}
	candidates := s.SignIn.Candidates
	switch key := msg.String(); key {
	case "up", "k", "shift+tab":
		m.signInCursor = max(m.signInCursor-1, 0)
	case "down", "j", "tab":
		m.signInCursor = max(min(m.signInCursor+1, len(candidates)-1), 0)
	case "enter":
		return m.chooseCandidate(s, m.signInCursor)
	case "esc", "q":
		m.mode = ssmModeSessions
		m.refreshViewport()
		return m, m.skipSignIn(s)
	case "ctrl+c":
		m.mode = ssmModeSessions
		skip := m.skipSignIn(s)
		next, cmd := m.handleSessionKey(msg)
		return next, tea.Batch(skip, cmd)
	default:
		if index, err := strconv.Atoi(key); err == nil && index >= 1 && index <= len(candidates) {
			return m.chooseCandidate(s, index-1)
		}
	}
	return m, nil
}

func (m ssmModel) chooseCandidate(s *sessions.Session, index int) (tea.Model, tea.Cmd) {
	m.mode = ssmModeSessions
	service, id := m.service, s.ID
	return m, m.act("", func(ctx context.Context) error {
		err := service.ChooseSignIn(ctx, id, index)
		if err != nil && answeredElsewhere(ctx, service, id) {
			return nil
		}
		return err
	})
}

// answeredElsewhere reports whether the running session id no longer waits for a sign-in choice:
// another view of the daemon answered first, so the refusal of this view's choice tells nothing.
func answeredElsewhere(ctx context.Context, service sessions.Service, id sessions.ID) bool {
	snapshot, err := service.Snapshot(ctx)
	if err != nil {
		return false
	}
	for index := range snapshot.Sessions {
		if s := &snapshot.Sessions[index]; s.ID == id {
			state := s.SignIn.State
			return s.Active() && state != sessions.SignInChoosing && state != sessions.SignInSkipped
		}
	}
	return false
}

// skipSignIn leaves the sign-in of s unchecked.
func (m ssmModel) skipSignIn(s *sessions.Session) tea.Cmd {
	service, id := m.service, s.ID
	return m.act("", func(ctx context.Context) error { return service.SkipSignIn(ctx, id) })
}

// renderSignInDialog draws the credentials to choose from, the failed ones marked.
func (m ssmModel) renderSignInDialog(width int) string {
	s := m.selectedSession()
	if s == nil {
		return ""
	}
	dialogWidth := min(width-4, 90)
	// Modal border (2) and padding (6) around the rows.
	inner := max(dialogWidth-8, 20)

	rows := []string{
		m.styles.Text.Bold(true).Render(i18n.T("ui.ssm_signin.choose_database_credentials")),
		m.styles.Muted.Render(truncate(i18n.Format("ui.signin.checked_with", s.Target), inner)),
		"",
	}
	for idx := range s.SignIn.Candidates {
		c := &s.SignIn.Candidates[idx]
		marker, label := "  ", m.styles.Text.Render(c.Label)
		if idx == m.signInCursor {
			marker, label = m.styles.CardMarker.Render("▌ "), m.styles.Text.Bold(true).Render(c.Label)
		}
		row := marker + m.styles.Faint.Render(strconv.Itoa(idx+1)) + "  " + label + m.styles.Muted.Render(" · "+c.User)
		rows = append(rows, truncate(row, inner))
		if failed, ok := s.SignIn.Failed[sessions.CandidateKey(c)]; ok {
			// On its own line: after the label and user, the cause would be cut off.
			failed = shownErr(s.SignIn.LocalFailed[sessions.CandidateKey(c)], failed)
			rows = append(rows, "     "+m.styles.Danger.Render(truncate("✗ "+failed, inner-5)))
		}
	}
	return m.styles.ModalPanel.Width(dialogWidth).Render(strings.Join(rows, "\n"))
}

// signInStatus is the Sign-in row of the details of s; empty when it has no sign-in to check.
func (m ssmModel) signInStatus(s *sessions.Session) string {
	if !s.HasSignIn {
		return ""
	}
	again := "  " + m.styles.KeyCap.Render("a") + m.styles.Faint.Render(i18n.T("ui.ssm_signin.sign_in"))
	if !canRetrySignIn(s) {
		again = ""
	}
	switch s.SignIn.State {
	case sessions.SignInListing, sessions.SignInChecking:
		return m.styles.Warning.Render(m.spinner() + i18n.T("ui.signin.checking"))
	case sessions.SignInChoosing:
		return m.styles.Warning.Render(i18n.T("ui.hint.choose_credentials")) + again
	case sessions.SignInSkipped:
		return m.styles.Muted.Render(i18n.T("ui.hint.not_checked")) + again
	case sessions.SignInDone:
		return m.styles.Success.Render(i18n.Format("ui.signin.signed_in_as", s.SignIn.User)) + m.styles.Faint.Render(i18n.T("ui.ssm_signin.select_1_ok"))
	case sessions.SignInFailed:
		return m.styles.Danger.Render("✗ "+shownErr(s.SignIn.LocalErr, s.SignIn.Err)) + again
	default:
		if !s.Active() {
			return m.styles.Faint.Render(i18n.T("ui.hint.not_checked"))
		}
		return m.styles.Faint.Render(i18n.T("ui.ssm_signin.once_tunnel_is_ready"))
	}
}
