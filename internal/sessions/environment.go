package sessions

import "github.com/n2jsoft-public-org/airlock/internal/environment"

// EnvironmentOf returns the environment of s: its direct database's, production for one an
// earlier daemon only flagged (the flag always wins), else the environment of its AWS profile.
func EnvironmentOf(set environment.Set, s *Session) environment.Env {
	return resolve(set, s.Environment, s.Production, s.Profile)
}

// ApprovalEnvironmentOf returns the environment of the target of a.
func ApprovalEnvironmentOf(set environment.Set, a *Approval) environment.Env {
	return resolve(set, a.Environment, a.Production, a.Profile)
}

// resolve applies the rule of EnvironmentOf to the fields of a session or an approval.
func resolve(set environment.Set, name string, production bool, profile string) environment.Env {
	switch {
	case production:
		// The flag wins: the gateway that set it may name its production differently.
		if env := set.Lookup(name); env.Production {
			return env
		}
		return set.Production()
	case name != "":
		return set.Lookup(name)
	default:
		return set.Match(profile)
	}
}
