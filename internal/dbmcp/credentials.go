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

	"github.com/n2jsoft/airlock/internal/awstag"
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

func (p *iamTokenProvider) authFailureHint() string {
	// PostgreSQL gives the same answer for an unknown user and for a password user without rds_iam.
	return fmt.Sprintf("%s rejected the AWS IAM token: the user does not exist on this instance, or lacks the rds_iam role "+
		"(GRANT rds_iam TO %s); otherwise connect again with IAM auth turned off and its password", p.user, p.user)
}

// passwordProvider signs in with the user and password entered by the user.
type passwordProvider struct {
	// onRejected, when set, is called once when the database rejects the password.
	onRejected      func()
	user            string
	password        string
	defaultDatabase string
	rejectedOnce    sync.Once
}

func newPasswordProvider(user, password, defaultDatabase string) *passwordProvider {
	return &passwordProvider{user: user, password: password, defaultDatabase: defaultDatabase}
}

func (p *passwordProvider) Credentials(_ context.Context, database string) (Credentials, error) {
	if database == "" {
		database = p.defaultDatabase
	}
	return Credentials{User: p.user, Password: p.password, Database: database}, nil
}

func (*passwordProvider) Databases(context.Context) ([]string, error) { return nil, nil }

func (p *passwordProvider) authFailureHint() string {
	return fmt.Sprintf("the password entered for %s was rejected: connect again with the right one", p.user)
}

// rejected reports that the database refused the password.
func (p *passwordProvider) rejected() {
	if p.onRejected != nil {
		p.rejectedOnce.Do(p.onRejected)
	}
}

// secretPasswordProvider signs in with a password read from a Secrets Manager secret picked by the
// user: the secret's username, else the typed user. The secret is read once, and again after the
// database rejects it (it may have been rotated).
type secretPasswordProvider struct {
	aws             AWSRunner
	creds           *Credentials
	secretID        string
	user            string
	defaultDatabase string
	mu              sync.Mutex
}

func newSecretPasswordProvider(aws AWSRunner, secretID, user, defaultDatabase string) *secretPasswordProvider {
	return &secretPasswordProvider{aws: aws, secretID: secretID, user: strings.TrimSpace(user), defaultDatabase: defaultDatabase}
}

func (p *secretPasswordProvider) Credentials(ctx context.Context, database string) (Credentials, error) {
	if database == "" {
		database = p.defaultDatabase
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.creds == nil {
		secret, err := ReadPasswordSecret(ctx, p.aws, p.secretID)
		if err != nil {
			return Credentials{}, fmt.Errorf("password secret %s is not readable (needs secretsmanager:GetSecretValue, and kms:Decrypt for a customer-managed key): %w", p.secretID, err)
		}
		user := secret.Username
		if user == "" {
			user = p.user
		}
		if user == "" {
			return Credentials{}, fmt.Errorf("secret %s has no username: fill the User field", p.secretID)
		}
		p.creds = &Credentials{User: user, Password: secret.Password}
	}
	return Credentials{User: p.creds.User, Password: p.creds.Password, Database: database}, nil
}

func (*secretPasswordProvider) Databases(context.Context) ([]string, error) { return nil, nil }

// refresh forgets the secret, read again on the next call.
func (p *secretPasswordProvider) refresh() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.creds = nil
}

func (p *secretPasswordProvider) authFailureHint() string {
	return fmt.Sprintf("the credentials of secret %s were rejected, even read again: check it holds the current password", p.secretID)
}

// iamDisabledProvider explains that PostgreSQL MCP access without a password needs AWS IAM
// database authentication, which this instance does not have enabled.
type iamDisabledProvider struct {
	instance string
}

func newIAMDisabledProvider(instance string) *iamDisabledProvider {
	return &iamDisabledProvider{instance: instance}
}

func (p *iamDisabledProvider) err() error {
	return fmt.Errorf(
		"IAM authentication is disabled on %s: MCP access needs it (enable IAM auth, or connect with a database user and password)",
		p.instance)
}

func (p *iamDisabledProvider) Credentials(context.Context, string) (Credentials, error) {
	return Credentials{}, p.err()
}

func (p *iamDisabledProvider) Databases(context.Context) ([]string, error) { return nil, p.err() }

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

// PasswordSecret is a secret holding a database password: RDS JSON (username and password) or the
// password as plain text, without username.
type PasswordSecret struct {
	Username string
	Password string
}

