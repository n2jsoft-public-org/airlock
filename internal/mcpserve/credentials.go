package mcpserve

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// credentialsRefreshMargin renews exported credentials this long before they expire.
const credentialsRefreshMargin = 5 * time.Minute

// ExportedCredentials reads the AWS credentials of a session's profile with
// aws configure export-credentials, and keeps them until shortly before they expire. It is an
// aws.CredentialsProvider.
type ExportedCredentials struct {
	expires time.Time
	aws     AWSRunner
	now     func() time.Time
	creds   aws.Credentials
	mu      sync.Mutex
	cached  bool
}

// CredentialsError is a failure to export the profile's credentials, typically an expired sign-in.
type CredentialsError struct{ Err error }

func (e *CredentialsError) Error() string {
	return "failed to read the AWS credentials of the profile (sign in again from airlock): " + e.Err.Error()
}

func (e *CredentialsError) Unwrap() error { return e.Err }

// NewExportedCredentials reads the credentials with run, the session's aws runner.
func NewExportedCredentials(run AWSRunner) *ExportedCredentials {
	return &ExportedCredentials{aws: run, now: time.Now}
}

// Retrieve returns the credentials, reading them again once they near their expiry.
func (e *ExportedCredentials) Retrieve(ctx context.Context) (aws.Credentials, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cached && (e.expires.IsZero() || e.now().Before(e.expires.Add(-credentialsRefreshMargin))) {
		return e.creds, nil
	}
	raw, err := e.aws(ctx, "configure", "export-credentials", "--format", "process")
	if err != nil {
		return aws.Credentials{}, &CredentialsError{Err: err}
	}
	var exported struct {
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string `json:"SecretAccessKey"`
		SessionToken    string `json:"SessionToken"`
		Expiration      string `json:"Expiration"`
	}
	if json.Unmarshal([]byte(raw), &exported) != nil || exported.AccessKeyID == "" {
		return aws.Credentials{}, errors.New("aws configure export-credentials printed no credentials")
	}
	var expires time.Time
	// Credentials whose expiry cannot be read are used once, never cached: they may expire anytime.
	readable := true
	if exported.Expiration != "" {
		if expires, err = time.Parse(time.RFC3339, exported.Expiration); err != nil {
			expires, readable = time.Time{}, false
		}
	}
	e.creds = aws.Credentials{
		AccessKeyID: exported.AccessKeyID, SecretAccessKey: exported.SecretAccessKey, SessionToken: exported.SessionToken,
		CanExpire: !expires.IsZero(), Expires: expires,
	}
	e.expires, e.cached = expires, readable
	return e.creds, nil
}
