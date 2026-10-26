package jumphost

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/logsmcp"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// resourceLogs is the session type of the CloudWatch Logs of a profile and region: no jumphost, no
// tunnel, no local port; its read-only MCP server is the session.
const resourceLogs = "logs"

// logsmcpStart starts a CloudWatch Logs session's MCP server; a package variable so tests can replace it.
var logsmcpStart = logsmcp.Start

// checkLogs lists one log group: the profile may read CloudWatch Logs in its region.
func (r *Runner) checkLogs(ctx context.Context, cfg awsConfig) error {
	awsCfg, err := r.apiConfig(ctx, cfg)
	if err != nil {
		return err
	}
	if _, err = cloudwatchlogs.NewFromConfig(awsCfg).DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{Limit: aws.Int32(1)}); err != nil {
		return i18n.NewError("jumphost.logs.describe_log_groups", awsCallError(err))
	}
	return nil
}

// logsTargets is the one target of a profile: its region, when CloudWatch Logs answers there.
func (r *Runner) logsTargets(ctx context.Context, cfg awsConfig) ([]ResourceSummary, error) {
	if err := r.checkLogs(ctx, cfg); err != nil {
		return nil, err
	}
	return []ResourceSummary{{Name: cfg.Region, Type: resourceLogs, Description: "CloudWatch Logs in " + cfg.Region}}, nil
}

// prepareLogs opens the CloudWatch Logs session of options' profile; options.Name, the target, is
// the region when no region is given.
func (r *Runner) prepareLogs(ctx context.Context, options *Options) (*Session, error) {
	switch {
	case options.SkipMCP:
		return nil, i18n.NewError("jumphost.logs.no_mcp")
	case options.MCPAllowWrites:
		return nil, i18n.NewError("jumphost.logs.read_only")
	}
	name, region := strings.TrimSpace(options.Name), strings.TrimSpace(options.AwsRegion)
	switch {
	case region == "":
		options.AwsRegion = name
	case name != "" && name != region:
		return nil, i18n.NewError("jumphost.logs.region_mismatch", name, region)
	}
	cfg, err := r.resolveAWSConfig(ctx, options)
	if err != nil {
		return nil, err
	}
	if err := r.checkLogs(ctx, cfg); err != nil {
		return nil, i18n.NewError("jumphost.logs.unreachable", cfg.Profile, cfg.Region, err)
	}
	label := sessionLabel(resourceLogs, cfg.Region, cfg)
	if err := r.registry.claim(label, cfg.Region); err != nil {
		return nil, err
	}
	endpoint := "logs." + cfg.Region + ".amazonaws.com"
	session := &Session{
		Label: label, ResourceType: resourceLogs, Target: cfg.Region, Profile: cfg.Profile, Region: cfg.Region,
		Details: SessionDetails{
			ConnectedServer: endpoint,
			RestartCommand:  buildRestartCommand(resourceLogs, cfg.Region, "", "", "", cfg),
			Auth:            "AWS profile " + cfg.Profile,
		},
		Intro: []string{fmt.Sprintf("CloudWatch Logs of %s in %s, read-only, through the AWS API (%s)", cfg.Profile, cfg.Region, endpoint)},
	}
	handle, display := r.startLogsMCP(ctx, options, cfg)
	r.attachMCP(session, handle, display)
	session.release = sessionRelease(func() { r.registry.release(label) }, session.mcp)
	return session, nil
}

// startLogsMCP serves the MCP server of a CloudWatch Logs session and returns its display line
// ("not started: …" on failure, which leaves the session without use but open, as for the other types).
func (r *Runner) startLogsMCP(ctx context.Context, options *Options, cfg awsConfig) (server mcpserve.Session, display string) {
	return serveMCP(ctx, r, options, &cfg, func(projectDir string, awsCfg aws.Config) (*mcpserve.Handle, error) {
		return logsmcpStart(r.mcpHost, &logsmcp.SessionConfig{
			AWS: awsCfg, ProjectDir: projectDir, Profile: cfg.Profile, Region: cfg.Region,
			ServerPrefix: r.mcpServerPrefix, LogDir: r.mcpLogDir, SkipRegistration: options.SkipMCPRegistration,
		})
	})
}