// ReadPasswordSecret reads secretID as a PasswordSecret. Plain text is the whole secret string
// (the AWS CLI output is trimmed, so leading and trailing spaces are lost).
func ReadPasswordSecret(ctx context.Context, aws AWSRunner, secretID string) (PasswordSecret, error) {
	raw, err := aws(ctx, "secretsmanager", "get-secret-value",
		"--secret-id", secretID,
		"--query", "SecretString",
		"--output", "text",
	)
	if err != nil {
		return PasswordSecret{}, err
	}
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") {
		if raw == "" {
			return PasswordSecret{}, fmt.Errorf("secret %s is empty", secretID)
		}
		return PasswordSecret{Password: raw}, nil
	}
	var payload secretPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return PasswordSecret{}, fmt.Errorf("secret %s is not valid JSON: %w", secretID, err)
	}
	if payload.Password == "" {
		return PasswordSecret{}, fmt.Errorf("secret %s has no password", secretID)
	}
	return PasswordSecret{Username: payload.Username, Password: payload.Password}, nil
}

// readOnlySecretsProvider uses the read-only secrets published for an instance, one per database,
// found by their tag.
type readOnlySecretsProvider struct {
	aws         AWSRunner
	byDatabase  map[string]Credentials
	instanceARN string
	instance    string
	tag         awstag.Tag
	warnings    []string
	mu          sync.Mutex
}

func newReadOnlySecretsProvider(aws AWSRunner, instance, instanceARN string, tag awstag.Tag) *readOnlySecretsProvider {
	return &readOnlySecretsProvider{aws: aws, instance: instance, instanceARN: instanceARN, tag: tag}
}

// ReadOnlySecretsQuery keeps the secrets of this instance carrying tag.
func ReadOnlySecretsQuery(tag awstag.Tag, instanceARN string) string {
	return fmt.Sprintf("SecretList[?Tags[?Key=='%s' && Value=='%s'] && Tags[?Key=='DBInstanceArn' && Value=='%s']].ARN", tag.Key, tag.Value, instanceARN)
}

// load discovers and reads the secrets once; failures are not cached so a later call can retry.
func (p *readOnlySecretsProvider) load(ctx context.Context) error {
	if p.byDatabase != nil {
		return nil
	}
	if p.instanceARN == "" {
		return fmt.Errorf("instance ARN of %s is unknown: cannot look up its read-only secrets", p.instance)
	}

	raw, err := p.aws(ctx, "secretsmanager", "list-secrets",
		"--filters", "Key=tag-key,Values=DBInstanceArn", "Key=tag-value,Values="+p.instanceARN,
		"--query", ReadOnlySecretsQuery(p.tag, p.instanceARN),
		"--output", "json",
	)
	if err != nil {
		return fmt.Errorf("failed to list read-only secrets of %s (missing secretsmanager:ListSecrets?): %w", p.instance, err)
	}
	var arns []string
	if err := json.Unmarshal([]byte(raw), &arns); err != nil {
		return fmt.Errorf("failed to parse read-only secrets of %s: %w", p.instance, err)
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
		message := fmt.Sprintf("no usable read-only secret (%s) found for %s (not created yet, or missing secretsmanager:ListSecrets / GetSecretValue; tag from \"readOnlySecretTag\", see `airlock config show`)", p.tag, p.instance)
		if len(warnings) > 0 {
			message += ": " + strings.Join(warnings, "; ")
		}
		return errors.New(message)
	}
	p.byDatabase, p.warnings = byDatabase, warnings
	return nil
}

func (p *readOnlySecretsProvider) Credentials(ctx context.Context, database string) (Credentials, error) {
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
		return Credentials{}, fmt.Errorf("no read-only credentials for database %q (available: %s)", database, strings.Join(p.names(), ", "))
	}
	return creds, nil
}

func (p *readOnlySecretsProvider) Databases(ctx context.Context) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.load(ctx); err != nil {
		return nil, err
	}
	return p.names(), nil
}

// refresh forgets the read-only secrets, discovered and read again on the next call: they may have been rotated.
func (p *readOnlySecretsProvider) refresh() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.byDatabase, p.warnings = nil, nil
}

func (p *readOnlySecretsProvider) names() []string {
	names := make([]string, 0, len(p.byDatabase))
	for name := range p.byDatabase {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// masterSecretProvider uses the RDS-managed master user secret (SQL Server writes mode).
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

// refresh forgets the master secret, read again on the next call: RDS may have rotated it.
func (p *masterSecretProvider) refresh() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.creds = nil
}
