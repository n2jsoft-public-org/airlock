package selfupdate

import (
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
