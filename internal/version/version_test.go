package version

import (
	"runtime/debug"
	"testing"
)

func TestFromBuildInfo(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{name: "tagged release", version: "v1.2.3", want: "v1.2.3"},
		{name: "commit after a release", version: "v1.2.4-0.20261001120000-b50fc20abcde", want: "v1.2.4-0.20261001120000-b50fc20abcde"},
		{name: "modified checkout", version: "v1.2.4-0.20261001120000-b50fc20abcde+dirty", want: "v1.2.4-0.20261001120000-b50fc20abcde+dirty"},
		{name: "go run records no version", version: "(devel)", want: Unknown},
		{name: "no module version", version: "", want: Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := debug.BuildInfo{Main: debug.Module{Version: tt.version}}
			if got := FromBuildInfo(&info); got != tt.want {
				t.Fatalf("FromBuildInfo() = %q, want %q", got, tt.want)
			}
		})
	}
}
