package logsmcp

import (
	"testing"
	"time"
)

func TestTimeRange(t *testing.T) {
	for _, c := range []struct {
		from, to   time.Time
		start, end string
	}{
		{testNow.Add(-15 * time.Minute), testNow, "", ""},
		{testNow.Add(-time.Hour), testNow, "1h", ""},
		{testNow.Add(-7 * 24 * time.Hour), testNow.Add(-24 * time.Hour), "7d", "1d"},
		{testNow.Add(-2 * time.Hour), testNow.Add(-time.Hour), "2026-10-20T10:00:00Z", "2026-10-20T13:00:00+02:00"},
	} {
		from, to, err := timeRange(c.start, c.end, testNow)
		if err != nil || !from.Equal(c.from) || !to.Equal(c.to) {
			t.Errorf("timeRange(%q, %q) = %v, %v, %v; want %v, %v", c.start, c.end, from, to, err, c.from, c.to)
		}
	}
	for _, c := range [][2]string{{"yesterday", ""}, {"-1h", ""}, {"5m", "1h"}, {"0s", ""}} {
		if _, _, err := timeRange(c[0], c[1], testNow); err == nil {
			t.Errorf("timeRange(%q, %q) succeeded", c[0], c[1])
		}
	}
}
