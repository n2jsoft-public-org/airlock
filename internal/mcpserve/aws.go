// Package mcpserve serves the MCP servers of airlock sessions: one localhost host, the AI client
// registration, the audit log of their tool calls and the handle of a running server.
package mcpserve

import "context"

// AWSRunner runs an AWS CLI command (profile and region already applied) and returns its stdout.
type AWSRunner func(ctx context.Context, args ...string) (string, error)
