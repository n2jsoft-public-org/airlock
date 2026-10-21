package selfupdate

import (
	"slices"
	"testing"

	"github.com/n2jsoft/airlock/internal/version"
)

func TestDecide(t *testing.T) {
	const latest = "v1.4.0"
	tests := []struct {
		current string
		want    Status
	}{
		{current: version.Unknown, want: StatusDev},
		{current: latest, want: StatusUpToDate},
		{current: "v1.3.2", want: StatusAvailable},
		{current: "v1.4.0-0.20261001120000-b50fc20abcde", want: StatusAvailable},
		{current: "v1.4.1-0.20261009120000-0aa11bbccdde+dirty", want: StatusNewer},
		{current: "v1.10.0", want: StatusNewer},
	}
	for _, tt := range tests {
		if got := Decide(tt.current, latest); got != tt.want {
			t.Errorf("Decide(%q, %q) = %q, want %q", tt.current, latest, got, tt.want)
		}
	}
}

func TestChangesAreTheReleasesAfterCurrentUpToLatest(t *testing.T) {
	releases := []Release{
		{Tag: "v1.5.0"}, {Tag: "v1.4.0"}, {Tag: "v1.3.10", Draft: true}, {Tag: "nightly"},
		{Tag: "v1.3.9"}, {Tag: "v1.3.10-rc"}, {Tag: "v1.3.2"}, {Tag: "v1.3.11"},
	}
	var got []string
	for _, r := range Changes(releases, "v1.3.2", "v1.4.0") {
		got = append(got, r.Tag)
	}
	if want := []string{"v1.4.0", "v1.3.11", "v1.3.10-rc", "v1.3.9"}; !slices.Equal(got, want) {
		t.Fatalf("Changes = %v, want %v", got, want)
	}
	if got := Changes(releases, "dev", "v1.4.0"); got != nil {
		t.Fatalf("Changes of a dev build = %v, want none", got)
	}
}
