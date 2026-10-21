package jumphost

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/n2jsoft/airlock/internal/dbmcp"
	"github.com/n2jsoft/airlock/internal/kubemcp"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// kubemcpStart starts an EKS session's MCP server; a package variable so tests can force a failure.
var kubemcpStart = kubemcp.Start

// serveMCP starts the MCP server of a session with start, given the project directory and, when
// cfg is set, its AWS SDK configuration. It returns the server, nil when none applies, and its
// display line: empty without server, "not started: …" on failure (reported there rather than
// through r.err, which is discarded in TUI mode).
func serveMCP[H interface {
	comparable
	mcpserve.Session
}](ctx context.Context, r *Runner, options *Options, cfg *awsConfig, start func(projectDir string, awsCfg aws.Config) (H, error)) (server mcpserve.Session, display string) {
	projectDir, err := r.projectDirFor(options)
	if err != nil {
		return nil, fmt.Sprintf("not started: %v", err)
	}
	var awsCfg aws.Config
	if cfg != nil {
		if awsCfg, err = r.sdkConfig(ctx, *cfg); err != nil {
			return nil, fmt.Sprintf("not started: %v", err)
		}
	}
	handle, err := start(projectDir, awsCfg)
	var none H
	switch {
	case err != nil:
		return nil, fmt.Sprintf("not started: %v", err)
	case handle == none:
		return nil, ""
	default:
		return handle, handle.Display()
	}
}

// startKubeMCP serves the read-only MCP server of an EKS session and returns its display line:
// empty when no server applies (no certificate authority), "not started: …" on failure.
func (r *Runner) startKubeMCP(ctx context.Context, options *Options, clusterName, endpointHost, caData string, localPort int, cfg awsConfig) (server mcpserve.Session, display string) {
	return serveMCP(ctx, r, options, &cfg, func(projectDir string, awsCfg aws.Config) (*mcpserve.Handle, error) {
		return kubemcpStart(r.mcpHost, &kubemcp.SessionConfig{
			AWS: awsCfg, ProjectDir: projectDir, Profile: cfg.Profile, Region: cfg.Region,
			Target: clusterName, EndpointHost: endpointHost, ClusterCA: caData, LocalPort: localPort,
			ServerPrefix: r.mcpServerPrefix, LogDir: r.mcpLogDir,
			SkipRegistration: options.SkipMCPRegistration,
		})
	})
}

// dbmcpStart starts a session's MCP server; a package variable so tests can force a failure.
var dbmcpStart = dbmcp.Start

// startMCP serves the MCP server of a database session and returns the display line describing
// it (empty when the engine is unsupported, so the session shows no MCP line at all). A failure
// to start is reported through the returned display rather than r.err, which is discarded in TUI
// mode.
func (r *Runner) startMCP(ctx context.Context, options *Options, targetName string, localPort int, target *ideTarget, cfg awsConfig, allowWrites bool) (server mcpserve.Session, display string) {
	return serveMCP(ctx, r, options, &cfg, func(projectDir string, awsCfg aws.Config) (*dbmcp.Handle, error) {
		handle, err := dbmcpStart(r.mcpHost, &dbmcp.SessionConfig{
			AWS:                awsCfg,
			ProjectDir:         projectDir,
			Profile:            cfg.Profile,
			Region:             cfg.Region,
			Target:             targetName,
			AWSEngine:          target.Engine,
			RDSHost:            target.RDSHost,
			RDSPort:            target.RDSPort,
			DBName:             target.Database,
			InstanceARN:        target.InstanceARN,
			MasterSecretARN:    target.MasterSecretARN,
			DBUser:             target.DBUser,
			DBPassword:         target.DBPassword,
			DBSecretARN:        target.DBSecretARN,
			OnPasswordRejected: r.forgetKeychainFunc(target.KeychainAccount, target.DBPassword),
			IAMAuth:            target.IAMAuth,
			LocalPort:          localPort,
			AllowWrites:        allowWrites,
			LogDir:             r.mcpLogDir,
			ServerPrefix:       r.mcpServerPrefix,
			ReadOnlySecretTag:  r.readOnlySecretTag,
			SkipRegistration:   options.SkipMCPRegistration,
		})
		if errors.Is(err, dbmcp.ErrReadOnlySecretTagUnset) {
			return nil, errors.New("set the tag of the read-only SQL Server secrets with `airlock config set modules.sql.readOnlySecretTag Key=Value` (or enable writes)")
		}
		return handle, err
	})
}
