package cli

import (
	"net"
	"strconv"

	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/jumphost"
	"github.com/spf13/cobra"
)

// forwardResourceType is the session type of airlock forward.
const forwardResourceType = "forward"

// newForwardCmd opens a tunnel through the jumphost to any host and port it reaches, as a session
// of the dashboard like airlock rds: no discovery, IDE data source nor MCP server.
func newForwardCmd() *cobra.Command {
	options := jumphost.Options{}
	cmd := &cobra.Command{
		Use:   "forward <host> <remote-port> [local-port]",
		Short: i18n.T("cli.forward.short"),
		Long:  i18n.T("cli.forward.long"),
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := jumphost.ForwardTarget(args[0], args[1])
			if err != nil {
				return err
			}
			// No --no-mcp: a forward starts no MCP server nor IDE data source whatever the options,
			// which the dashboard keeps for the sessions opened next.
			return executeJumphost(cmd, append([]string{forwardResourceType, target}, args[2:]...), new(options))
		},
	}
	cmd.Flags().StringVarP(&options.AwsProfile, "profile", "p", "", i18n.T("cli.root.flag.profile"))
	cmd.Flags().StringVar(&options.AwsProfile, "aws-profile", "", i18n.T("cli.root.flag.aws-profile"))
	cmd.Flags().StringVar(&options.AwsRegion, "region", "", i18n.T("cli.root.flag.region"))
	cmd.Flags().String("jumphost-tag", "", i18n.T("cli.root.flag.jumphost-tag"))
	registerCompletions(cmd)
	cmd.ValidArgsFunction = completeForwardArgs
	return cmd
}

// completeForwardArgs completes the hosts of the remembered forwards, then their remote ports,
// then the local port they last used.
func completeForwardArgs(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	var completions []string
	seen := map[string]bool{}
	recent := recentConnections(cmd, forwardResourceType)
	for idx := range recent {
		conn := &recent[idx]
		host, port, err := net.SplitHostPort(conn.Ref.Name)
		if err != nil {
			continue
		}
		var value, description string
		switch {
		case len(args) == 0:
			value, description = host, conn.Prefs.Profile+" · "+conn.Ref.Region
		case len(args) == 1 && args[0] == host:
			value, description = port, conn.Prefs.Profile+" · "+conn.Ref.Region
		case len(args) == 2 && args[0] == host && args[1] == port && conn.Prefs.LocalPort > 0:
			value, description = strconv.Itoa(conn.Prefs.LocalPort), i18n.T("cli.completion.last_used")
		default:
			continue
		}
		if !seen[value] {
			seen[value] = true
			completions = append(completions, value+"\t"+description)
		}
	}
	return completions, cobra.ShellCompDirectiveNoFileComp
}
