package sessions

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/n2jsoft/airlock/internal/jumphost"
)

const (
	// ApprovalTimeout is how long an approval waits for a dashboard's answer.
	ApprovalTimeout = 2 * time.Minute
	// grantTimeout bounds how long an approving answer waits for the opening it allowed.
	grantTimeout = 10 * time.Minute
)

var (
	// ErrApprovalResolved refuses an answer to an approval already answered, withdrawn or expired.
	ErrApprovalResolved = errors.New("this request was already answered, withdrawn or expired")
	// ErrNoView refuses an approval no dashboard can answer.
	ErrNoView = errors.New("no airlock dashboard is open to approve")
	// ErrApprovalExpired reports an approval nobody answered in time.
	ErrApprovalExpired = errors.New("nobody answered in the airlock dashboard in time")
	// ErrApprovalDenied reports an approval a dashboard refused.
	ErrApprovalDenied = errors.New("denied in the airlock dashboard")
)

// ApprovalID identifies an approval.
type ApprovalID uint64

// Approval is an opening an AI agent asks for through the MCP gateway, waiting for a dashboard.
type Approval struct {
	RequestedAt time.Time `json:"requestedAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
	// Agent names the AI client; Reason is the agent's own words.
	Agent  string `json:"agent"`
	Reason string `json:"reason,omitempty"`
	// Type (rds, eks, elasticache, direct), Target, Profile and Region describe the connection.
	Type    string `json:"type"`
	Target  string `json:"target"`
	Profile string `json:"profile,omitempty"`
	Region  string `json:"region,omitempty"`
	// Environment is the target's environment; Production stays for the dashboards of earlier versions.
	Environment string     `json:"environment,omitempty"`
	Production  bool       `json:"production,omitempty"`
	Writes      bool       `json:"writes,omitempty"`
	ID          ApprovalID `json:"id"`
}

// Grant is an approved opening. Interactive runs its terminal commands in the approving view (nil
// when that view gave none). Done must be called once the opening was prepared, whatever happened:
// the approving view's answer waits for it.
type Grant struct {
	Interactive jumphost.InteractiveFunc
	Done        func()
}

type pendingApproval struct {
	answer chan approvalAnswer
	info   Approval
}

// approvalAnswer is the outcome of an approval: err is nil when it was approved.
type approvalAnswer struct {
	err         error
	interactive jumphost.InteractiveFunc
	done        chan struct{}
}

// RequestApproval asks the dashboards to allow a, and waits for the first answer, the expiry,
// ctx, or the last view leaving. An approval may win the race with ctx: the Grant must then be
// released with Done all the same.
func (m *Manager) RequestApproval(ctx context.Context, a Approval) (Grant, error) { //nolint:gocritic // a is the caller's copy, completed here.
	m.mu.Lock()
	if m.views == 0 {
		m.mu.Unlock()
		return Grant{}, ErrNoView
	}
	m.nextApproval++
	a.ID, a.RequestedAt = m.nextApproval, m.now()
	a.ExpiresAt = a.RequestedAt.Add(ApprovalTimeout)
	p := &pendingApproval{info: a, answer: make(chan approvalAnswer, 1)}
	m.approvals = append(m.approvals, p)
	m.publish(&Event{Kind: EventApprovalRequested, Approval: new(a)})
	m.mu.Unlock()

	expired := make(chan struct{})
	stop := m.afterFunc(ApprovalTimeout, func() { close(expired) })
	defer stop()
	var answer approvalAnswer
	select {
	case answer = <-p.answer:
	case <-expired:
		answer = m.withdraw(p, ErrApprovalExpired)
	case <-ctx.Done():
		answer = m.withdraw(p, ctx.Err())
	}
	if answer.err != nil {
		return Grant{}, answer.err
	}
	return Grant{Interactive: answer.interactive, Done: sync.OnceFunc(func() { close(answer.done) })}, nil
}

// withdraw resolves p with err unless an answer came first, and returns the answer that won.
func (m *Manager) withdraw(p *pendingApproval, err error) approvalAnswer {
	m.mu.Lock()
	m.resolveLocked(p.info.ID, approvalAnswer{err: err})
	m.mu.Unlock()
	return <-p.answer
}

// resolveLocked hands answer to the pending approval id and tells the subscribers; false when id
// is not pending.
func (m *Manager) resolveLocked(id ApprovalID, answer approvalAnswer) bool {
	for i, p := range m.approvals {
		if p.info.ID != id {
			continue
		}
		m.approvals = append(m.approvals[:i:i], m.approvals[i+1:]...)
		p.answer <- answer
		m.publish(&Event{Kind: EventApprovalResolved, ApprovalID: id, Approved: answer.err == nil})
		return true
	}
	return false
}

// ResolveApproval implements Service.
func (m *Manager) ResolveApproval(ctx context.Context, id ApprovalID, approve bool, interactive jumphost.InteractiveFunc) error {
	answer := approvalAnswer{err: ErrApprovalDenied}
	if approve {
		answer = approvalAnswer{interactive: interactive, done: make(chan struct{})}
	}
	m.mu.Lock()
	resolved := m.resolveLocked(id, answer)
	m.mu.Unlock()
	if !resolved {
		return ErrApprovalResolved
	}
	if !approve {
		return nil
	}
	// The approving view's terminal runs the opening's sign-in: its answer stays open until then.
	timer := time.NewTimer(grantTimeout)
	defer timer.Stop()
	select {
	case <-answer.done:
	case <-ctx.Done():
	case <-timer.C:
	}
	return nil
}

// ViewsChanged records how many dashboards can answer approvals (the daemon's views); the pending
// approvals are refused when none is left. It never blocks.
func (m *Manager) ViewsChanged(views int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.views = views
	for views == 0 && len(m.approvals) > 0 {
		m.resolveLocked(m.approvals[0].info.ID, approvalAnswer{err: ErrNoView})
	}
}

// Views returns the number of dashboards able to answer approvals.
func (m *Manager) Views() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.views
}
