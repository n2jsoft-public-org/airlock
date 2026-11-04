package jumphost

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
	"github.com/n2jsoft-public-org/airlock/internal/sqsmcp"
)

// resourceSQS is the session type of the SQS queues and SNS topics of a profile and region: no
// jumphost, no tunnel, no local port; its MCP server is the session.
const resourceSQS = "sqs"

// sqsmcpStart starts an SQS session's MCP server; a package variable so tests can replace it.
var sqsmcpStart = sqsmcp.Start

// checkSQS lists one queue: the profile may use SQS in its region.
func (r *Runner) checkSQS(ctx context.Context, cfg awsConfig) error {
	awsCfg, err := r.apiConfig(ctx, cfg)
	if err != nil {
		return err
	}
	if _, err = sqs.NewFromConfig(awsCfg).ListQueues(ctx, &sqs.ListQueuesInput{MaxResults: aws.Int32(1)}); err != nil {
		return i18n.NewError("jumphost.sqs.list_queues", awsCallError(err))
	}
	return nil
}

// sqsTargets is the one target of a profile: its region, when SQS answers there.
func (r *Runner) sqsTargets(ctx context.Context, cfg awsConfig) ([]ResourceSummary, error) {
	if err := r.checkSQS(ctx, cfg); err != nil {
		return nil, err
	}
	return []ResourceSummary{{Name: cfg.Region, Type: resourceSQS, Description: "SQS queues and SNS topics in " + cfg.Region}}, nil
}

// prepareSQS opens the SQS session of options' profile; options.Name, the target, is the
// region when no region is given.
func (r *Runner) prepareSQS(ctx context.Context, options *Options) (*Session, error) {
	if options.SkipMCP {
		return nil, i18n.NewError("jumphost.sqs.no_mcp")
	}
	name, region := strings.TrimSpace(options.Name), strings.TrimSpace(options.AwsRegion)
	switch {
	case region == "":
		options.AwsRegion = name
	case name != "" && name != region:
		return nil, i18n.NewError("jumphost.sqs.region_mismatch", name, region)
	}
	cfg, err := r.resolveAWSConfig(ctx, options)
	if err != nil {
		return nil, err
	}
	if err := r.checkSQS(ctx, cfg); err != nil {
		return nil, i18n.NewError("jumphost.sqs.unreachable", cfg.Profile, cfg.Region, err)
	}
	label := sessionLabel(resourceSQS, cfg.Region, cfg)
	if err := r.registry.claim(label, cfg.Region); err != nil {
		return nil, err
	}
	endpoint := "sqs." + cfg.Region + ".amazonaws.com"
	session := &Session{
		Label: label, ResourceType: resourceSQS, Target: cfg.Region, Profile: cfg.Profile, Region: cfg.Region,
		Details: SessionDetails{
			ConnectedServer: endpoint,
			RestartCommand:  buildRestartCommand(resourceSQS, cfg.Region, "", "", "", cfg),
			Auth:            "AWS profile " + cfg.Profile,
		},
		Intro: []string{fmt.Sprintf("SQS queues and SNS topics of %s in %s, through the AWS API (%s)", cfg.Profile, cfg.Region, endpoint)},
	}
	handle, display := r.startSQSMCP(ctx, options, cfg)
	r.attachMCP(session, handle, display)
	session.release = sessionRelease(func() { r.registry.release(label) }, session.mcp)
	return session, nil
}

// startSQSMCP serves the MCP server of an SQS session and returns its display line ("not
// started: …" on failure, which leaves the session without use but open, as for the other types).
func (r *Runner) startSQSMCP(ctx context.Context, options *Options, cfg awsConfig) (server mcpserve.Session, display string) {
	return serveMCP(ctx, r, options, &cfg, func(projectDir string, awsCfg aws.Config) (*mcpserve.Handle, error) {
		return sqsmcpStart(r.mcpHost, &sqsmcp.SessionConfig{
			AWS: awsCfg, ProjectDir: projectDir, Profile: cfg.Profile, Region: cfg.Region,
			ServerPrefix: r.mcpServerPrefix, LogDir: r.mcpLogDir,
			SkipRegistration: options.SkipMCPRegistration, AllowWrites: options.MCPAllowWrites,
			AllowDequeue: options.MCPAllowWrites && r.sqsAllowDequeue,
		})
	})
}
