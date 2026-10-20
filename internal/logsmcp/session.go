package logsmcp

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// SessionConfig describes the log groups of one session: an AWS profile and region. It is always
// read-only.
type SessionConfig struct {
	// AWS is the SDK configuration of the session's profile and region.
	AWS        aws.Config
	ProjectDir string
	Profile    string
	Region     string
	// ServerPrefix starts the client-facing server name (mcpserve.DefaultServerPrefix when empty).
	ServerPrefix string
	// LogDir receives the audit log of the tool calls; empty disables it.
	LogDir string
	// SkipRegistration serves without writing the AI client files (see mcpserve.ServeConfig).
	SkipRegistration bool
}

// Start mounts the MCP server of a CloudWatch Logs session on host and registers it in the
// project's AI clients.
func Start(host *mcpserve.Host, cfg *SessionConfig) (*mcpserve.Handle, error) {
	name := mcpserve.ServerName(cfg.ServerPrefix, cfg.Profile, "logs-"+cfg.Region)
	recorder := mcpserve.NewRecorder(cfg.LogDir)
	client := cloudwatchlogs.NewFromConfig(cfg.AWS, func(o *cloudwatchlogs.Options) {
		o.Credentials = mcpserve.SignInCredentials(cfg.AWS.Credentials)
	})
	server := NewServer(&ServerConfig{Name: name, Profile: cfg.Profile, Region: cfg.Region}, newLogs(client), recorder.Record)
	return mcpserve.Serve(host, &mcpserve.ServeConfig{
		Server: server, Recorder: recorder, Name: name, ProjectDir: cfg.ProjectDir, SkipRegistration: cfg.SkipRegistration,
	})
}
