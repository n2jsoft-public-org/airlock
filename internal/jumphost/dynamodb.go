package jumphost

import (
	"context"
	"fmt"
	"strings"

	"github.com/n2jsoft/airlock/internal/dynamomcp"
	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// resourceDynamoDB is the session type of the DynamoDB tables of a profile and region: no
// jumphost, no tunnel, no local port; its MCP server is the session.
const resourceDynamoDB = "dynamodb"

// dynamomcpStart starts a DynamoDB session's MCP server; a package variable so tests can replace it.
var dynamomcpStart = dynamomcp.Start

// checkDynamoDB lists one table: the profile may use DynamoDB in its region.
func (r *Runner) checkDynamoDB(ctx context.Context, cfg awsConfig) error {
	_, err := r.runAWSCapture(ctx, cfg.Args, "dynamodb", "list-tables", "--max-items", "1", "--output", "json")
	return err
}

// dynamoDBTargets is the one target of a profile: its region, when DynamoDB answers there.
func (r *Runner) dynamoDBTargets(ctx context.Context, cfg awsConfig) ([]ResourceSummary, error) {
	if err := r.checkDynamoDB(ctx, cfg); err != nil {
		return nil, err
	}
	return []ResourceSummary{{Name: cfg.Region, Type: resourceDynamoDB, Description: "DynamoDB tables in " + cfg.Region}}, nil
}

// prepareDynamoDB opens the DynamoDB session of options' profile; options.Name, the target, is the
// region when no region is given.
func (r *Runner) prepareDynamoDB(ctx context.Context, options *Options) (*Session, error) {
	if options.SkipMCP {
		return nil, i18n.NewError("jumphost.dynamodb.no_mcp")
	}
	name, region := strings.TrimSpace(options.Name), strings.TrimSpace(options.AwsRegion)
	switch {
	case region == "":
		options.AwsRegion = name
	case name != "" && name != region:
		return nil, i18n.NewError("jumphost.dynamodb.region_mismatch", name, region)
	}
	cfg, err := r.resolveAWSConfig(ctx, options)
	if err != nil {
		return nil, err
	}
	if err := r.checkDynamoDB(ctx, cfg); err != nil {
		return nil, i18n.NewError("jumphost.dynamodb.unreachable", cfg.Profile, cfg.Region, err)
	}
	label := sessionLabel(resourceDynamoDB, cfg.Region, cfg)
	if err := r.registry.claim(label, cfg.Region); err != nil {
		return nil, err
	}
	endpoint := "dynamodb." + cfg.Region + ".amazonaws.com"
	session := &Session{
		Label: label, ResourceType: resourceDynamoDB, Target: cfg.Region, Profile: cfg.Profile, Region: cfg.Region,
		Details: SessionDetails{
			ConnectedServer: endpoint,
			RestartCommand:  buildRestartCommand(resourceDynamoDB, cfg.Region, "", "", "", cfg),
			Auth:            "AWS profile " + cfg.Profile,
		},
		Intro: []string{fmt.Sprintf("DynamoDB tables of %s in %s, through the AWS API (%s)", cfg.Profile, cfg.Region, endpoint)},
	}
	handle, display := r.startDynamoMCP(options, cfg)
	r.attachMCP(session, handle, display)
	session.release = sessionRelease(func() { r.registry.release(label) }, session.mcp)
	return session, nil
}

// startDynamoMCP serves the MCP server of a DynamoDB session and returns its display line ("not
// started: …" on failure, which leaves the session without use but open, as for the other types).
func (r *Runner) startDynamoMCP(options *Options, cfg awsConfig) (handle *mcpserve.Handle, display string) {
	projectDir, err := r.projectDirFor(options)
	if err != nil {
		return nil, fmt.Sprintf("not started: %v", err)
	}
	handle, err = dynamomcpStart(r.mcpHost, &dynamomcp.SessionConfig{
		AWS: r.awsRunner(cfg), ProjectDir: projectDir, Profile: cfg.Profile, Region: cfg.Region,
		ServerPrefix: r.mcpServerPrefix, LogDir: r.mcpLogDir,
		SkipRegistration: options.SkipMCPRegistration, AllowWrites: options.MCPAllowWrites,
	})
	if err != nil {
		return nil, fmt.Sprintf("not started: %v", err)
	}
	if handle == nil {
		return nil, ""
	}
	display = handle.Display()
	return handle, display
}
