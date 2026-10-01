// Package version tells which commit the running airlock was built from.
package version

import (
	"regexp"
	"runtime/debug"
	"time"
)

// Unknown is the version of a binary built without commit information, such as with `go run`.
const Unknown = "dev"

// shortSHALength matches git's default abbreviated commit hash.
const shortSHALength = 7

// pseudoVersion matches the Go module pseudo-versions `go install …@latest` embeds,
// such as v0.0.0-20261001120000-b50fc20abcde: it captures the commit date and hash.
var pseudoVersion = regexp.MustCompile(`-(?:0\.)?(\d{8})\d{6}-([0-9a-f]{12})(?:\+.*)?$`)

// String returns the version of the running binary as YYYYMMDD-<short commit sha>.
func String() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Unknown
	}
	return FromBuildInfo(info)
}

// FromBuildInfo returns the version recorded in info, the commit date (UTC) and short hash
// as YYYYMMDD-<sha>, or Unknown when info holds no commit.
func FromBuildInfo(info *debug.BuildInfo) string {
	// A build from a git checkout (make install, install.ps1) records the commit in its settings.
	var revision, commitTime string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.time":
			commitTime = setting.Value
		}
	}
	if revision != "" && commitTime != "" {
		if at, err := time.Parse(time.RFC3339, commitTime); err == nil {
			return at.UTC().Format("20060102") + "-" + shortSHA(revision)
		}
	}

	// `go install …@latest` records no settings but a pseudo-version holding the same values.
	if match := pseudoVersion.FindStringSubmatch(info.Main.Version); match != nil {
		return match[1] + "-" + shortSHA(match[2])
	}
	return Unknown
}

func shortSHA(revision string) string {
	if len(revision) > shortSHALength {
		return revision[:shortSHALength]
	}
	return revision
}
