// Package kubeconfig writes the kubeconfig files pointing Kubernetes clients at tunneled clusters.
package kubeconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// execAPIVersion is the ExecCredential version printed by `aws eks get-token`.
const execAPIVersion = "client.authentication.k8s.io/v1beta1"

// Cluster describes a tunneled cluster and how its clients get a token.
type Cluster struct {
	// Name names the cluster, context and user entries, and the file.
	Name string
	// Server is the URL clients connect to, e.g. https://localhost:15443.
	Server string
	// TLSServerName is the host the server certificate is checked against: the real endpoint.
	TLSServerName string
	// CertificateAuthorityData is the base64 PEM of the cluster certificate authority.
	CertificateAuthorityData string
	// Command and Args print an ExecCredential holding a token, e.g. aws eks get-token.
	Command string
	Args    []string
}

// unsafeNameChars are replaced in file names, so a name never leaves the directory.
var unsafeNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Write writes a kubeconfig whose only context is c to dir, created when missing, and returns
// its path and a function removing it. JSON is valid YAML, so every Kubernetes client reads it.
func Write(dir string, c *Cluster) (path string, remove func() error, err error) {
	content, err := json.MarshalIndent(document(c), "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("failed to encode the kubeconfig: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("failed to create %s: %w", dir, err)
	}
	path = filepath.Join(dir, unsafeNameChars.ReplaceAllString(c.Name, "-")+".json")
	if err := os.WriteFile(path, append(content, '\n'), 0o600); err != nil {
		return "", nil, fmt.Errorf("failed to write %s: %w", path, err)
	}
	remove = func() error {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return path, remove, nil
}

type config struct {
	APIVersion     string         `json:"apiVersion"`
	Kind           string         `json:"kind"`
	CurrentContext string         `json:"current-context"`
	Clusters       []namedCluster `json:"clusters"`
	Contexts       []namedContext `json:"contexts"`
	Users          []namedUser    `json:"users"`
}

type namedCluster struct {
	Name    string      `json:"name"`
	Cluster clusterInfo `json:"cluster"`
}

type clusterInfo struct {
	Server                   string `json:"server"`
	TLSServerName            string `json:"tls-server-name,omitempty"`
	CertificateAuthorityData string `json:"certificate-authority-data,omitempty"`
}

type namedContext struct {
	Name    string      `json:"name"`
	Context contextInfo `json:"context"`
}

type contextInfo struct {
	Cluster string `json:"cluster"`
	User    string `json:"user"`
}

type namedUser struct {
	Name string   `json:"name"`
	User userInfo `json:"user"`
}

type userInfo struct {
	Exec execConfig `json:"exec"`
}

type execConfig struct {
	APIVersion      string   `json:"apiVersion"`
	Command         string   `json:"command"`
	InteractiveMode string   `json:"interactiveMode"`
	Args            []string `json:"args"`
}

func document(c *Cluster) config {
	return config{
		APIVersion:     "v1",
		Kind:           "Config",
		CurrentContext: c.Name,
		Clusters: []namedCluster{{Name: c.Name, Cluster: clusterInfo{
			Server:                   c.Server,
			TLSServerName:            c.TLSServerName,
			CertificateAuthorityData: c.CertificateAuthorityData,
		}}},
		Contexts: []namedContext{{Name: c.Name, Context: contextInfo{Cluster: c.Name, User: c.Name}}},
		Users: []namedUser{{Name: c.Name, User: userInfo{Exec: execConfig{
			APIVersion: execAPIVersion,
			Command:    c.Command,
			// The token command never prompts: an expired SSO session fails with its own message.
			InteractiveMode: "Never",
			Args:            c.Args,
		}}}},
	}
}
