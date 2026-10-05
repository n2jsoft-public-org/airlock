package kubemcp

import (
	"fmt"
	"strings"

	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// SessionConfig describes the tunneled cluster of one EKS session.
type SessionConfig struct {
	// SkipRegistration serves without writing the AI client files (see mcpserve.ServeConfig).
	SkipRegistration bool
	AWS              mcpserve.AWSRunner
	ProjectDir       string
	Profile          string
	Region           string
	// Target is the cluster name.
	Target string
	// EndpointHost is the real API host the server certificate is checked against.
	EndpointHost string
	// ClusterCA is the base64 PEM certificate authority of the cluster.
	ClusterCA string
	// ServerPrefix starts the client-facing server name (mcpserve.DefaultServerPrefix when empty).
	ServerPrefix string
	// LogDir receives the audit log of the tool calls; empty disables it.
	LogDir    string
	LocalPort int
}

// Start mounts the read-only Kubernetes MCP server of an EKS session on host and registers it in
// the project's AI clients. It returns nil, nil without certificate authority, which leaves the
// cluster impossible to trust.
func Start(host *mcpserve.Host, cfg *SessionConfig) (*mcpserve.Handle, error) {
	if strings.TrimSpace(cfg.ClusterCA) == "" {
		return nil, nil
	}
	client, err := newAPIClient(fmt.Sprintf("https://localhost:%d", cfg.LocalPort), cfg.EndpointHost, cfg.ClusterCA,
		newTokenSource(cfg.AWS, cfg.Target, cfg.Profile))
	if err != nil {
		return nil, err
	}
	// The -eks suffix keeps the name apart from an RDS instance of the same name on the profile.
	name := mcpserve.ServerName(cfg.ServerPrefix, cfg.Profile, cfg.Target) + "-eks"
	recorder := mcpserve.NewRecorder(cfg.LogDir)
	server := NewServer(&ServerConfig{Name: name, Target: cfg.Target, Profile: cfg.Profile, Region: cfg.Region},
		newAPICluster(client), recorder.Record)
	return mcpserve.Serve(host, &mcpserve.ServeConfig{
		Server: server, Recorder: recorder, Name: name, ProjectDir: cfg.ProjectDir, SkipRegistration: cfg.SkipRegistration,
		Cleanup: func() error { client.close(); return nil },
	})
}
