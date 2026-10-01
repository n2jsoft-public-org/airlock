package version

import (
	"runtime/debug"
	"testing"
)

func TestFromBuildInfo(t *testing.T) {
	tests := []struct {
		name string
		want string
		info debug.BuildInfo
	}{
		{
			name: "checkout build",
			info: debug.BuildInfo{Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "b50fc20d7e1f2a3b4c5d6e7f8091a2b3c4d5e6f7"},
				{Key: "vcs.time", Value: "2026-09-30T21:15:04Z"},
			}},
			want: "20260930-b50fc20",
		},
		{
			name: "checkout build dates the commit in UTC",
			info: debug.BuildInfo{Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "b50fc20d7e1f2a3b4c5d6e7f8091a2b3c4d5e6f7"},
				{Key: "vcs.time", Value: "2026-10-01T01:30:00+02:00"},
			}},
			want: "20260930-b50fc20",
		},
		{
			name: "go install pseudo-version",
			info: debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261001120000-b50fc20abcde"}},
			want: "20261001-b50fc20",
		},
		{
			name: "go install pseudo-version after a tag",
			info: debug.BuildInfo{Main: debug.Module{Version: "v1.2.4-0.20261001120000-b50fc20abcde+incompatible"}},
			want: "20261001-b50fc20",
		},
		{
			name: "go run records no commit",
			info: debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			want: Unknown,
		},
		{
			name: "unparsable commit time falls back to the module version",
			info: debug.BuildInfo{Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "b50fc20d7e1f"},
				{Key: "vcs.time", Value: "yesterday"},
			}},
			want: Unknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FromBuildInfo(&tt.info); got != tt.want {
				t.Fatalf("FromBuildInfo() = %q, want %q", got, tt.want)
			}
		})
	}
}
