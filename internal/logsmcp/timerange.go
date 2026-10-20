package logsmcp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// defaultRange is the time range of a call that gives no start: Logs Insights bills every GB it
// scans, so the default stays short.
const defaultRange = 15 * time.Minute

// timeRange resolves start and end, each empty, a duration before now ("15m", "1h", "7d") or an
// RFC 3339 time, into a range ending now and starting defaultRange earlier by default.
func timeRange(start, end string, now time.Time) (from, to time.Time, err error) {
	if from, err = instant(start, now, now.Add(-defaultRange)); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("start: %w", err)
	}
	if to, err = instant(end, now, now); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("end: %w", err)
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, errors.New("start must be before end")
	}
	return from, to, nil
}

func instant(value string, now, fallback time.Time) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	d, err := duration(value)
	if err != nil || d <= 0 {
		return time.Time{}, fmt.Errorf("%q is neither a duration before now (15m, 1h, 7d) nor an RFC 3339 time", value)
	}
	return now.Add(-d), nil
}

// duration parses a Go duration, plus whole days ("7d") which Go lacks.
func duration(value string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(value, "d"); ok {
		n, err := strconv.Atoi(days)
		return time.Duration(n) * 24 * time.Hour, err
	}
	return time.ParseDuration(value)
}

// stamp formats milliseconds since the epoch; empty for none.
func stamp(ms *int64) string {
	if ms == nil {
		return ""
	}
	return time.UnixMilli(*ms).UTC().Format(time.RFC3339Nano)
}
