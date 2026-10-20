// Package doctor checks what airlock needs, from plain values and read-only calls: it never signs
// in, writes a file nor starts a process that stays.
package doctor

import (
	"io"
	"strings"
)

// Status is the outcome of a check.
type Status string

const (
	OK      Status = "ok"
	Warning Status = "warning"
	Failure Status = "failure"
)

// Result is one checked item. Message and Fix are in the user's language; Check, Subject and
// Status are stable, for scripts.
type Result struct {
	Check   string `json:"check"`
	Subject string `json:"subject,omitempty"`
	Status  Status `json:"status"`
	Message string `json:"message"`
	// Fix tells how to fix a warning or a failure; empty when ok.
	Fix string `json:"fix,omitempty"`
}

func ok(check, subject, message string) Result {
	return Result{Check: check, Subject: subject, Status: OK, Message: message}
}

func warning(check, subject, message, fix string) Result {
	return Result{Check: check, Subject: subject, Status: Warning, Message: message, Fix: fix}
}

func failure(check, subject, message, fix string) Result {
	return Result{Check: check, Subject: subject, Status: Failure, Message: message, Fix: fix}
}

// Failed reports whether a result is a failure.
func Failed(results []Result) bool {
	for i := range results {
		if results[i].Status == Failure {
			return true
		}
	}
	return false
}

var marks = map[Status]string{OK: "✓", Warning: "!", Failure: "✗"}

// Write prints one line per result, with its fix indented under it.
func Write(w io.Writer, results []Result) error {
	var b strings.Builder
	for i := range results {
		r := &results[i]
		b.WriteString(marks[r.Status] + " " + r.Message + "\n")
		if r.Fix != "" {
			b.WriteString("    " + r.Fix + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}
