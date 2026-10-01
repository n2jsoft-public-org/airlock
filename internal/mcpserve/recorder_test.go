package mcpserve

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecorderNotifiesSubscribersAndReportsLogFailures(t *testing.T) {
	// A file where the logs directory should be makes every audit log write fail.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := NewRecorder(blocked)

	var first, second []ToolEvent
	recorder.Subscribe(func(e ToolEvent) { first = append(first, e) })
	recorder.Subscribe(func(e ToolEvent) { second = append(second, e) })
	recorder.Record(&ToolEvent{Tool: "query", SQL: "SELECT 1"})

	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("subscribers got %d and %d events, want 1 each", len(first), len(second))
	}
	if first[0].LogError == "" {
		t.Fatal("a failed audit log write must be reported on the event")
	}
	if recorder.LogDir() != blocked {
		t.Fatalf("LogDir() = %q", recorder.LogDir())
	}
}

func TestRecorderWithoutLogDir(t *testing.T) {
	recorder := NewRecorder("")
	var got []ToolEvent
	recorder.Subscribe(func(e ToolEvent) { got = append(got, e) })
	recorder.Record(&ToolEvent{Tool: "info"})
	if len(got) != 1 || got[0].LogError != "" || recorder.LogDir() != "" {
		t.Fatalf("events = %+v, LogDir = %q", got, recorder.LogDir())
	}
}
