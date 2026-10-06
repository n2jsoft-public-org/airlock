package cli

import (
	"errors"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/spf13/cobra"
)

// newMCPProxyCmd relays an airlock HTTP MCP server over stdio, for the AI clients that only launch
// stdio servers (Claude Desktop). airlock writes the command in their configuration.
func newMCPProxyCmd() *cobra.Command {
	return &cobra.Command{
		Use:    mcpserve.ProxyCommand + " <url>",
		Short:  "Relay an airlock MCP server over stdio (written in the Claude Desktop configuration)",
		Long:   "Relays the airlock MCP server at <url> over stdin and stdout, signing in with the token in " + mcpserve.ProxyTokenEnv + ".",
		Args:   cobra.ExactArgs(1),
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			token := os.Getenv(mcpserve.ProxyTokenEnv)
			if token == "" {
				return errors.New(mcpserve.ProxyTokenEnv + " is not set")
			}
			return mcpserve.Proxy(cmd.Context(), args[0], token, &mcp.StdioTransport{})
		},
	}
}
