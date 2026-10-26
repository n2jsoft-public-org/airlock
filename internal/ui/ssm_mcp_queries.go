package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
	"github.com/n2jsoft-public-org/airlock/internal/sessions"
)

// queriesShown reports whether the bottom panel shows the MCP queries of s rather than its logs.
func (m ssmModel) queriesShown(s *sessions.Session) bool {
	return m.showQueries && s != nil && s.MCP != nil
}

func (m ssmModel) renderQueryLines(s *sessions.Session, width int) string {
	queries := s.MCP
	if len(queries.Events) == 0 {
		text := m.styles.Muted.Render(i18n.T("ui.ssm_mcp_queries.no_mcp_calls_yet"))
		if queries.LogDir != "" {
			text += "\n\n" + m.styles.Faint.Render(i18n.Format("ui.queries.logged_to", queries.LogDir))
		}
		return lipgloss.NewStyle().Width(width).Render(text)
	}

	textWidth := max(10, width-logTimeWidth)
	indent := strings.Repeat(" ", logTimeWidth)
	var out []string
	if hidden := queries.Total - len(queries.Events); hidden > 0 {
		out = append(out, m.styles.Faint.Render(i18n.Format("ui.ssm_mcp_queries.older_calls_not_shown", hidden)), "")
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
			{m.styles.Warning, prefixed(i18n.T("ui.ssm_mcp_queries.not_saved_to_audit"), e.LogError)},
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
		summary = m.styles.Danger.Render(i18n.T("ui.ssm_mcp_queries.failed")) + m.styles.Faint.Render(" · "+formatQueryDuration(e.Duration))
	} else {
		parts := []string{queryCount(e), formatQueryDuration(e.Duration)}
		if e.Truncated {
			parts = append(parts, i18n.T("ui.hint.truncated"))
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
			subject = i18n.T("ui.hint.cluster")
		}
		if e.Namespace != "" {
			subject = i18n.Format("ui.queries.in_namespace", subject, e.Namespace)
		}
		return subject
	}
	subject := e.Database
	if subject == "" {
		subject = i18n.T("ui.hint.default_database")
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
			return i18n.T("ui.hint.done")
		}
		return i18n.Plural(n, "ui.count.item")
	}
	switch e.Tool {
	case "execute":
		return i18n.Plural(n, "ui.count.row_affected")
	case "list_databases":
		return i18n.Plural(n, "ui.count.database")
	case "list_tables":
		return i18n.Plural(n, "ui.count.table")
	case "describe_table":
		return i18n.Plural(n, "ui.count.column")
	default:
		return i18n.Plural(n, "ui.count.row")
	}
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
