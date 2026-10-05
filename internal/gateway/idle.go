package gateway

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/n2jsoft/airlock/internal/sessions"
)

// defaultSweepEvery is how often the gateway looks for idle agent sessions.
const defaultSweepEvery = time.Minute

// sweep stops the agent-opened sessions without relayed call (nor opening) for the idle timeout,
// and forgets the relay clients of the sessions that ended.
func (t *toolset) sweep(ctx context.Context, now time.Time) {
	snapshot, err := t.manager.Snapshot(ctx)
	if err != nil {
		return
	}
	timeout := t.settings().IdleTimeout
	active := map[sessions.ID]bool{}
	for idx := range snapshot.Sessions {
		s := &snapshot.Sessions[idx]
		if !s.Active() {
			continue
		}
		active[s.ID] = true
		if s.OpenedBy == "" || s.State != sessions.StateRunning || timeout <= 0 {
			continue
		}
		last, called := t.relay.lastCallOf(s.ID)
		if !called || last.Before(s.StartedAt) {
			last = s.StartedAt
		}
		if now.Sub(last) < timeout {
			continue
		}
		delete(active, s.ID)
		// A session gone meanwhile was not closed here: no line.
		if t.manager.Stop(ctx, s.ID) == nil {
			_ = t.manager.Log(s.ID, "", fmt.Sprintf("closed after %s without agent activity", formatDuration(timeout))) //nolint:errcheck // the session may be gone.
		}
	}
	t.relay.sweep(active)
}

// sweepLoop sweeps every period until ctx ends.
func (t *toolset) sweepLoop(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = defaultSweepEvery
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.sweep(ctx, t.now())
		}
	}
}

// formatDuration renders d as the settings write it: 30m, 2h, 1h30m.
func formatDuration(d time.Duration) string {
	hours, minutes := int64(d/time.Hour), int64(d%time.Hour/time.Minute)
	switch {
	case d%time.Minute != 0:
		return d.String()
	case minutes == 0:
		return strconv.FormatInt(hours, 10) + "h"
	case hours == 0:
		return strconv.FormatInt(minutes, 10) + "m"
	default:
		return strconv.FormatInt(hours, 10) + "h" + strconv.FormatInt(minutes, 10) + "m"
	}
}
