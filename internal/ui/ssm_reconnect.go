package ui

import (
	"math"
	"time"

	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/sessions"
)

// reconnectStatus is the status label of an ended session waiting for its reconnection, short
// enough for a sidebar card next to its local address.
func reconnectStatus(s *sessions.Session) string {
	if s.Reconnecting {
		return i18n.T("ui.ssm_reconnect.reconnecting")
	}
	return i18n.Format("ui.ssm_reconnect.retry_in_ds", int(math.Ceil(time.Until(s.ReconnectAt).Seconds())))
}
