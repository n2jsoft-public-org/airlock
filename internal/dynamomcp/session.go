package dynamomcp

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// SessionConfig describes the DynamoDB tables of one session: an AWS profile and region.
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
	// AllowWrites exposes put_item, update_item and delete_item; IAM still decides what is allowed.
	AllowWrites bool
}

// Start mounts the MCP server of a DynamoDB session on host and registers it in the project's AI
// clients.
func Start(host *mcpserve.Host, cfg *SessionConfig) (*mcpserve.Handle, error) {
	name := mcpserve.ServerName(cfg.ServerPrefix, cfg.Profile, "dynamodb-"+cfg.Region)
	recorder := mcpserve.NewRecorder(cfg.LogDir)
	client := dynamodb.NewFromConfig(cfg.AWS, func(o *dynamodb.Options) {
		o.Credentials = mcpserve.SignInCredentials(cfg.AWS.Credentials)
	})
	server := NewServer(&ServerConfig{Name: name, Profile: cfg.Profile, Region: cfg.Region, AllowWrites: cfg.AllowWrites},
		newTables(client), recorder.Record)
	return mcpserve.Serve(host, &mcpserve.ServeConfig{
		Server: server, Recorder: recorder, Name: name, ProjectDir: cfg.ProjectDir,
		AllowWrites: cfg.AllowWrites, SkipRegistration: cfg.SkipRegistration,
	})
}
