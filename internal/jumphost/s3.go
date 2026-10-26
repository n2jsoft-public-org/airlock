package jumphost

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/n2jsoft-public-org/airlock/internal/i18n"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
	"github.com/n2jsoft-public-org/airlock/internal/s3mcp"
)

// s3mcpStart starts an S3 session's MCP server; a package variable so tests can replace it.
var s3mcpStart = s3mcp.Start

// checkS3 lists one bucket: the profile may use S3.
func (r *Runner) checkS3(ctx context.Context, cfg awsConfig) error {
	awsCfg, err := r.apiConfig(ctx, cfg)
	if err != nil {
		return err
	}
	if _, err = s3.NewFromConfig(awsCfg).ListBuckets(ctx, &s3.ListBucketsInput{MaxBuckets: aws.Int32(1)}); err != nil {
		return i18n.NewError("jumphost.s3.list_buckets", awsCallError(err))
	}
	return nil
}

// s3Targets is the one target of a profile, every bucket, when S3 answers.
func (r *Runner) s3Targets(ctx context.Context, cfg awsConfig) ([]ResourceSummary, error) {
	if err := r.checkS3(ctx, cfg); err != nil {
		return nil, err
	}
	return []ResourceSummary{{Name: s3mcp.Target, Type: resourceS3, Description: "S3 buckets of " + cfg.Profile}}, nil
}

// prepareS3 opens the S3 session of options' profile; its only target is s3mcp.Target, the default
// when options.Name is empty.
func (r *Runner) prepareS3(ctx context.Context, options *Options) (*Session, error) {
	if options.SkipMCP {
		return nil, i18n.NewError("jumphost.s3.no_mcp")
	}
	if name := strings.TrimSpace(options.Name); name != "" && name != s3mcp.Target {
		return nil, i18n.NewError("jumphost.s3.unknown_target", name)
	}
	cfg, err := r.resolveAWSConfig(ctx, options)
	if err != nil {
		return nil, err
	}
	if err := r.checkS3(ctx, cfg); err != nil {
		return nil, i18n.NewError("jumphost.s3.unreachable", cfg.Profile, err)
	}
	// Region-free: one S3 session per profile, whatever region it was opened with.
	label := fmt.Sprintf("%s %s (%s)", strings.ToUpper(resourceS3), s3mcp.Target, cfg.Profile)
	if err := r.registry.claim(label, s3mcp.Target); err != nil {
		return nil, err
	}
	endpoint := "s3." + cfg.Region + ".amazonaws.com"
	session := &Session{
		Label: label, ResourceType: resourceS3, Target: s3mcp.Target, Profile: cfg.Profile, Region: cfg.Region,
		Details: SessionDetails{
			ConnectedServer: endpoint,
			RestartCommand:  buildRestartCommand(resourceS3, s3mcp.Target, "", "", "", cfg),
			Auth:            "AWS profile " + cfg.Profile,
		},
		Intro: []string{fmt.Sprintf("S3 buckets of %s, in every region, through the AWS API (%s)", cfg.Profile, endpoint)},
	}
	handle, display := r.startS3MCP(ctx, options, cfg)
	r.attachMCP(session, handle, display)
	session.release = sessionRelease(func() { r.registry.release(label) }, session.mcp)
	return session, nil
}

// startS3MCP serves the MCP server of an S3 session and returns its display line ("not started: …"
// on failure, which leaves the session without use but open, as for the other types).
func (r *Runner) startS3MCP(ctx context.Context, options *Options, cfg awsConfig) (server mcpserve.Session, display string) {
	return serveMCP(ctx, r, options, &cfg, func(projectDir string, awsCfg aws.Config) (*mcpserve.Handle, error) {
		return s3mcpStart(r.mcpHost, &s3mcp.SessionConfig{
			AWS: awsCfg, ProjectDir: projectDir, Profile: cfg.Profile, Region: cfg.Region,
			ServerPrefix: r.mcpServerPrefix, LogDir: r.mcpLogDir,
			SkipRegistration: options.SkipMCPRegistration, AllowWrites: options.MCPAllowWrites,
		})
	})
}
