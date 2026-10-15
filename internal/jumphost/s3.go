package jumphost

import (
	"context"
	"fmt"
	"strings"

	"github.com/n2jsoft/airlock/internal/i18n"
	"github.com/n2jsoft/airlock/internal/mcpserve"
	"github.com/n2jsoft/airlock/internal/s3mcp"
)

// s3mcpStart starts an S3 session's MCP server; a package variable so tests can replace it.
var s3mcpStart = s3mcp.Start

// checkS3 lists one bucket: the profile may use S3.
func (r *Runner) checkS3(ctx context.Context, cfg awsConfig) error {
	_, err := r.runAWSCapture(ctx, cfg.Args, "s3api", "list-buckets", "--max-buckets", "1", "--output", "json")
	return err
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
	handle, display := r.startS3MCP(options, cfg)
	r.attachMCP(session, handle, display)
	session.release = sessionRelease(func() { r.registry.release(label) }, session.mcp)
	return session, nil
}

// startS3MCP serves the MCP server of an S3 session and returns its display line ("not started: …"
// on failure, which leaves the session without use but open, as for the other types).
func (r *Runner) startS3MCP(options *Options, cfg awsConfig) (handle *mcpserve.Handle, display string) {
	projectDir, err := r.projectDirFor(options)
	if err != nil {
		return nil, fmt.Sprintf("not started: %v", err)
	}
	handle, err = s3mcpStart(r.mcpHost, &s3mcp.SessionConfig{
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
