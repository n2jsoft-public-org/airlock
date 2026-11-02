package sqsmcp

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/n2jsoft-public-org/airlock/internal/mcpserve"
)

// SessionConfig describes the queues and topics of one session: an AWS profile and region.
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
	// AllowWrites exposes repost_message; IAM still decides what is allowed.
	AllowWrites bool
}

// Start mounts the MCP server of an SQS session on host and registers it in the project's AI
// clients.
func Start(host *mcpserve.Host, cfg *SessionConfig) (*mcpserve.Handle, error) {
	name := mcpserve.ServerName(cfg.ServerPrefix, cfg.Profile, "sqs-"+cfg.Region)
	recorder := mcpserve.NewRecorder(cfg.LogDir)
	credentials := mcpserve.SignInCredentials(cfg.AWS.Credentials)
	sqsClient := sqs.NewFromConfig(cfg.AWS, func(o *sqs.Options) { o.Credentials = credentials })
	snsClient := sns.NewFromConfig(cfg.AWS, func(o *sns.Options) { o.Credentials = credentials })
	server := NewServer(&ServerConfig{Name: name, Profile: cfg.Profile, Region: cfg.Region, AllowWrites: cfg.AllowWrites},
		newQueues(sqsClient), newTopics(snsClient), recorder.Record)
	return mcpserve.Serve(host, &mcpserve.ServeConfig{
		Server: server, Recorder: recorder, Name: name, ProjectDir: cfg.ProjectDir,
		AllowWrites: cfg.AllowWrites, SkipRegistration: cfg.SkipRegistration,
	})
}
