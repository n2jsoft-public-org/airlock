package cli

import (
	"slices"
	"strings"
	"testing"
)

func TestForwardRefusesAnInvalidTarget(t *testing.T) {
	for _, args := range [][]string{
		{"forward", "https://search.internal", "443"},
		{"forward", "search.internal:443", "443"},
		{"forward", "search.internal", "70000"},
	} {
		if _, err := runAirlock(t, args...); err == nil {
			t.Errorf("airlock %s succeeded, want an error", strings.Join(args, " "))
		}
	}
}

func TestForwardRefusesMCPWrites(t *testing.T) {
	_, err := runAirlock(t, "forward", "search.internal", "443", "--mcp-allow-writes")
	if err == nil || !strings.Contains(err.Error(), "mcp-allow-writes") {
		t.Fatalf("err = %v, want --mcp-allow-writes refused", err)
	}
}

func TestCompletesRecentForwards(t *testing.T) {
	saveRecent(t,
		recent("forward", "search.internal:9200", "dev", "eu-west-3", 19200),
		recent("forward", "search.internal:443", "dev", "eu-west-3", 0),
		recent("rds", "orders", "dev", "eu-west-3", 5432),
	)
	if got := complete(t, "forward", ""); len(got) != 1 || !strings.HasPrefix(got[0], "search.internal\t") {
		t.Fatalf("hosts = %q, want search.internal once", got)
	}
	ports := complete(t, "forward", "search.internal", "")
	if len(ports) != 2 || !slices.ContainsFunc(ports, func(c string) bool { return strings.HasPrefix(c, "9200\t") }) ||
		!slices.ContainsFunc(ports, func(c string) bool { return strings.HasPrefix(c, "443\t") }) {
		t.Fatalf("remote ports = %q, want 9200 and 443", ports)
	}
	if got := complete(t, "forward", "search.internal", "9200", ""); len(got) != 1 || !strings.HasPrefix(got[0], "19200\t") {
		t.Fatalf("local ports = %q, want 19200", got)
	}
}
