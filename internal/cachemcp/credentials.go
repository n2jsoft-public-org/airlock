package cachemcp

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/n2jsoft/airlock/internal/mcpserve"
)

// DefaultUser signs in caches that only have a password (legacy AUTH token).
const DefaultUser = "default"

// CredentialProvider returns the user and password signing a new cache connection in.
type CredentialProvider func(ctx context.Context) (user, password string, err error)

func orDefaultUser(user string) string {
	if user = strings.TrimSpace(user); user != "" {
		return user
	}
	return DefaultUser
}

// PasswordCredentials signs user (DefaultUser when empty) in with password.
func PasswordCredentials(user, password string) CredentialProvider {
	user = orDefaultUser(user)
	return func(context.Context) (string, string, error) { return user, password, nil }
}

// SecretCredentials reads the user and password from read (a Secrets Manager secret) on the first
// connection and keeps them; a failed read is retried on the next connection.
func SecretCredentials(read func(ctx context.Context) (user, password string, err error)) CredentialProvider {
	var mu sync.Mutex
	var user, password string
	loaded := false
	return func(ctx context.Context) (string, string, error) {
		mu.Lock()
		defer mu.Unlock()
		if loaded {
			return user, password, nil
		}
		readUser, readPassword, err := read(ctx)
		if err != nil {
			return "", "", err
		}
		user, password, loaded = orDefaultUser(readUser), readPassword, true
		return user, password, nil
	}
}

// IAMCredentials signs user in with a new AWS IAM token per connection, signed with the AWS
// credentials of cfg, the session's profile; the token is for the region of cfg.
func IAMCredentials(cfg *aws.Config, cacheName, user string, serverless bool) CredentialProvider {
	signIn := mcpserve.SignInCredentials(cfg.Credentials)
	return func(ctx context.Context) (string, string, error) {
		creds, err := signIn.Retrieve(ctx)
		if err != nil {
			return "", "", err
		}
		token, err := presignConnect(ctx, creds, cacheName, user, cfg.Region, serverless, time.Now())
		return user, token, err
	}
}
