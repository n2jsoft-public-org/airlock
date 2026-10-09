package ui

import (
	"context"
	"errors"
	"maps"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// maxReasonLines caps the agent's reason in the approval dialog.
const maxReasonLines = 3

// settleApprovals keeps the approval dialog on the oldest pending approval this view has not
// answered. It closes when that approval is gone (answered in another view, withdrawn, expired),
// and opens over the sessions list only: never over the wizard nor another dialog.
func (m ssmModel) settleApprovals() ssmModel {
	m = m.pruneAnswered()
	next := m.nextApproval()
	if m.mode == ssmModeApproval {
		if next != nil && next.ID == m.approvalID {
			return m
		}
		if !m.answered[m.approvalID] {
			m.notice = i18n.T("ui.ssm_approval.agent_request_was_answered")
		}
		m.mode = ssmModeSessions
		m.refreshViewport()
	}
	if next == nil || m.mode != ssmModeSessions || m.connLost != "" || m.quitting {
		return m
	}
	m.mode, m.approvalID, m.approvalAllow = ssmModeApproval, next.ID, false
	return m
}

// pruneAnswered forgets the answers whose approval is gone: IDs restart with the daemon, so a stale
// answer would hide a new approval, and the set must not grow for ever.
func (m ssmModel) pruneAnswered() ssmModel {
	if len(m.answered) == 0 {
		return m
	}
	kept := make(map[sessions.ApprovalID]bool, len(m.answered))
	for idx := range m.approvals {
		if id := m.approvals[idx].ID; m.answered[id] {
			kept[id] = true
		}
	}
	m.answered = kept
	return m
}

func (m ssmModel) nextApproval() *sessions.Approval {
	for idx := range m.approvals {
		if !m.answered[m.approvals[idx].ID] {
			return &m.approvals[idx]
		}
	}
	return nil
}

func (m ssmModel) shownApproval() *sessions.Approval {
	for idx := range m.approvals {
		if m.approvals[idx].ID == m.approvalID {
			return &m.approvals[idx]
		}
	}
	return nil
}

// handleApprovalKey answers in the approval dialog; Deny has the focus first.
func (m ssmModel) handleApprovalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "right", "tab", "shift+tab":
		m.approvalAllow = !m.approvalAllow
		return m, nil
	case "y":
		return m.answerApproval(true)
	case "n", "esc":
		return m.answerApproval(false)
	case "enter":
		return m.answerApproval(m.approvalAllow)
	case "ctrl+c":
		answered, answer := m.answer(false)
		next, cmd := answered.handleSessionKey(msg)
		return next, tea.Batch(answer, cmd)
	default:
		return m, nil
	}
}

// answerApproval sends the answer; an approved opening signs in on this view's terminal.
func (m ssmModel) answerApproval(allow bool) (tea.Model, tea.Cmd) {
	return m.answer(allow)
}

func (m ssmModel) answer(allow bool) (ssmModel, tea.Cmd) {
	id := m.approvalID
	m.answered = maps.Clone(m.answered)
	if m.answered == nil {
		m.answered = map[sessions.ApprovalID]bool{}
	}
	m.answered[id] = true
	m.mode = ssmModeSessions
	m.notice = i18n.T("ui.ssm_approval.denied_agent_is_told")
	interactive := m.interactive
	if allow {
		m.notice = i18n.T("ui.ssm_approval.allowed_agent_connection_opens")
	} else {
		interactive = nil
	}
	m.refreshViewport()
	service := m.service
	cmd := m.act(i18n.T("ui.ssm_approval.answering_agent"), func(ctx context.Context) error {
		err := service.ResolveApproval(ctx, id, allow, interactive)
		if errors.Is(err, sessions.ErrApprovalResolved) {
			// Another view answered first, or the request is gone: nothing to report.
			return nil
		}
		return err
	})
	return m.settleApprovals(), cmd
}

// printable is text an agent sent, fit for the terminal: control characters (escape sequences
// included) are dropped and line breaks become spaces. The gateway cleans it already.
func printable(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r):
			return -1
		default:
			return r
		}
	}, s)), " ")
}

// renderApprovalDialog draws the pending approval: who asks, for what, why, and the time left.
func (m ssmModel) renderApprovalDialog(width int) string {
	shown := m.shownApproval()
	if shown == nil {
		return ""
	}
	a := *shown
	a.Agent, a.Target, a.Profile, a.Region, a.Reason = printable(a.Agent), printable(a.Target), printable(a.Profile), printable(a.Region), printable(a.Reason)
	dialogWidth := min(width-4, 76)
	inner := maxInt(dialogWidth-8, 20)
	target := m.badge(a.Type) + " " + m.styles.Text.Bold(true).Render(a.Target)
	if badge := m.styles.EnvBadge(sessions.ApprovalEnvironmentOf(m.environments, &a)); badge != "" {
		target += " " + badge
	}
	if a.Writes {
		target += " " + m.styles.WritesBadge()
	}
	rows := []string{m.styles.Text.Bold(true).Render(truncate(i18n.Format("ui.approval.asks", a.Agent), inner)), "", truncate(target, inner)}
	var where []string
	if a.Profile != "" {
		where = append(where, i18n.Format("ui.approval.profile", a.Profile))
	}
	if a.Region != "" {
		where = append(where, i18n.Format("ui.approval.region", a.Region))
	}
	if len(where) > 0 {
		rows = append(rows, m.styles.Muted.Render(truncate(strings.Join(where, " · "), inner)))
	}
	if a.Writes {
		rows = append(rows, m.styles.Danger.Render(truncate(i18n.T("ui.ssm_approval.it_asks_for_writes"), inner)))
	}
	if a.Reason != "" {
		lines := strings.Split(lipgloss.NewStyle().Width(inner).Render("“"+a.Reason+"”"), "\n")
		if len(lines) > maxReasonLines {
			lines = lines[:maxReasonLines]
			lines[maxReasonLines-1] = truncate(strings.TrimRight(lines[maxReasonLines-1], " "), inner-1) + "…"
		}
		rows = append(rows, "", m.styles.Muted.Render(strings.Join(lines, "\n")))
	}
	left := max(int(time.Until(a.ExpiresAt).Round(time.Second).Seconds()), 0)
	rows = append(rows, "", m.styles.Faint.Render(i18n.Format("ui.ssm_approval.expires_in_ds_allow", left)))
	allowStyle, denyStyle := m.styles.Button, m.styles.ButtonFocus
	if m.approvalAllow {
		allowStyle, denyStyle = m.styles.ButtonFocus, m.styles.Button
	}
	rows = append(rows, "", allowStyle.Render(i18n.T("ui.ssm_approval.allow"))+"  "+denyStyle.Render(i18n.T("ui.ssm_approval.deny")))
	return m.styles.ModalPanel.Width(dialogWidth).Render(strings.Join(rows, "\n"))
}
