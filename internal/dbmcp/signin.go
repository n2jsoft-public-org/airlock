package dbmcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// ErrSignInRejected reports credentials the database refused: wrong password or unknown user.
var ErrSignInRejected = errors.New("the database rejected the credentials")

// ErrEngineUnsupported reports an RDS engine airlock cannot query.
var ErrEngineUnsupported = errors.New("engine not supported")

// signInCheckQuery is the test query: valid on every supported engine, it reads nothing.
const signInCheckQuery = "SELECT 1"

// DefaultDatabase is the database a sign-in opens when none is named: dbName, else postgres on
// PostgreSQL, else the login's default ("").
func DefaultDatabase(awsEngine, dbName string) string {
	engine, _ := EngineFor(awsEngine)
	return defaultDatabaseFor(engine, dbName)
}

func defaultDatabaseFor(engine Engine, dbName string) string {
	if dbName == "" && engine == EnginePostgres {
		return "postgres"
	}
	return dbName
}

// IAMTokenCredentials signs user in with AWS IAM tokens generated for rdsHost:rdsPort.
func IAMTokenCredentials(cfg *aws.Config, rdsHost string, rdsPort int, user, defaultDatabase string) CredentialProvider {
	return newIAMTokenProvider(cfg, rdsHost, rdsPort, user, defaultDatabase)
}

// PasswordCredentials signs user in with password.
func PasswordCredentials(user, password, defaultDatabase string) CredentialProvider {
	return newPasswordProvider(user, password, defaultDatabase)
}

// SecretCredentials signs in with the password of secretID, as its username, else as user.
func SecretCredentials(cfg *aws.Config, secretID, user, defaultDatabase string) CredentialProvider {
	return newSecretPasswordProvider(cfg, secretID, user, defaultDatabase)
}

// CheckSignIn signs in through the tunnel on localhost:port with creds and runs SELECT 1 on the
// default database. A refused password comes back wrapped in ErrSignInRejected.
func CheckSignIn(ctx context.Context, awsEngine string, creds CredentialProvider, port int) error {
	engine, ok := EngineFor(awsEngine)
	if !ok {
		return fmt.Errorf("%w: %s", ErrEngineUnsupported, awsEngine)
	}
	return checkSignIn(ctx, func() Querier { return newQuerier(engine, creds, TunnelAddress(port)) })
}

// CheckDirectSignIn signs in to the database reached directly at addr with creds and runs SELECT 1
// on the default database. A refused password comes back wrapped in ErrSignInRejected.
func CheckDirectSignIn(ctx context.Context, engine Engine, creds CredentialProvider, addr Address) error {
	if !engine.direct() {
		return fmt.Errorf("%w: %s", ErrEngineUnsupported, engine)
	}
	return checkSignIn(ctx, func() Querier { return newQuerier(engine, creds, addr) })
}

// ReadOnlyRestsOnGrants reports whether read-only access to engine relies on the SQL guard and the
// user's grants only: SQL Server has no read-only transaction.
func ReadOnlyRestsOnGrants(engine Engine) bool {
	return !dialectFor(engine).readOnlyTx
}

// checkSignIn runs the test query on the querier open returns and closes it; an error of the query
// wins over one of the close.
func checkSignIn(ctx context.Context, open func() Querier) (err error) {
	q := open()
	defer func() {
		if closeErr := q.Close(); err == nil {
			err = closeErr
		}
	}()
	if _, err = q.Query(ctx, "", signInCheckQuery, 1); err != nil && isPasswordRejected(err) {
		return fmt.Errorf("%w: %w", ErrSignInRejected, err)
	}
	return err
}
