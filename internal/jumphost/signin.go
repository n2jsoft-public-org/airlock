package jumphost

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/n2jsoft/airlock/internal/cachemcp"
	"github.com/n2jsoft/airlock/internal/dbmcp"
)

// ErrSignInRejected reports credentials the database refused (see SignInChecker.Check).
var ErrSignInRejected = dbmcp.ErrSignInRejected

// SignInCandidate is one set of credentials a session may sign in with; the password is only read
// when checked.
type SignInCandidate struct {
	// Label names it for display: the secret's name, else the method ("AWS IAM", "password").
	Label string
	User  string
	// SecretARN is the secret holding the password; empty for AWS IAM and typed passwords.
	SecretARN string
	// Discovered marks a read-only secret found for the Auto sign-in, remembered once it works.
	Discovered bool
}

// SignInChecker resolves and checks the database credentials of a session once its tunnel is up.
type SignInChecker interface {
	// Candidates lists the credentials the session may sign in with, reading the read-only secrets
	// of the instance for the Auto sign-in; never the master user secret.
	Candidates(ctx context.Context) ([]SignInCandidate, error)
	// Check signs in with c through the tunnel and runs SELECT 1; ErrSignInRejected when the
	// database refuses the password.
	Check(ctx context.Context, c SignInCandidate) error
	// Use switches the session to c (MCP server, IDE data source, Auth and restart details, the
	// password dialog) and updates options to prepare the session again with c.
	Use(c SignInCandidate, options *Options)
	// Forget updates options to what to remember when the database rejected c, a secret saved for
	// the target: the Auto sign-in, so the next candidates are the read-only secrets. False, with
	// options unchanged, when nothing is to be forgotten (never with MCP writes, where Auto means
	// the master user secret).
	Forget(c SignInCandidate, options *Options) bool
}

// checkSignIn runs the test query; a package variable so tests need no database.
var checkSignIn = dbmcp.CheckSignIn

// sessionSignIn is the SignInChecker of a prepared RDS session.
type sessionSignIn struct {
	runner  *Runner
	session *Session
	aws     dbmcp.AWSRunner
	// updateIDE registers the IDE data source again for another user; nil without data source.
	updateIDE    func(user string)
	resourceType string
	instance     string
	localPort    string
	cfg          awsConfig
	target       ideTarget
}

// signInApplies reports whether airlock can check the sign-in of target: an engine it queries,
// and not the Auto sign-in with MCP writes, which uses the master user secret.
func signInApplies(target *ideTarget, allowWrites bool) bool {
	if !MCPEngineSupported(target.Engine) {
		return false
	}
	auto := !target.IAMAuth && target.DBPassword == "" && target.DBSecretARN == ""
	return !auto || !allowWrites
}

func (s *sessionSignIn) Candidates(ctx context.Context) ([]SignInCandidate, error) {
	target := &s.target
	switch {
	case target.DBSecretARN != "":
		return []SignInCandidate{{Label: secretShortName(target.DBSecretARN), User: target.DBUser, SecretARN: target.DBSecretARN}}, nil
	case target.DBPassword != "":
		return []SignInCandidate{{Label: "password", User: target.DBUser}}, nil
	case target.IAMAuth:
		return []SignInCandidate{{Label: "AWS IAM", User: target.DBUser}}, nil
	}
	tag := s.runner.readOnlySecretTag
	if tag.IsZero() {
		return nil, errors.New(`no read-only secret tag configured (set "readOnlySecretTag"): pick a secret or a password in the wizard`)
	}
	secrets, err := dbmcp.ReadReadOnlySecrets(ctx, s.aws, s.instance, target.InstanceARN, tag)
	if err != nil {
		return nil, err
	}
	candidates := make([]SignInCandidate, 0, len(secrets))
	for _, secret := range secrets {
		label := secret.Credentials.Database
		if label == "" {
			label = secretShortName(secret.ARN)
		}
		candidates = append(candidates, SignInCandidate{Label: label, User: secret.Credentials.User, SecretARN: secret.ARN, Discovered: true})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Label < candidates[j].Label })
	return candidates, nil
}

