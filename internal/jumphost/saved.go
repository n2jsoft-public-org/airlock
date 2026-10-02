package jumphost

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// ListSavedConnections returns the remembered connections, most recently used first, keeping
// those of profile and region when they are set; direct databases, outside AWS, are always kept.
// Entries whose profile no longer exists in the AWS CLI configuration are flagged ProfileMissing,
// direct databases whose definition is gone TargetMissing.
func (r *Runner) ListSavedConnections(ctx context.Context, profile, region string) ([]SavedConnection, error) {
	if r.prefs == nil {
		return nil, nil
	}
	saved, err := r.prefs.List()
	if err != nil {
		return nil, err
	}
	kept := filterSavedConnections(saved, profile, region, r.knownProfiles(ctx))
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

// ForgetConnection removes the remembered choices of ref, and its password from the keychain.
func (r *Runner) ForgetConnection(_ context.Context, ref TargetRef) error {
	if r.prefs == nil {
		return nil
	}
	var keychainErr error
	if prefs, ok := r.prefs.Load(ref); ok {
		keychainErr = r.forgetKeychainPassword(ref, &prefs)
	}
	return errors.Join(r.prefs.Delete(ref), keychainErr)
}

// knownProfiles returns the local AWS CLI profiles; nil when they cannot be listed, so that no
// entry is wrongly flagged as missing its profile.
func (r *Runner) knownProfiles(ctx context.Context) map[string]bool {
	if _, err := exec.LookPath("aws"); err != nil {
		return nil
	}
	names, err := r.listProfileNames(ctx)
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
