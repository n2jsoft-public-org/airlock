package mcpserve

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// AuditLog appends the MCP tool calls of every session to one JSON Lines file per day,
// e.g. <dir>/mcp-queries-2026-09-29.jsonl. The files are private to the user since
// statements may contain sensitive literals. It is safe for concurrent use.
type AuditLog struct {
	dir string
	mu  sync.Mutex
}

// auditLine is the on-disk form of an event.
type auditLine struct {
	*ToolEvent
	// NoNS shadows the embedded duration_ns key, always empty so never written.
	NoNS       struct{} `json:"duration_ns,omitzero"`
	DurationMS float64  `json:"duration_ms"`
}

// NewAuditLog writes to dir, created on the first write.
func NewAuditLog(dir string) *AuditLog {
	return &AuditLog{dir: dir}
}

// Dir is where the log files are written.
func (l *AuditLog) Dir() string {
	return l.dir
}

// Write appends e to the file of the day of e.Time (local time).
func (l *AuditLog) Write(e *ToolEvent) error {
	line, err := json.Marshal(auditLine{ToolEvent: e, DurationMS: float64(e.Duration.Microseconds()) / 1000})
	if err != nil {
		return fmt.Errorf("failed to encode MCP audit log entry: %w", err)
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	if mkdirErr := os.MkdirAll(l.dir, 0o700); mkdirErr != nil {
		return fmt.Errorf("failed to create MCP audit log directory: %w", mkdirErr)
	}
	path := filepath.Join(l.dir, "mcp-queries-"+e.Time.Local().Format("2006-01-02")+".jsonl")
	// The file is reopened per write so logs roll over at midnight and several processes can append.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) // #nosec G304 -- path is built from the log directory
	if err != nil {
		return fmt.Errorf("failed to open MCP audit log: %w", err)
	}
	_, writeErr := file.Write(line)
	return errors.Join(writeErr, file.Close())
}
