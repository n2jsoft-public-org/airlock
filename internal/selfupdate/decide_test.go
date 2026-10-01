package selfupdate

import (
	"testing"

	"github.com/n2jsoft/airlock/internal/version"
)

func TestDecide(t *testing.T) {
	const latest = "20261005-3f9a1c2"
	tests := []struct {
		current string
		compare string
		want    Status
	}{
		{current: version.Unknown, want: StatusDev},
		{current: latest, want: StatusUpToDate},
		{current: "20261001-b50fc20", compare: CompareIdentical, want: StatusUpToDate},
		{current: "20261001-b50fc20", compare: CompareAhead, want: StatusAvailable},
		{current: "20261009-0aa11bb", compare: CompareBehind, want: StatusNewer},
		{current: "20261005-0aa11bb", compare: CompareDiverged, want: StatusDiverged},
		{current: "20261005-0aa11bb", compare: CompareUnknown, want: StatusDiverged},
	}
	for _, tt := range tests {
		if got := Decide(tt.current, latest, tt.compare); got != tt.want {
			t.Errorf("Decide(%q, %q, %q) = %q, want %q", tt.current, latest, tt.compare, got, tt.want)
		}
	}
}

func TestNeedsCompare(t *testing.T) {
	if NeedsCompare(version.Unknown, "20261005-3f9a1c2") {
		t.Error("a dev build needs no comparison")
	}
	if NeedsCompare("20261005-3f9a1c2", "20261005-3f9a1c2") {
		t.Error("the latest release itself needs no comparison")
	}
	if !NeedsCompare("20261001-b50fc20", "20261005-3f9a1c2") {
		t.Error("another version needs the comparison")
	}
}

func TestCommit(t *testing.T) {
	for v, want := range map[string]string{
		"20261001-b50fc20": "b50fc20",
		"dev":              "",
		"v1.2.3":           "",
		"2026-10-01":       "",
		"20261001-":        "",
		"2026100a-b50fc20": "",
	} {
		if got := commit(v); got != want {
			t.Errorf("commit(%q) = %q, want %q", v, got, want)
		}
	}
}