func (s *sessionSignIn) Check(ctx context.Context, c SignInCandidate) error {
	port, err := strconv.Atoi(s.localPort)
	if err != nil {
		return err
	}
	if s.resourceType == resourceElastiCache {
		return s.checkCache(ctx, c, port)
	}
	target := &s.target
	database := dbmcp.DefaultDatabase(target.Engine, target.Database)
	var creds dbmcp.CredentialProvider
	switch {
	case c.SecretARN != "":
		creds = dbmcp.SecretCredentials(s.aws, c.SecretARN, c.User, database)
	case target.IAMAuth:
		creds = dbmcp.IAMTokenCredentials(s.aws, target.RDSHost, target.RDSPort, c.User, database)
	default:
		creds = dbmcp.PasswordCredentials(c.User, target.DBPassword, database)
	}
	return checkSignIn(ctx, target.Engine, creds, port)
}

// checkCache signs in to the cache through the tunnel with c and runs PING.
func (s *sessionSignIn) checkCache(ctx context.Context, c SignInCandidate, port int) error {
	target := s.target
	if c.SecretARN != "" {
		target.DBSecretARN, target.DBUser = c.SecretARN, c.User
	}
	creds, err := s.runner.cacheCredentials(s.instance, &target, s.cfg)
	if err != nil {
		return err
	}
	return cacheSignInError(checkCacheSignIn(ctx, cachemcp.ConnConfig{Credentials: creds, ServerName: target.CacheEndpoint, LocalPort: port, TLS: target.CacheTLS}))
}

// cacheSignInError makes a credentials refusal of a cache match ErrSignInRejected, keeping the
// cache's own message ("the cache rejected the credentials: …") rather than prefixing the
// database one; other errors pass through.
func cacheSignInError(err error) error {
	if errors.Is(err, cachemcp.ErrSignInRejected) {
		return cacheRejected{err}
	}
	return err
}

// cacheRejected is a cache refusal that is also ErrSignInRejected; its message is the cache's.
type cacheRejected struct{ error }

func (e cacheRejected) Unwrap() []error { return []error{ErrSignInRejected, e.error} }

func (s *sessionSignIn) Use(c SignInCandidate, options *Options) {
	if c.SecretARN == "" || c.SecretARN == s.target.DBSecretARN {
		return
	}
	s.target.DBSecretARN, s.target.DBUser = c.SecretARN, c.User
	s.target.DBPassword, s.target.IAMAuth = "", false
	session := s.session
	session.Details.Auth = describeAuth(&s.target)
	session.Details.RestartCommand = buildRestartCommand(s.resourceType, s.instance, s.localPort, restartDBUser(&s.target), restartDBSecret(&s.target), s.cfg)
	session.ReadCredentials = s.runner.secretCredentialsReader(s.aws, s.instance, &s.target)
	// No *cachemcp.Handle case: a cache session's only candidate is its current sign-in, so Use returned above.
	if handle, ok := session.mcp.(*dbmcp.Handle); ok {
		_ = handle.UseSecret(c.SecretARN, c.User) //nolint:errcheck // closing the former connections may fail; the next queries open new ones
	}
	if s.updateIDE != nil {
		s.updateIDE(c.User)
	}
	options.SignIn, options.DBSecretARN, options.DBUser = SignInSecret, c.SecretARN, c.User
	options.DBPassword, options.RememberPassword = "", false
}

func (s *sessionSignIn) Forget(c SignInCandidate, options *Options) bool {
	sqlServer := strings.HasPrefix(strings.ToLower(s.target.Engine), "sqlserver")
	// With MCP writes, Auto signs in with the master user secret: falling back to it would silently
	// hand the MCP server administrator access on the next reconnection.
	if c.Discovered || c.SecretARN == "" || !sqlServer || s.runner.readOnlySecretTag.IsZero() || options.MCPAllowWrites {
		return false
	}
	// The running session keeps its credentials until others are used; the next candidates are
	// the read-only secrets.
	s.target.DBSecretARN, s.target.DBUser = "", ""
	options.SignIn, options.DBSecretARN, options.DBUser = SignInAuto, "", ""
	return true
}
