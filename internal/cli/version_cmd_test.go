package cli

import (
	"testing"

	"github.com/n2jsoft-public-org/airlock/internal/version"
)

func TestVersionPrintsTheBuildVersion(t *testing.T) {
	out, err := runAirlock(t, "version")
	if err != nil || out != version.String()+"\n" {
		t.Fatalf("out = %q, %v, want %q", out, err, version.String())
	}
}
