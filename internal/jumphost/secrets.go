package jumphost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/n2jsoft/airlock/internal/dbmcp"
)

// SecretSummary is one Secrets Manager secret offered to hold the password of a database.
type SecretSummary struct {
	ARN         string
	Name        string
	Description string
	// Linked reports that the secret belongs to the instance: tagged DBInstanceArn, the RDS master
	// user secret, or named after the instance.
	Linked bool
}

// secretListEntry is one secret of `aws secretsmanager list-secrets`.
type secretListEntry struct {
	ARN         string `json:"ARN"`
	Name        string `json:"Name"`
	Description string `json:"Description"`
	Tags        []struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	} `json:"Tags"`
}

// ListDBSecrets returns every secret of the profile and region of options, those linked to the
// RDS instance options.Name first, then by name.
func (r *Runner) ListDBSecrets(ctx context.Context, options *Options) ([]SecretSummary, error) {
	if err := requireAWSCLI(); err != nil {
		return nil, err
	}
	cfg, err := r.resolveAWSConfig(ctx, *options)
	if err != nil {
		return nil, err
	}
	instance := strings.TrimSpace(options.Name)
	rawInstance, err := r.runAWSCapture(ctx, cfg.Args,
		"rds", "describe-db-instances",
		"--db-instance-identifier", instance,
		"--query", "DBInstances[0].{arn:DBInstanceArn,masterSecret:MasterUserSecret.SecretArn}",
		"--output", "json",
	)
	if err != nil {
		return nil, err
	}
	var described struct {
		ARN          string `json:"arn"`
		MasterSecret string `json:"masterSecret"`
	}
	if e := json.Unmarshal([]byte(rawInstance), &described); e != nil {
		return nil, fmt.Errorf("failed to parse RDS instance details: %w", e)
	}

	// The AWS CLI fetches every page of list-secrets before applying --query.
	raw, err := r.runAWSCapture(ctx, cfg.Args,
		"secretsmanager", "list-secrets",
		"--query", "SecretList[].{ARN:ARN,Name:Name,Description:Description,Tags:Tags}",
		"--output", "json",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list the secrets (missing secretsmanager:ListSecrets?): %w", err)
	}
	var entries []secretListEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("failed to parse the secrets: %w", err)
	}
	if len(entries) == 0 {
		return nil, errors.New("no secret in " + cfg.Region)
	}
	return rankDBSecrets(entries, instance, described.ARN, described.MasterSecret), nil
}

// rankDBSecrets summarizes entries, linked to the instance first, then by name.
func rankDBSecrets(entries []secretListEntry, instance, instanceARN, masterSecretARN string) []SecretSummary {
	instance = strings.ToLower(strings.TrimSpace(instance))
	secrets := make([]SecretSummary, 0, len(entries))
	for _, entry := range entries {
		linked := (masterSecretARN != "" && entry.ARN == masterSecretARN) ||
			(instance != "" && strings.Contains(strings.ToLower(entry.Name), instance))
		for _, tag := range entry.Tags {
			if tag.Key == "DBInstanceArn" && instanceARN != "" && tag.Value == instanceARN {
				linked = true
			}
		}
		secrets = append(secrets, SecretSummary{ARN: entry.ARN, Name: entry.Name, Description: entry.Description, Linked: linked})
	}
	sort.SliceStable(secrets, func(i, j int) bool {
		if secrets[i].Linked != secrets[j].Linked {
			return secrets[i].Linked
		}
		return secrets[i].Name < secrets[j].Name
	})
	return secrets
}

// secretARNSuffix is the "-" and 6 random characters Secrets Manager appends to a name in an ARN.
var secretARNSuffix = regexp.MustCompile(`-[A-Za-z0-9]{6}$`)

// SecretDisplayName returns the name of a secret ARN without its random suffix; any other secret
// ID (a name) is returned unchanged.
func SecretDisplayName(secretID string) string {
	secretID = strings.TrimSpace(secretID)
	_, name, ok := strings.Cut(secretID, ":secret:")
	if !strings.HasPrefix(secretID, "arn:") || !ok {
		return secretID
	}
	return secretARNSuffix.ReplaceAllString(name, "")
}

// resolveSecretSignIn reads the password secret once before connecting, so an unreadable secret
// fails before the tunnel opens, and returns the user: the secret's, else typedUser.
func resolveSecretSignIn(ctx context.Context, aws dbmcp.AWSRunner, secretID, typedUser string) (string, error) {
	secret, err := dbmcp.ReadPasswordSecret(ctx, aws, secretID)
	if err != nil {
		return "", fmt.Errorf("failed to read secret %s (needs secretsmanager:GetSecretValue, and kms:Decrypt for a customer-managed key): %w", secretID, err)
	}
	if secret.Username != "" {
		return secret.Username, nil
	}
	if user := strings.TrimSpace(typedUser); user != "" {
		return user, nil
	}
	return "", fmt.Errorf("secret %s has no username: fill the User field", SecretDisplayName(secretID))
}

// MCPEngineSupported reports whether the MCP server can query the RDS engine, the only case where
// airlock itself uses the database credentials.
func MCPEngineSupported(engine string) bool {
	_, ok := dbmcp.EngineFor(engine)
	return ok
}
