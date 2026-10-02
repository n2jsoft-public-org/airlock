package jumphost

import (
	"context"
	"errors"

	"github.com/n2jsoft/airlock/internal/dbmcp"
)

// checkDirectSignIn signs in to a direct database and runs SELECT 1; a package variable so tests
// need no database.
var checkDirectSignIn = dbmcp.CheckDirectSignIn

// directSignIn is the SignInChecker of a direct database session: its only candidate is the
// password the session was opened with.
type directSignIn struct {
	// forget deletes the keychain password once the database rejected it; nil when not remembered.
	forget   func()
	engine   dbmcp.Engine
	user     string
	password string
	// database is signed in to by the check; empty is the login's default (SQL Server).
	database string
	addr     dbmcp.Address
}

func (s *directSignIn) Candidates(context.Context) ([]SignInCandidate, error) {
	return []SignInCandidate{{Label: "password", User: s.user}}, nil
}

func (s *directSignIn) Check(ctx context.Context, c SignInCandidate) error {
	err := checkDirectSignIn(ctx, s.engine, dbmcp.PasswordCredentials(c.User, s.password, s.database), s.addr)
	if errors.Is(err, dbmcp.ErrSignInRejected) && s.forget != nil {
		s.forget()
	}
	return err
}

// Use does nothing: the only candidate is the session's own password.
func (*directSignIn) Use(SignInCandidate, *Options) {}

// Forget never applies: a direct database has no secret to fall back from.
func (*directSignIn) Forget(SignInCandidate, *Options) bool { return false }
