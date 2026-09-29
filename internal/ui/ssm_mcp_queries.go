package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/example/acme-cli/internal/dbmcp"
)

// maxMCPQueries caps the MCP tool calls kept per session; the audit log keeps them all.
const maxMCPQueries = 500

// mcpQuerySource is the part of a session MCP server the dashboard listens to.
type mcpQuerySource interface {
	Subscribe(fn func(dbmcp.QueryEvent))
	QueryLogDir() string
}

type ssmMCPQueryMsg struct {
	event dbmcp.QueryEvent
	id    int
}

// ssmMCPQueries holds the recent MCP tool calls of a session.
type ssmMCPQueries struct {
	// logDir is where every call is logged (empty when the audit log is disabled).
	logDir string
	events []dbmcp.QueryEvent
	total  int
}

func (q *ssmMCPQueries) add(e *dbmcp.QueryEvent) {
	q.events = append(q.events, *e)
	q.total++
	if len(q.events) > maxMCPQueries {
		q.events = q.events[len(q.events)-maxMCPQueries:]
	}
}

// subscribeMCPQueries forwards the tool calls of source to the dashboard as messages for session id.
// Events are dropped rather than blocking the MCP server when the dashboard lags behind.
func subscribeMCPQueries(id int, source mcpQuerySource, events chan<- tea.Msg) *ssmMCPQueries {
	source.Subscribe(func(e dbmcp.QueryEvent) {
		select {
		case events <- ssmMCPQueryMsg{id: id, event: e}:
		default:
		}
	})
	return &ssmMCPQueries{logDir: source.QueryLogDir()}
}

// queriesShown reports whether the bottom panel shows the MCP queries of s rather than its logs.
func (m ssmModel) queriesShown(s *ssmSession) bool {
	return m.showQueries && s != nil && s.mcp != nil
}

func (m ssmModel) renderQueryLines(s *ssmSession, width int) string {
	queries := s.mcp
	if len(queries.events) == 0 {
		text := m.styles.Muted.Render("No MCP queries yet. SQL run by AI assistants through this session's MCP server shows up here.")
		if queries.logDir != "" {
			text += "\n\n" + m.styles.Faint.Render("Every call is also logged to "+queries.logDir)
		}
		return lipgloss.NewStyle().Width(width).Render(text)
	}

	textWidth := maxInt(10, width-logTimeWidth)
	indent := strings.Repeat(" ", logTimeWidth)
	var out []string
	if hidden := queries.total - len(queries.events); hidden > 0 {
		out = append(out, m.styles.Faint.Render(fmt.Sprintf("%d older calls not shown", hidden)), "")
	}
	for idx := range queries.events {
		e := &queries.events[idx]
		if idx > 0 {
			out = append(out, "")
		}
		out = append(out, m.styles.Faint.Render(e.Time.Format("15:04:05"))+"  "+truncate(m.queryHeadline(e), textWidth))
		body := []struct {
			style lipgloss.Style
			text  string
		}{
			{m.styles.Text, e.SQL},
			{m.styles.Danger, e.Error},
			{m.styles.Warning, prefixed("not saved to the audit log: ", e.LogError)},
		}
		for _, part := range body {
			if part.text == "" {
				continue
			}
			for _, line := range wrapText(part.text, textWidth) {
				out = append(out, indent+part.style.Render(line))
			}
		}
	}
	return strings.Join(out, "\n")
}

// queryHeadline is e.g. "query · archive  42 rows · 18ms".
func (m ssmModel) queryHeadline(e *dbmcp.QueryEvent) string {
	tool := m.styles.Title.Render(e.Tool)
	if e.Tool == "execute" {
		tool = m.styles.Warning.Bold(true).Render(e.Tool)
	}

	subject := e.Database
	if subject == "" {
		subject = "default database"
	}
	switch {
	case e.Table != "":
		subject += " " + strings.TrimPrefix(e.Schema+"."+e.Table, ".")
	case e.Schema != "":
		subject += " " + e.Schema
	}

	var summary string
	if e.Error != "" {
		summary = m.styles.Danger.Render("✗ failed") + m.styles.Faint.Render(" · "+formatQueryDuration(e.Duration))
	} else {
		parts := []string{queryCount(e), formatQueryDuration(e.Duration)}
		if e.Truncated {
			parts = append(parts, "truncated")
		}
		summary = m.styles.Muted.Render(strings.Join(parts, " · "))
	}
	return tool + m.styles.Faint.Render(" · ") + m.styles.Text.Render(subject) + "  " + summary
}

func queryCount(e *dbmcp.QueryEvent) string {
	var n int
	if e.Rows != nil {
		n = int(*e.Rows)
	}
	switch e.Tool {
	case "execute":
		return countLabel(n, "row") + " affected"
	case "list_databases":
		return countLabel(n, "database")
	case "list_tables":
		return countLabel(n, "table")
	case "describe_table":
		return countLabel(n, "column")
	default:
		return countLabel(n, "row")
	}
}

// countLabel is e.g. "1 row" or "42 rows".
func countLabel(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func formatQueryDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
}

func prefixed(prefix, text string) string {
	if text == "" {
		return ""
	}
	return prefix + text
}

// wrapText splits text into lines of at most width cells, keeping its own line breaks.
func wrapText(text string, width int) []string {
	text = strings.ReplaceAll(strings.TrimSpace(text), "\t", "  ")
	lines := strings.Split(lipgloss.NewStyle().Width(width).Render(text), "\n")
	for idx, line := range lines {
		lines[idx] = strings.TrimRight(line, " ")
	}
	return lines
}
