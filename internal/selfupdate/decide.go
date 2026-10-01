package selfupdate

import (
	"strings"

	"github.com/n2jsoft/airlock/internal/version"
)

// Status tells how the running build compares with the latest release.
type Status string

const (
	// StatusUpToDate: the running build is the latest release.
	StatusUpToDate Status = "up-to-date"
	// StatusAvailable: the latest release is newer than the running build.
	StatusAvailable Status = "available"
	// StatusNewer: the running build is newer than the latest release (built from a later commit).
	StatusNewer Status = "newer"
	// StatusDiverged: the running build comes from a commit that is not on the release branch.
	StatusDiverged Status = "diverged"
	// StatusDev: the running build has no version (go run).
	StatusDev Status = "dev"
)

// GitHub's statuses for the comparison base...head, plus CompareUnknown when GitHub does not know
// one of the commits.
const (
	CompareIdentical = "identical"
	CompareAhead     = "ahead"
	CompareBehind    = "behind"
	CompareDiverged  = "diverged"
	CompareUnknown   = "unknown"
)

// NeedsCompare reports whether Decide needs GitHub's comparison of current with latest.
func NeedsCompare(current, latest string) bool {
	return current != version.Unknown && current != latest
}

// Decide tells how the current version compares with the latest release; compare is GitHub's
// status of current...latest, read only when NeedsCompare.
func Decide(current, latest, compare string) Status {
	if current == version.Unknown {
		return StatusDev
	}
	if current == latest {
		return StatusUpToDate
	}
	switch compare {
	case CompareIdentical:
		return StatusUpToDate
	case CompareAhead:
		return StatusAvailable
	case CompareBehind:
		return StatusNewer
	default:
		return StatusDiverged
	}
}

// commit returns the short sha of a version YYYYMMDD-<sha>, or "" when v has another form.
func commit(v string) string {
	date, sha, ok := strings.Cut(v, "-")
	if !ok || len(date) != 8 || strings.Trim(date, "0123456789") != "" || sha == "" {
		return ""
	}
	return sha
}
