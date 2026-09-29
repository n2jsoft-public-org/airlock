package jumphost

import (
	"context"
	"os/exec"
	"strings"
)

// ListSavedConnections returns the remembered connections, most recently used first, keeping
// those of profile and region when they are set. Entries whose profile no longer exists in the
// AWS CLI configuration are flagged ProfileMissing.
func (r *Runner) ListSavedConnections(ctx context.Context, profile, region string) ([]SavedConnection, error) {
	if r.prefs == nil {
		return nil, nil
	}
	saved, err := r.prefs.List()
	if err != nil {
		return nil, err
	}
	return filterSavedConnections(saved, profile, region, r.knownProfiles(ctx)), nil
}

// ForgetConnection removes the remembered choices of ref.
func (r *Runner) ForgetConnection(_ context.Context, ref TargetRef) error {
	if r.prefs == nil {
		return nil
	}
	return r.prefs.Delete(ref)
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
// those whose profile is not in known; a nil known flags none.
func filterSavedConnections(saved []SavedConnection, profile, region string, known map[string]bool) []SavedConnection {
	profile = strings.TrimSpace(profile)
	region = strings.TrimSpace(region)
	kept := make([]SavedConnection, 0, len(saved))
	for idx := range saved {
		conn := saved[idx]
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
