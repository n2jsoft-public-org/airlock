package jumphost

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/n2jsoft/airlock/internal/ideadatasource"
)

// registerIDEDataSource adds the forwarded connection to the IntelliJ project in the working directory.
// It returns a display message and a function removing the entry again and describing the outcome
// (nil when nothing was registered).
// Failures are reported as a warning and never abort the session.
func (r *Runner) registerIDEDataSource(options *Options, resourceType, targetName, localPort string, target *ideTarget, cfg awsConfig) (message string, remove func() string) {
	if strings.TrimSpace(target.Engine) == "" {
		return "", nil
	}

	port, err := strconv.Atoi(strings.TrimSpace(localPort))
	if err != nil || port <= 0 {
		return "", nil
	}

	dataSource := &ideadatasource.DataSource{
		Key:        strings.Join([]string{resourceType, cfg.Profile, cfg.Region, targetName}, "|"),
		Name:       fmt.Sprintf("airlock %s %s", cfg.Profile, targetName),
		Engine:     target.Engine,
		Host:       "localhost",
		Port:       port,
		Database:   target.Database,
		Properties: awsIAMProperties(target, cfg),
	}
	dataSource.User = target.DBUser
	if target.IAMAuth {
		dataSource.AuthProvider = awsIAMAuthProvider
	}
	return r.registerDataSource(options, dataSource, fmt.Sprintf("localhost:%d", port))
}

// registerDataSource adds dataSource to the IntelliJ project in the working directory; endpoint is
// where it connects, for display. It returns a display message and a function removing the entry
// again (nil when nothing was registered). Failures are reported as a warning and never abort the
// session.
func (r *Runner) registerDataSource(options *Options, dataSource *ideadatasource.DataSource, endpoint string) (message string, remove func() string) {
	projectDir, err := r.projectDirFor(options)
	if err != nil {
		return "", nil
	}

	result, err := ideadatasource.Register(projectDir, dataSource)
	if err != nil {
		fprintf(r.err, "Warning: failed to register IntelliJ data source: %v\n", err)
		return "", nil
	}
	if !result.Registered {
		return "", nil
	}

	key := dataSource.Key
	remove = func() string {
		unregistered, err := ideadatasource.Unregister(projectDir, key)
		switch {
		case err != nil:
			return fmt.Sprintf("Warning: failed to remove IntelliJ data source %q: %v", result.Name, err)
		case unregistered.Removed:
			return fmt.Sprintf("IntelliJ data source %q removed from %s", result.Name, relativePaths(projectDir, result.Paths))
		case unregistered.ActiveSessions > 0:
			return fmt.Sprintf("IntelliJ data source %q kept: still used by %d other session(s)", result.Name, unregistered.ActiveSessions)
		default:
			return ""
		}
	}

	return fmt.Sprintf("IntelliJ data source %q -> %s (%s)", result.Name, endpoint, relativePaths(projectDir, result.Paths)), remove
}

// awsIAMAuthProvider is the IntelliJ authentication method "AWS IAM".
const awsIAMAuthProvider = "aws.rds.iam"

// awsIAMProperties prefills the AWS Toolkit settings used by IntelliJ's "AWS IAM" authentication.
func awsIAMProperties(target *ideTarget, cfg awsConfig) map[string]string {
	if !target.IAMAuth {
		return nil
	}
	return map[string]string{
		"AWS.RdsSigningHost": target.RDSHost,
		"AWS.RdsSigningPort": strconv.Itoa(target.RDSPort),
		"AWS.RegionId":       cfg.Region,
		"AWS.CredentialId":   "profile:" + cfg.Profile,
	}
}

// projectDirFor is where the opening of options registers its IDE data source and MCP servers: its
// own project directory, else the runner's, else the working directory.
func (r *Runner) projectDirFor(options *Options) (string, error) {
	if options != nil && options.ProjectDir != "" {
		return options.ProjectDir, nil
	}
	if r.projectDir != "" {
		return r.projectDir, nil
	}
	return os.Getwd()
}

func relativePaths(base string, paths []string) string {
	relative := make([]string, 0, len(paths))
	for _, path := range paths {
		if rel, err := filepath.Rel(base, path); err == nil {
			path = rel
		}
		relative = append(relative, path)
	}
	return strings.Join(relative, ", ")
}
