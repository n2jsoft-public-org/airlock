package selfupdate

import (
	"golang.org/x/mod/semver"

	"github.com/n2jsoft/airlock/internal/version"
)

// Status tells how the running build compares with the latest release.
type Status string

const (
	// StatusUpToDate means the running build is the latest release.
	StatusUpToDate Status = "up-to-date"
	// StatusAvailable means the latest release is newer than the running build.
	StatusAvailable Status = "available"
	// StatusNewer means the running build is newer than the latest release (built from a later commit).
	StatusNewer Status = "newer"
	// StatusDev means the running build has no version (go run).
	StatusDev Status = "dev"
)

// Decide tells how the current version compares with the latest release, both semantic versions.
// A pseudo-version (a build after a release) orders before the next release.
func Decide(current, latest string) Status {
	if current == version.Unknown {
		return StatusDev
	}
	switch semver.Compare(current, latest) {
	case 0:
		return StatusUpToDate
	case -1:
		return StatusAvailable
	default:
		return StatusNewer
	}
}
