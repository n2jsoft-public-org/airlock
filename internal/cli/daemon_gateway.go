package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/n2jsoft-public-org/airlock/internal/config"
	"github.com/n2jsoft-public-org/airlock/internal/daemon"
	"github.com/n2jsoft-public-org/airlock/internal/gateway"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/jumphost"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// gatewayTurnedOff is why the gateway is off when the user turned it off.
const gatewayTurnedOff = "turned off (mcp.gateway.enabled is false)"

// gatewayNotice is the dashboard notice of a daemon whose gateway failed to start; empty when it
// runs, or the user turned it off.
func gatewayNotice(status *daemon.Status) string {
	reason, off := strings.CutPrefix(status.Gateway, "off: ")
	if !off || reason == gatewayTurnedOff || reason == "" {
		return ""
	}
	return i18n.Format("cli.daemon_gateway.off_notice", reason)
}

// daemonRunner is the daemon's runner with the config.json it was built from: the gateway follows
// the same file as the openings.
type daemonRunner struct {
	*jumphost.Runner
	cfg config.Config
}

// gatewaySettings are the settings of cfg each gateway request applies.
func gatewaySettings(cfg *config.Config) gateway.Settings {
	return gateway.Settings{
		Environments:    cfg.EnvironmentSet(),
		AutoConnect:     gateway.AutoConnect(cfg.AgentAutoConnect),
		IdleTimeout:     cfg.AgentIdleTimeout,
		AgentWrites:     cfg.AgentWrites,
		DisabledModules: cfg.DisabledModules,
	}
}

// startGatewayFunc starts the daemon's gateway from config.json as it is when the daemon starts
// (mcp.gateway.enabled, mcp.gateway.port, mcp.serverPrefix, mcp.clients); the agent settings and the discovery
// follow source, rebuilt when the file changes.
func startGatewayFunc(configPath, tokenDir, logDir string, source *runnerSource[*daemonRunner], databases jumphost.DirectDatabases) daemon.StartGatewayFunc {
	return func(ctx context.Context, env daemon.GatewayEnv) (daemon.Gateway, error) {
		cfg, err := config.Load(configPath, source.organization())
		if err != nil {
			return nil, err
		}
		if !cfg.MCPGateway {
			return nil, errors.New(gatewayTurnedOff)
		}
		g, err := gateway.Start(ctx, &gateway.Config{
			Manager: env.Manager, Activity: env.Activity, Log: env.Log, Databases: databases, Containers: dockerContainers(),
			Settings: func() gateway.Settings {
				if runner, currentErr := source.Current(); currentErr == nil {
					return gatewaySettings(&runner.cfg)
				}
				if runner, ok := source.Last(); ok {
					return gatewaySettings(&runner.cfg)
				}
				return gatewaySettings(&cfg)
			},
			Discovery: func() (gateway.Discovery, error) {
				runner, currentErr := source.Current()
				if currentErr != nil {
					return nil, currentErr
				}
				return runner.Runner, nil
			},
			Clients: cfg.MCPClientSelection(), Name: mcpserve.GatewayName(cfg.MCPServerPrefix),
			TokenDir: tokenDir, LogDir: logDir, Port: cfg.MCPGatewayPort,
		})
		if err != nil {
			// A nil *gateway.Gateway would make a non-nil daemon.Gateway.
			return nil, err
		}
		return g, nil
	}
}
