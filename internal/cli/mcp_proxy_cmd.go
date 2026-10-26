package cli

import (
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
	"github.com/spf13/cobra"
)

// newMCPProxyCmd relays an airlock HTTP MCP server over stdio, for the AI clients that only launch
// stdio servers (Claude Desktop). airlock writes the command in their configuration.
func newMCPProxyCmd() *cobra.Command {
	return &cobra.Command{
		Use:    mcpserve.ProxyCommand + " <url>",
		Short:  i18n.T("cli.mcp_proxy.short"),
		Long:   i18n.Format("cli.mcp_proxy.long", mcpserve.ProxyTokenEnv),
		Args:   cobra.ExactArgs(1),
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			token := os.Getenv(mcpserve.ProxyTokenEnv)
			if token == "" {
				return i18n.NewError("cli.mcp_proxy.token_not_set", mcpserve.ProxyTokenEnv)
			}
			return mcpserve.Proxy(cmd.Context(), args[0], token, &mcp.StdioTransport{})
		},
	}
}
