package sessions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// IdentityCheckInterval is how often the AWS sign-ins of the running sessions are checked. A
// tunnel outlives the sign-in it was opened with (AWS IAM Identity Center sessions last a few
// hours), but the tools signing in with AWS through it (kubectl, IAM database tokens) fail.
const IdentityCheckInterval = 5 * time.Minute

// identityCheckTimeout bounds one identity check.
const identityCheckTimeout = 30 * time.Second

// IdentityChecker is a Runner able to check and renew AWS sign-ins; *jumphost.Runner is one.
type IdentityChecker interface {
	// CheckIdentity never signs in: an expired sign-in fails with jumphost.ErrSignInRequired.
	CheckIdentity(ctx context.Context, profile, region string) error
	// SignIn signs profile in again when needed, on the interactive runner of ctx.
	SignIn(ctx context.Context, profile, region string) error
}

// identityOf returns runner as an IdentityChecker; nil when it is none.
func identityOf(runner Runner) IdentityChecker {
	checker, _ := runner.(IdentityChecker) //nolint:errcheck // nil when the runner checks no identity.
	return checker
}

// identityTarget is one AWS identity to check, and the checker of the sessions using it.
type identityTarget struct {
	checker IdentityChecker
	profile string
	region  string
}

func identityKey(profile, region string) string {
	return strings.ToLower(profile + "|" + region)
}

// watchIdentityLocked schedules the next identity check, unless one is.
func (m *Manager) watchIdentityLocked() {
	if m.identityStop != nil || m.closing {
		return
	}
	m.identityStop = m.afterFunc(IdentityCheckInterval, m.checkIdentities)
}

// checkIdentities checks the AWS identity of each running session, once per profile and region,
// then schedules the next check while any session needs one.
func (m *Manager) checkIdentities() {
	m.mu.Lock()
	m.identityStop = nil
	targets := map[string]identityTarget{}
	for _, e := range m.entries {
		if e.identity != nil && e.info.Active() && !e.info.Direct() {
			targets[identityKey(e.info.Profile, e.info.Region)] = identityTarget{checker: e.identity, profile: e.info.Profile, region: e.info.Region}
		}
	}
	m.mu.Unlock()
	if len(targets) == 0 {
		return
	}

	results := make(map[string]error, len(targets))
	for key, target := range targets {
		ctx, cancel := context.WithTimeout(m.ctx, identityCheckTimeout)
		results[key] = target.checker.CheckIdentity(ctx, target.profile, target.region)
		cancel()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return
	}
	for _, e := range m.entries {
		err, checked := results[identityKey(e.info.Profile, e.info.Region)]
		if !checked || !e.info.Active() {
			continue
		}
		switch {
		case err == nil:
			m.setSignInExpiredLocked(e, false)
		case errors.Is(err, jumphost.ErrSignInRequired):
			m.setSignInExpiredLocked(e, true)
		default:
			// AWS out of reach: nothing learned about the sign-in.
		}
	}
	m.watchIdentityLocked()
}

// setSignInExpiredLocked records whether the AWS sign-in of e expired, telling when it changes.
func (m *Manager) setSignInExpiredLocked(e *entry, expired bool) {
	if e.info.AWSSignInExpired == expired {
		return
	}
	e.info.AWSSignInExpired = expired
	if expired {
		m.lines(e, "", fmt.Sprintf("The AWS sign-in of profile %s expired: the tunnel stays open, but the tools signing in with AWS through it (kubectl, IAM database tokens, the MCP server) fail. Press r to sign in again.", e.info.Profile))
	} else {
		m.lines(e, fmt.Sprintf("Signed in to AWS again with profile %s.", e.info.Profile))
	}
	m.changed(e)
}

// SignInAWS implements Service.
func (m *Manager) SignInAWS(ctx context.Context, id ID, interactive jumphost.InteractiveFunc) error {
	m.mu.Lock()
	e := m.findLocked(id)
	switch {
	case e == nil:
		m.mu.Unlock()
		return ErrUnknownSession
	case e.info.Direct() || e.identity == nil:
		m.mu.Unlock()
		return i18n.NewError("sessions.identity.no_aws_sign_in", e.info.Target)
	case m.closing:
		m.mu.Unlock()
		return ErrClosed
	}
	checker, profile, region := e.identity, e.info.Profile, e.info.Region
	m.mu.Unlock()

	if interactive != nil {
		ctx = jumphost.WithInteractive(ctx, interactive)
	}
	if err := checker.SignIn(ctx, profile, region); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	key := identityKey(profile, region)
	for _, other := range m.entries {
		if other.info.Active() && identityKey(other.info.Profile, other.info.Region) == key {
			m.setSignInExpiredLocked(other, false)
		}
	}
	return nil
}
