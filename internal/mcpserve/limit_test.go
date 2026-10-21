package mcpserve

import "testing"

func TestLimitDefaultsWhenUnsetAndCaps(t *testing.T) {
	for _, tc := range []struct{ requested, want int }{{-1, 100}, {0, 100}, {1, 1}, {500, 500}, {1000, 1000}, {5000, 1000}} {
		if got := Limit(tc.requested, 100, 1000); got != tc.want {
			t.Errorf("Limit(%d) = %d, want %d", tc.requested, got, tc.want)
		}
	}
}
