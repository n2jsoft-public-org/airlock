package s3mcp

import (
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// SessionConfig describes the buckets of one session: an AWS profile, and its region for the calls
// that are not about a bucket.
type SessionConfig struct {
	// AWS runs the aws CLI with the session's profile and region; it exports the credentials.
	AWS        mcpserve.AWSRunner
	ProjectDir string
	Profile    string
	Region     string
	// ServerPrefix starts the client-facing server name (mcpserve.DefaultServerPrefix when empty).
	ServerPrefix string
	// LogDir receives the audit log of the tool calls; empty disables it.
	LogDir string
	// SkipRegistration serves without writing the AI client files (see mcpserve.ServeConfig).
	SkipRegistration bool
	// AllowWrites exposes put_object and delete_object; IAM still decides what is allowed.
	AllowWrites bool
}

// Start mounts the MCP server of an S3 session on host and registers it in the project's AI
// clients. The client signs its requests with the profile's exported credentials, never with the
// SDK's own configuration loading.
func Start(host *mcpserve.Host, cfg *SessionConfig) (*mcpserve.Handle, error) {
	name := mcpserve.ServerName(cfg.ServerPrefix, cfg.Profile, "s3")
	recorder := mcpserve.NewRecorder(cfg.LogDir)
	client := s3.New(s3.Options{Region: cfg.Region, Credentials: mcpserve.NewExportedCredentials(cfg.AWS)})
	server := NewServer(&ServerConfig{Name: name, Profile: cfg.Profile, Region: cfg.Region, AllowWrites: cfg.AllowWrites},
		newBuckets(client), recorder.Record)
	return mcpserve.Serve(host, &mcpserve.ServeConfig{
		Server: server, Recorder: recorder, Name: name, ProjectDir: cfg.ProjectDir,
		AllowWrites: cfg.AllowWrites, SkipRegistration: cfg.SkipRegistration,
	})
}
