// Package version tells which release the running airlock was built from.
package version

import (
	"runtime/debug"

	"golang.org/x/mod/semver"
)

// Unknown is the version of a binary built without version information, such as with `go run`.
const Unknown = "dev"

// String returns the semantic version of the running binary, such as v1.2.3.
func String() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Unknown
	}
	return FromBuildInfo(info)
}

// FromBuildInfo returns the module version Go recorded in info, or Unknown when it holds none.
// Go records the release tag of a build from a tagged commit (v1.2.3), and a pseudo-version
// after it (v1.2.4-0.20261001120000-b50fc20abcde), which orders before the next release.
func FromBuildInfo(info *debug.BuildInfo) string {
	if !semver.IsValid(info.Main.Version) {
		return Unknown
	}
	return info.Main.Version
}
