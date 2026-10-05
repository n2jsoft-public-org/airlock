package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverVersion is reported to MCP clients.
const serverVersion = "1.0.0"

// maxAgentName caps the agent name taken from the User-Agent header.
const maxAgentName = 32

// refusal is a failure the agent reads: a tool result flagged as an error.
func refusal(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}}}
}

// jsonResult is v as the JSON text of a tool result, in the shape of a typed tool handler's return.
func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil, nil
}

// agentName names the AI client of req from its User-Agent (claude-code/2.1.0 gives claude-code);
// agent when it has none.
func agentName(req *mcp.CallToolRequest) string {
	if req == nil || req.Extra == nil || req.Extra.Header == nil {
		return "agent"
	}
	ua := strings.TrimSpace(req.Extra.Header.Get("User-Agent"))
	if end := strings.IndexAny(ua, "/ "); end >= 0 {
		ua = ua[:end]
	}
	var b strings.Builder
	for _, r := range ua {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if len(name) > maxAgentName {
		name = name[:maxAgentName]
	}
	if name == "" {
		return "agent"
	}
	return name
}

// The agent's text is capped, in runes: its reason, and the names it gives (target, profile, region).
const (
	maxReason = 500
	maxName   = 128
)

// cleanText is agent text fit for a terminal and a log line: control characters (escape sequences
// included) are dropped, line breaks and tabs become spaces, spaces are collapsed and trimmed, and
// the text is cut to limit runes.
func cleanText(s string, limit int) string {
	kept := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r), r == utf8.RuneError:
			return -1
		default:
			return r
		}
	}, s)
	runes := []rune(strings.Join(strings.Fields(kept), " "))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return strings.TrimSpace(string(runes))
}
