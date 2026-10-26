package ui

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
	"github.com/n2jsoft-public-org/airlock/internal/sessions"
)

// copyTestSession is the archive session of the copy tests, on local port 5432.
func copyTestSession() sessions.Session {
	s := testSession(1, "archive")
	s.LocalPort = 5432
	s.Details.TokenCommand = "airlock rds-token --hostname archive.rds --port 5432 --username app_ro --profile dev --region eu-west-3"
	s.Details.RestartCommand = "airlock rds archive 5432 --profile dev --region eu-west-3"
	return s
}

// newCopyTestModel returns a dashboard on s whose copies land in *copied instead of the clipboard.
func newCopyTestModel(t *testing.T, s *sessions.Session) (m ssmModel, service *fakeService, copied *[]string) {
	t.Helper()
	service = newFakeService(*s)
	m = newServiceModel(t, service)
	copied = &[]string{}
	m.copier = func(text string) error {
		*copied = append(*copied, text)
		return nil
	}
	return m, service, copied
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
	m, _, _ := newCopyTestModel(t, new(copyTestSession()))

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
	if strings.Contains(view, "KUBECONFIG") || strings.Contains(view, "Last MCP call") {
		t.Fatalf("copy menu offers values this session does not have:\n%s", view)
	}
}

func TestCopyMenuCopiesByNumberOrSelection(t *testing.T) {
	m, _, copied := newCopyTestModel(t, new(copyTestSession()))

	m = copyChosen(t, m, "c", "3")
	if len(*copied) != 1 || (*copied)[0] != "airlock rds archive 5432 --profile dev --region eu-west-3" {
		t.Fatalf("copied = %q, want the reconnect command", *copied)
	}
	if m.mode != ssmModeSessions || !strings.Contains(plainView(m), "Copied Reconnect command") {
		t.Fatalf("mode = %v, want the sessions back with a confirmation:\n%s", m.mode, plainView(m))
	}

	m = copyChosen(t, m, "c", "down", "enter")
	if len(*copied) != 2 || !strings.HasPrefix((*copied)[1], "airlock rds-token") {
		t.Fatalf("copied = %q, want the token command", *copied)
	}
}

func TestCopyMenuOffersKubeconfigAndLastQuery(t *testing.T) {
	s := copyTestSession()
	s.Details.Kubeconfig = "/home/dev/.config/airlock/kube/platform.json"
	s.MCP = &sessions.MCP{Events: []mcpserve.ToolEvent{queryEvent("query", "SELECT 1"), queryEvent("query", "SELECT id FROM users")}, Total: 2}
	m, _, copied := newCopyTestModel(t, &s)

	m = copyChosen(t, m, "c")
	view := plainView(m)
	if !strings.Contains(view, "KUBECONFIG") || !strings.Contains(view, "Last MCP call") {
		t.Fatalf("copy menu lacks the kubeconfig or the last query:\n%s", view)
	}
	m = copyChosen(t, m, "5")
	if (*copied)[0] != "SELECT id FROM users" {
		t.Fatalf("copied = %q, want the last MCP call", *copied)
	}
	copyChosen(t, m, "c", "4")
	if (*copied)[1] != "export KUBECONFIG=/home/dev/.config/airlock/kube/platform.json" {
		t.Fatalf("copied = %q, want the KUBECONFIG export", *copied)
	}
}

func TestCopyMenuOffersTheLastNonSQLCall(t *testing.T) {
	s := copyTestSession()
	s.MCP = &sessions.MCP{Events: []mcpserve.ToolEvent{{Tool: "command", Engine: "redis", Command: "HGETALL user:1"}}, Total: 1}
	m, _, copied := newCopyTestModel(t, &s)

	m = copyChosen(t, m, "c")
	if !strings.Contains(plainView(m), "Last MCP call") {
		t.Fatalf("copy menu lacks the last call:\n%s", plainView(m))
	}
	copyChosen(t, m, "4")
	if len(*copied) != 1 || (*copied)[0] != "HGETALL user:1" {
		t.Fatalf("copied = %q, want the cache command", *copied)
	}
}

func TestCopyFailureIsShown(t *testing.T) {
	m, _, _ := newCopyTestModel(t, new(copyTestSession()))
	m.copier = func(string) error { return errors.New("no clipboard") }

	m = copyChosen(t, m, "c", "1")
	if !strings.Contains(m.errorMsg, "no clipboard") {
		t.Fatalf("error = %q, want the clipboard failure shown", m.errorMsg)
	}
}

func TestCopyMenuEscCloses(t *testing.T) {
	m, _, copied := newCopyTestModel(t, new(copyTestSession()))
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
