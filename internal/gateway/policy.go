// Package gateway is the MCP gateway of the daemon: one MCP server through which AI agents list
// the targets, open sessions (approved in a dashboard or by the settings) and use the tools of
// every running session, relayed in-process to the session servers.
package gateway

import (
	"slices"
	"time"

	"github.com/n2jsoft/airlock/internal/environment"
	"github.com/n2jsoft/airlock/internal/jumphost"
)

// AutoConnect is which agent openings skip the dashboard prompt (agent.autoConnect).
type AutoConnect string

const (
	AutoConnectNone    AutoConnect = "none"
	AutoConnectNonProd AutoConnect = "non-prod"
	AutoConnectAll     AutoConnect = "all"
)

// Settings are the settings of config.json each gateway request applies.
type Settings struct {
	AutoConnect  AutoConnect
	Environments environment.Set
	// DisabledModules are the modules (jumphost.ModuleOf) agents neither list nor open.
	DisabledModules []string
	// IdleTimeout closes an agent-opened session without relayed call for that long.
	IdleTimeout time.Duration
	// AgentWrites lets agents ask for MCP writes, always prompted.
	AgentWrites bool
}

// disabled reports whether the targets of resourceType belong to a disabled module.
func (s *Settings) disabled(resourceType string) bool {
	return slices.Contains(s.DisabledModules, jumphost.ModuleOf(resourceType))
}

// Verdict is what Decide concludes for an opening.
type Verdict int

const (
	// Auto opens without asking.
	Auto Verdict = iota
	// Prompt asks the dashboards.
	Prompt
	// Refuse does not open; Decision.Reason says why.
	Refuse
)

// Decision is the verdict of an opening and, for a refusal, its reason for the agent.
type Decision struct {
	Reason  string
	Verdict Verdict
}

const (
	reasonWritesOff = "agent writes are off: the user can turn them on with `airlock config set agent.writes true`"
	reasonNoView    = "no airlock dashboard is open to approve: ask the user to open `airlock`, or to set agent.autoConnect"
)

// Decide applies the settings to an opening asking for writes or not, of a production target or
// not, while views dashboards can answer.
func Decide(writes bool, s *Settings, production bool, views int) Decision {
	if writes && !s.AgentWrites {
		return Decision{Verdict: Refuse, Reason: reasonWritesOff}
	}
	prompt := writes
	switch s.AutoConnect {
	case AutoConnectAll:
	case AutoConnectNonProd:
		prompt = prompt || production
	default:
		prompt = true
	}
	switch {
	case !prompt:
		return Decision{Verdict: Auto}
	case views == 0:
		return Decision{Verdict: Refuse, Reason: reasonNoView}
	default:
		return Decision{Verdict: Prompt}
	}
}
