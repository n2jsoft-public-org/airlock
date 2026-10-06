package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/n2jsoft/airlock/internal/sessions"
)

// queriesShown reports whether the bottom panel shows the MCP queries of s rather than its logs.
func (m ssmModel) queriesShown(s *sessions.Session) bool {
	return m.showQueries && s != nil && s.MCP != nil
}

func (m ssmModel) renderQueryLines(s *sessions.Session, width int) string {
	queries := s.MCP
	if len(queries.Events) == 0 {
		text := m.styles.Muted.Render("No MCP calls yet. What AI assistants run through this session's MCP server shows up here.")
		if queries.LogDir != "" {
			text += "\n\n" + m.styles.Faint.Render("Every call is also logged to "+queries.LogDir)
		}
		return lipgloss.NewStyle().Width(width).Render(text)
	}

	textWidth := maxInt(10, width-logTimeWidth)
	indent := strings.Repeat(" ", logTimeWidth)
	var out []string
	if hidden := queries.Total - len(queries.Events); hidden > 0 {
		out = append(out, m.styles.Faint.Render(fmt.Sprintf("%d older calls not shown", hidden)), "")
	}
	for idx := range queries.Events {
		e := &queries.Events[idx]
		if idx > 0 {
			out = append(out, "")
		}
		out = append(out, m.styles.Faint.Render(e.Time.Format("15:04:05"))+"  "+truncate(m.queryHeadline(e), textWidth))
		body := []struct {
			style lipgloss.Style
			text  string
		}{
			{m.styles.Text, e.Detail()},
			{m.styles.Danger, e.Error},
			{m.styles.Warning, prefixed("not saved to the audit log: ", e.LogError)},
		}
		for i := range body {
			part := &body[i]
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
func (m ssmModel) queryHeadline(e *mcpserve.ToolEvent) string {
	tool := m.styles.Title.Render(e.Tool)
	if e.Tool == "execute" {
		tool = m.styles.Warning.Bold(true).Render(e.Tool)
	}

	subject := querySubject(e)

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

// querySubject names what a call was about: the database objects, the Kubernetes resources or the
// cache database.
func querySubject(e *mcpserve.ToolEvent) string {
	switch {
	case e.DB != nil:
		return fmt.Sprintf("db %d", *e.DB)
	case e.Engine == "kubernetes":
		subject := e.Resource
		if subject == "" {
			subject = "cluster"
		}
		if e.Namespace != "" {
			subject += " in " + e.Namespace
		}
		return subject
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
	return subject
}

func queryCount(e *mcpserve.ToolEvent) string {
	var n int
	if e.Rows != nil {
		n = int(*e.Rows)
	}
	if e.Engine == "kubernetes" || e.DB != nil {
		if e.Tool == "execute" {
			return "done"
		}
		return countLabel(n, "item")
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
