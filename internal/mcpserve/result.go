package mcpserve

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolResult answers a tool registered without an output schema (Out = any) with out marshalled
// once, as text and structured content. A typed Out would make the SDK re-marshal the result
// through any, rounding integers over 2^53 (bigint ids, Redis counters) through float64.
func ToolResult(out any, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}, StructuredContent: json.RawMessage(raw)}, nil, nil
}
