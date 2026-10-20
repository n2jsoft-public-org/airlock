package jumphost

import (
	"context"
	"os/exec"
	"slices"
	"strings"
)

// ListSavedConnections returns the remembered connections, most recently used first, keeping
// those of profile and region when they are set; direct databases, outside AWS, are always kept.
// Connections of a disabled module are left out.
// Entries whose profile no longer exists in the AWS CLI configuration are flagged ProfileMissing,
// direct databases whose definition is gone TargetMissing.
func (r *Runner) ListSavedConnections(_ context.Context, profile, region string) ([]SavedConnection, error) {
	if r.prefs == nil {
		return nil, nil
	}
	saved, err := r.prefs.List()
	if err != nil {
		return nil, err
	}
	saved = slices.DeleteFunc(saved, func(conn SavedConnection) bool { return r.ModuleDisabled(conn.Ref.Type) })
	kept := filterSavedConnections(saved, profile, region, r.knownProfiles())
	r.flagMissingDirectDatabases(kept)
	return kept, nil
}

// flagMissingDirectDatabases sets TargetMissing on the direct entries whose definition is gone. An
// unreadable definitions file flags none: it does not prove a database is gone.
func (r *Runner) flagMissingDirectDatabases(saved []SavedConnection) {
	if r.databases == nil {
		return
	}
	for idx := range saved {
		if saved[idx].Ref.Type != resourceDirect {
			continue
		}
		if _, found, err := r.databases.Get(saved[idx].Ref.Name); err == nil && !found {
			saved[idx].TargetMissing = true
		}
	}
}

// ForgetConnection removes the remembered choices of ref, and its password from the keychain; when
// the password cannot be deleted, the choices stay too and the error is returned.
func (r *Runner) ForgetConnection(_ context.Context, ref TargetRef) error {
	if r.prefs == nil {
		return nil
	}
	if prefs, ok := r.prefs.Load(ref); ok {
		// A password that stays in the keychain must stay flagged in the prefs, so that a retry
		// still deletes it instead of leaving it for whatever is saved under this name next.
		if err := r.forgetKeychainPassword(ref, &prefs); err != nil {
			return err
		}
	}
	return r.prefs.Delete(ref)
}

// knownProfiles returns the local AWS CLI profiles; nil when they cannot be listed, so that no
// entry is wrongly flagged as missing its profile.
func (r *Runner) knownProfiles() map[string]bool {
	if _, err := exec.LookPath("aws"); err != nil {
		return nil
	}
	names, err := r.listProfileNames()
	if err != nil {
		return nil
	}
	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[name] = true
	}
	return known
}

// filterSavedConnections keeps the entries of profile and region (empty keeps all) and flags
// those whose profile is not in known; a nil known flags none. Direct databases have no profile
// nor region: they are always kept and never flagged.
func filterSavedConnections(saved []SavedConnection, profile, region string, known map[string]bool) []SavedConnection {
	profile = strings.TrimSpace(profile)
	region = strings.TrimSpace(region)
	kept := make([]SavedConnection, 0, len(saved))
	for idx := range saved {
		conn := saved[idx]
		if conn.Ref.Type == resourceDirect {
			kept = append(kept, conn)
			continue
		}
		if profile != "" && conn.Prefs.Profile != profile {
			continue
		}
		if region != "" && conn.Ref.Region != region {
			continue
		}
		conn.ProfileMissing = known != nil && !known[conn.Prefs.Profile]
		kept = append(kept, conn)
	}
	return kept
}
