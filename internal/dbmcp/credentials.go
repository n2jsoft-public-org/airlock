package dbmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// iamTokenMaxAge is how long an RDS IAM token is reused; AWS accepts it for 15 minutes.
const iamTokenMaxAge = 10 * time.Minute

// AWSRunner runs an AWS CLI command (profile and region already applied) and returns its stdout.
type AWSRunner func(ctx context.Context, args ...string) (string, error)

// Credentials authenticate one database connection.
type Credentials struct {
	User     string
	Password string
	Database string
}

// CredentialProvider resolves database credentials for a session.
type CredentialProvider interface {
	// Credentials returns the credentials for database; "" selects the default database,
	// and the resolved name is returned in Credentials.Database.
	Credentials(ctx context.Context, database string) (Credentials, error)
	// Databases lists the databases these credentials may use, or nil when the server catalog decides.
	Databases(ctx context.Context) ([]string, error)
}

// iamTokenProvider signs in as an RDS IAM user with short-lived auth tokens.
type iamTokenProvider struct {
	issued          time.Time
	aws             AWSRunner
	now             func() time.Time
	rdsHost         string
	user            string
	defaultDatabase string
	token           string
	rdsPort         int
	mu              sync.Mutex
}

func newIAMTokenProvider(aws AWSRunner, rdsHost string, rdsPort int, user, defaultDatabase string) *iamTokenProvider {
	return &iamTokenProvider{aws: aws, now: time.Now, rdsHost: rdsHost, rdsPort: rdsPort, user: user, defaultDatabase: defaultDatabase}
}

func (p *iamTokenProvider) Credentials(ctx context.Context, database string) (Credentials, error) {
	if database == "" {
		database = p.defaultDatabase
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token == "" || p.now().Sub(p.issued) >= iamTokenMaxAge {
		token, err := p.aws(ctx, "rds", "generate-db-auth-token",
			"--hostname", p.rdsHost,
			"--port", strconv.Itoa(p.rdsPort),
			"--username", p.user,
		)
		if err != nil {
			return Credentials{}, fmt.Errorf("failed to generate an IAM auth token for %s: %w", p.user, err)
		}
		p.token, p.issued = strings.TrimSpace(token), p.now()
	}
	return Credentials{User: p.user, Password: p.token, Database: database}, nil
}

func (*iamTokenProvider) Databases(context.Context) ([]string, error) { return nil, nil }

// secretPayload is the JSON stored in RDS credential secrets.
type secretPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
	DBName   string `json:"dbname"`
	Database string `json:"database"`
}

func readSecret(ctx context.Context, aws AWSRunner, secretID string) (secretPayload, error) {
	raw, err := aws(ctx, "secretsmanager", "get-secret-value",
		"--secret-id", secretID,
		"--query", "SecretString",
		"--output", "text",
	)
	if err != nil {
		return secretPayload{}, err
	}
	var payload secretPayload
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &payload); err != nil {
		return secretPayload{}, fmt.Errorf("secret %s is not a JSON credential: %w", secretID, err)
	}
	if payload.Username == "" || payload.Password == "" {
		return secretPayload{}, fmt.Errorf("secret %s has no username/password", secretID)
	}
	return payload, nil
}

// supportSecretsProvider uses the read-only "ro-access" secrets published for an instance, one per database.
type supportSecretsProvider struct {
	aws         AWSRunner
	byDatabase  map[string]Credentials
	instanceARN string
	instance    string
	warnings    []string
	mu          sync.Mutex
}

func newSupportSecretsProvider(aws AWSRunner, instance, instanceARN string) *supportSecretsProvider {
	return &supportSecretsProvider{aws: aws, instance: instance, instanceARN: instanceARN}
}

// SupportSecretsQuery keeps the secrets tagged for this instance with AccessRole=ro-access (same filter as jumphost.ps1).
func SupportSecretsQuery(instanceARN string) string {
	return fmt.Sprintf("SecretList[?Tags[?Key=='AccessRole' && Value=='ro-access'] && Tags[?Key=='DBInstanceArn' && Value=='%s']].ARN", instanceARN)
}

