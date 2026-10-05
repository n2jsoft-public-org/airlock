package ui

import (
	"fmt"
	"math"
	"time"

	"github.com/n2jsoft/airlock/internal/sessions"
)

// reconnectStatus is the status label of an ended session waiting for its reconnection, short
// enough for a sidebar card next to its local address.
func reconnectStatus(s *sessions.Session) string {
	if s.Reconnecting {
		return "Reconnecting"
	}
	return fmt.Sprintf("Retry in %ds", int(math.Ceil(time.Until(s.ReconnectAt).Seconds())))
}
