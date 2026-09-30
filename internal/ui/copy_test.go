package ui

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// newCopyTestModel returns a dashboard whose copies land in *copied instead of the clipboard.
func newCopyTestModel(t *testing.T) (m ssmModel, copied *[]string) {
	t.Helper()
	m = newTestSSMModel(t, "archive")
	copied = &[]string{}
	m.copier = func(text string) error {
		*copied = append(*copied, text)
		return nil
	}
	details := &m.sessions[0].session.Details
	details.TokenCommand = "aws rds generate-db-auth-token --hostname archive.rds --port 5432 --username app_ro --profile dev --region eu-west-3"
	details.RestartCommand = "airlock rds archive 5432 --profile dev --region eu-west-3"
	return m, copied
}

// copyChosen runs the copy command of a menu choice and feeds its result back.
func copyChosen(t *testing.T, m ssmModel, keys ...string) ssmModel {
	t.Helper()
	for _, k := range keys {
		var cmd tea.Cmd
		m, cmd = update(t, m, key(k))
		if cmd != nil {
			m, _ = update(t, m, cmd())
		}
	}
	return m
}

func TestCopyMenuListsWhatCanBeCopied(t *testing.T) {
	m, _ := newCopyTestModel(t)

	m, _ = update(t, m, key("c"))
	if m.mode != ssmModeCopy {
		t.Fatalf("mode = %v, want the copy menu", m.mode)
	}
	view := plainView(m)
	for _, want := range []string{"Local address", "localhost:5432", "IAM token command", "Reconnect command"} {
		if !strings.Contains(view, want) {
			t.Fatalf("copy menu lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "KUBECONFIG") || strings.Contains(view, "Last MCP query") {
		t.Fatalf("copy menu offers values this session does not have:\n%s", view)
	}
}

func TestCopyMenuCopiesByNumberOrSelection(t *testing.T) {
	m, copied := newCopyTestModel(t)

	m = copyChosen(t, m, "c", "3")
	if len(*copied) != 1 || (*copied)[0] != "airlock rds archive 5432 --profile dev --region eu-west-3" {
		t.Fatalf("copied = %q, want the reconnect command", *copied)
	}
	if m.mode != ssmModeSessions || !strings.Contains(plainView(m), "Copied Reconnect command") {
		t.Fatalf("mode = %v, want the sessions back with a confirmation:\n%s", m.mode, plainView(m))
	}

	m = copyChosen(t, m, "c", "down", "enter")
	if len(*copied) != 2 || !strings.HasPrefix((*copied)[1], "aws rds generate-db-auth-token") {
		t.Fatalf("copied = %q, want the token command", *copied)
	}
}

func TestCopyMenuOffersKubeconfigAndLastQuery(t *testing.T) {
	m, copied := newCopyTestModel(t)
	m.sessions[0].session.Details.Kubeconfig = "/home/dev/.config/airlock/kube/platform.json"
	m.sessions[0].mcp = &ssmMCPQueries{}
	m.sessions[0].mcp.add(ptr(queryEvent("query", "SELECT 1")))
	m.sessions[0].mcp.add(ptr(queryEvent("query", "SELECT id FROM users")))

	m = copyChosen(t, m, "c")
	view := plainView(m)
	if !strings.Contains(view, "KUBECONFIG") || !strings.Contains(view, "Last MCP query") {
		t.Fatalf("copy menu lacks the kubeconfig or the last query:\n%s", view)
	}
	m = copyChosen(t, m, "5")
	if (*copied)[0] != "SELECT id FROM users" {
		t.Fatalf("copied = %q, want the last MCP query", *copied)
	}
	m = copyChosen(t, m, "c", "4")
	if (*copied)[1] != "export KUBECONFIG=/home/dev/.config/airlock/kube/platform.json" {
		t.Fatalf("copied = %q, want the KUBECONFIG export", *copied)
	}
}

func TestCopyFailureIsShown(t *testing.T) {
	m, _ := newCopyTestModel(t)
	m.copier = func(string) error { return errors.New("no clipboard") }

	m = copyChosen(t, m, "c", "1")
	if !strings.Contains(m.errorMsg, "no clipboard") {
		t.Fatalf("error = %q, want the clipboard failure shown", m.errorMsg)
	}
}

func TestCopyMenuEscCloses(t *testing.T) {
	m, copied := newCopyTestModel(t)
	m = copyChosen(t, m, "c", "esc")
	if m.mode != ssmModeSessions || len(*copied) != 0 {
		t.Fatalf("mode = %v copied = %v, want the menu closed without copying", m.mode, *copied)
	}
}

func TestWriteOSC52(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TERM", "xterm-256color")
	var out bytes.Buffer
	if err := writeOSC52(&out, "localhost:5432"); err != nil {
		t.Fatal(err)
	}
	if want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("localhost:5432")); !strings.HasPrefix(out.String(), want) {
		t.Fatalf("sequence = %q, want it to start with %q", out.String(), want)
	}

	t.Setenv("TMUX", "/tmp/tmux-501/default,1,0")
	out.Reset()
	if err := writeOSC52(&out, "x"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "\x1bPtmux;") {
		t.Fatalf("sequence = %q, want it passed through tmux", out.String())
	}
}

func ptr[T any](v T) *T { return &v }