// load discovers and reads the secrets once; failures are not cached so a later call can retry.
func (p *supportSecretsProvider) load(ctx context.Context) error {
	if p.byDatabase != nil {
		return nil
	}
	if p.instanceARN == "" {
		return fmt.Errorf("instance ARN of %s is unknown: cannot look up its ro-access secrets", p.instance)
	}

	raw, err := p.aws(ctx, "secretsmanager", "list-secrets",
		"--filters", "Key=tag-key,Values=DBInstanceArn", "Key=tag-value,Values="+p.instanceARN,
		"--query", SupportSecretsQuery(p.instanceARN),
		"--output", "json",
	)
	if err != nil {
		return fmt.Errorf("failed to list ro-access secrets of %s (missing secretsmanager:ListSecrets?): %w", p.instance, err)
	}
	var arns []string
	if err := json.Unmarshal([]byte(raw), &arns); err != nil {
		return fmt.Errorf("failed to parse ro-access secrets of %s: %w", p.instance, err)
	}

	byDatabase := map[string]Credentials{}
	var warnings []string
	for _, arn := range arns {
		payload, err := readSecret(ctx, p.aws, arn)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		database := payload.DBName
		if database == "" {
			database = payload.Database
		}
		if database == "" {
			warnings = append(warnings, fmt.Sprintf("secret %s names no database; skipped", arn))
			continue
		}
		byDatabase[database] = Credentials{User: payload.Username, Password: payload.Password, Database: database}
	}
	if len(byDatabase) == 0 {
		message := fmt.Sprintf("no usable ro-access secret found for %s (not created yet, or missing secretsmanager:ListSecrets / GetSecretValue)", p.instance)
		if len(warnings) > 0 {
			message += ": " + strings.Join(warnings, "; ")
		}
		return errors.New(message)
	}
	p.byDatabase, p.warnings = byDatabase, warnings
	return nil
}

func (p *supportSecretsProvider) Credentials(ctx context.Context, database string) (Credentials, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.load(ctx); err != nil {
		return Credentials{}, err
	}

	if database == "" {
		if len(p.byDatabase) == 1 {
			for _, creds := range p.byDatabase {
				return creds, nil
			}
		}
		return Credentials{}, fmt.Errorf("several databases are available (%s): pass the database argument", strings.Join(p.names(), ", "))
	}
	creds, ok := p.byDatabase[database]
	if !ok {
		return Credentials{}, fmt.Errorf("no ro-access credentials for database %q (available: %s)", database, strings.Join(p.names(), ", "))
	}
	return creds, nil
}

func (p *supportSecretsProvider) Databases(ctx context.Context) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.load(ctx); err != nil {
		return nil, err
	}
	return p.names(), nil
}

func (p *supportSecretsProvider) names() []string {
	names := make([]string, 0, len(p.byDatabase))
	for name := range p.byDatabase {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// masterSecretProvider uses the RDS-managed master user secret (writes mode).
type masterSecretProvider struct {
	aws             AWSRunner
	creds           *Credentials
	secretARN       string
	instance        string
	defaultDatabase string
	mu              sync.Mutex
}

func newMasterSecretProvider(aws AWSRunner, instance, secretARN, defaultDatabase string) *masterSecretProvider {
	return &masterSecretProvider{aws: aws, instance: instance, secretARN: secretARN, defaultDatabase: defaultDatabase}
}

func (p *masterSecretProvider) Credentials(ctx context.Context, database string) (Credentials, error) {
	if p.secretARN == "" {
		return Credentials{}, fmt.Errorf("%s has no RDS-managed master user secret: writes cannot be enabled", p.instance)
	}
	if database == "" {
		database = p.defaultDatabase
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.creds == nil {
		payload, err := readSecret(ctx, p.aws, p.secretARN)
		if err != nil {
			return Credentials{}, fmt.Errorf("master user secret of %s is not readable: %w", p.instance, err)
		}
		p.creds = &Credentials{User: payload.Username, Password: payload.Password}
	}
	return Credentials{User: p.creds.User, Password: p.creds.Password, Database: database}, nil
}

func (*masterSecretProvider) Databases(context.Context) ([]string, error) { return nil, nil }
