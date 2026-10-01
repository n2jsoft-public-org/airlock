package mcpserve

import "sync"

// Recorder writes tool calls to the audit log and hands them to subscribers. Safe for concurrent use.
type Recorder struct {
	// log is nil when events are not written to disk.
	log         *AuditLog
	subscribers []func(ToolEvent)
	mu          sync.Mutex
}

// NewRecorder logs to logDir; empty disables the audit log.
func NewRecorder(logDir string) *Recorder {
	r := &Recorder{}
	if logDir != "" {
		r.log = NewAuditLog(logDir)
	}
	return r
}

// Subscribe calls fn with every recorded event, from the goroutine recording it: fn must not block.
func (r *Recorder) Subscribe(fn func(ToolEvent)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subscribers = append(r.subscribers, fn)
}

// Record never fails: an audit log error is reported to subscribers on the event.
func (r *Recorder) Record(e *ToolEvent) {
	if r.log != nil {
		if err := r.log.Write(e); err != nil {
			e.LogError = err.Error()
		}
	}
	r.mu.Lock()
	subscribers := append([]func(ToolEvent){}, r.subscribers...)
	r.mu.Unlock()
	for _, fn := range subscribers {
		fn(*e)
	}
}

// LogDir is where the calls are logged; empty when they are not.
func (r *Recorder) LogDir() string {
	if r.log == nil {
		return ""
	}
	return r.log.Dir()
}
