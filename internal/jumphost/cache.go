package jumphost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/n2jsoft/airlock/internal/cachemcp"
	"github.com/n2jsoft/airlock/internal/dbmcp"
)

// How an ElastiCache cache signs clients in.
const (
	// CacheAuthNone accepts any client of the VPC.
	CacheAuthNone = "none"
	// CacheAuthToken asks for the legacy AUTH token: a password only, as the default user.
	CacheAuthToken = "token"
	// CacheAuthRBAC asks for a user of its user group: a password, or an AWS IAM token.
	CacheAuthRBAC = "rbac"
)

// cacheAuthMode tells how a cache signs clients in from what the ElastiCache API reports.
func cacheAuthMode(authToken bool, userGroups []string) string {
	switch {
	case len(userGroups) > 0:
		return CacheAuthRBAC
	case authToken:
		return CacheAuthToken
	default:
		return CacheAuthNone
	}
}

// CacheEngineSupported reports whether airlock serves an MCP server for the ElastiCache engine.
func CacheEngineSupported(engine string) bool {
	_, ok := cachemcp.EngineFor(engine)
	return ok
}

// resolveCacheSignIn fills the sign-in of a cache target from options: the secret, the typed
// password, else AWS IAM as the given user. Caches without auth ignore them.
func (r *Runner) resolveCacheSignIn(ctx context.Context, cfg awsConfig, options *Options, cacheName string, target *ideTarget) error {
	if target.CacheAuth == CacheAuthNone || target.CacheAuth == "" {
		return nil
	}
	user := strings.TrimSpace(options.DBUser)
	switch secretID := strings.TrimSpace(options.DBSecretARN); {
	case secretID != "":
		secret, err := dbmcp.ReadPasswordSecret(ctx, r.awsRunner(cfg), secretID)
		if err != nil {
			return fmt.Errorf("failed to read secret %s (needs secretsmanager:GetSecretValue, and kms:Decrypt for a customer-managed key): %w", secretID, err)
		}
		if secret.Username != "" {
			user = secret.Username
		}
		target.DBSecretARN, target.DBUser = secretID, orCacheDefaultUser(user)
	case options.DBPassword != "":
		target.DBPassword, target.DBUser = options.DBPassword, orCacheDefaultUser(user)
	case options.SignIn == SignInIAM || (options.SignIn == "" && user != ""):
		if target.CacheAuth != CacheAuthRBAC {
			return fmt.Errorf("AWS IAM sign-in needs a cache with an RBAC user group; %s uses an AUTH token: pass --db-secret, or pick a password in the wizard", cacheName)
		}
		if user == "" {
			return fmt.Errorf("an RBAC user is required to sign in to %s with AWS IAM: pass --db-user", cacheName)
		}
		target.IAMAuth, target.DBUser = true, user
	}
	return nil
}

// applyNodeReplicationGroup sets on target the sign-in of the replication group groupID of a cache
// node: its user groups, AUTH token and encryption. IAM tokens are signed for the group.
func (r *Runner) applyNodeReplicationGroup(ctx context.Context, cfg awsConfig, groupID string, target *ideTarget) error {
	raw, err := r.runAWSCapture(ctx, cfg.Args,
		"elasticache", "describe-replication-groups",
		"--replication-group-id", groupID,
		"--query", "ReplicationGroups[0]",
		"--output", "json",
	)
	if err != nil {
		return err
	}
	var group struct {
		UserGroupIDs             []string `json:"UserGroupIds"`
		TransitEncryptionEnabled bool     `json:"TransitEncryptionEnabled"`
		AuthTokenEnabled         bool     `json:"AuthTokenEnabled"`
	}
	if err := json.Unmarshal([]byte(raw), &group); err != nil {
		return errors.New("failed to parse the replication group")
	}
	target.CacheAuth = cacheAuthMode(group.AuthTokenEnabled, group.UserGroupIDs)
	target.CacheTLS = group.TransitEncryptionEnabled
	target.CacheReplicationGroup = groupID
	return nil
}

func orCacheDefaultUser(user string) string {
	if user = strings.TrimSpace(user); user != "" {
		return user
	}
	return cachemcp.DefaultUser
}

// cacheSessionInfo explains how redis-cli reaches the tunnel of target.
func cacheSessionInfo(localPort int, target *ideTarget) []string {
	if !CacheEngineSupported(target.Engine) {
		return nil
	}
	args := []string{"redis-cli", "-p", strconv.Itoa(localPort)}
	if target.CacheTLS {
		args = append(args, "--tls", "--sni", target.CacheEndpoint)
	}
	if target.CacheAuth == CacheAuthRBAC || target.CacheAuth == CacheAuthToken {
		user := target.DBUser
		if user == "" {
			user = "<user>"
		}
		args = append(args, "--user", user, "--askpass")
	}
	lines := []string{"", "With redis-cli (or valkey-cli):", "  " + strings.Join(args, " ")}
	if target.IAMAuth {
		lines = append(lines, "  The password is an AWS IAM token: airlock signs the MCP server in with fresh ones.")
	}
	return lines
}

// ListCacheIAMUsers lists, sorted, the users of the cache of options that sign in with AWS IAM.
func (r *Runner) ListCacheIAMUsers(ctx context.Context, options *Options) ([]string, error) {
	cfg, err := r.resolveAWSConfig(ctx, options)
	if err != nil {
		return nil, err
	}
	groups, err := r.runAWSCapture(ctx, cfg.Args, "elasticache", "describe-user-groups", "--query", "UserGroups", "--output", "json")
	if err != nil {
		return nil, err
	}
	users, err := r.runAWSCapture(ctx, cfg.Args, "elasticache", "describe-users", "--query", "Users", "--output", "json")
	if err != nil {
		return nil, err
	}
	return iamUsersOf(groups, users, strings.TrimSpace(options.Name))
}

// iamUsersOf picks the IAM users of the user groups attached to cacheName.
func iamUsersOf(groupsJSON, usersJSON, cacheName string) ([]string, error) {
	var groups []struct {
		UserIDs           []string `json:"UserIds"`
		ReplicationGroups []string `json:"ReplicationGroups"`
		ServerlessCaches  []string `json:"ServerlessCaches"`
	}
	var users []struct {
		UserID         string `json:"UserId"`
		Authentication struct {
			Type string `json:"Type"`
		} `json:"Authentication"`
	}
	if err := json.Unmarshal([]byte(groupsJSON), &groups); err != nil {
		return nil, errors.New("failed to parse the ElastiCache user groups")
	}
	if err := json.Unmarshal([]byte(usersJSON), &users); err != nil {
		return nil, errors.New("failed to parse the ElastiCache users")
	}
	members := map[string]bool{}
	for _, group := range groups {
		if slices.Contains(group.ReplicationGroups, cacheName) || slices.Contains(group.ServerlessCaches, cacheName) {
			for _, id := range group.UserIDs {
				members[id] = true
			}
		}
	}
	iamUsers := make([]string, 0)
	for _, user := range users {
		if members[user.UserID] && user.Authentication.Type == "iam" {
			iamUsers = append(iamUsers, user.UserID)
		}
	}
	sort.Strings(iamUsers)
	return iamUsers, nil
}

// prepareCacheTarget ends the preparation of a cache reached at address:port: it resolves the
// sign-in, reserves the local port and describes the session. display names the session.
func (r *Runner) prepareCacheTarget(ctx context.Context, cfg awsConfig, options *Options, name, address string, port int,
	target *ideTarget, display func(localPort int) string) (preparedTarget, error) {
	target.CacheEndpoint = address
	if err := r.resolveCacheSignIn(ctx, cfg, options, name, target); err != nil {
		return preparedTarget{}, err
	}
	localPort, err := r.reservePort(sessionLabel(resourceElastiCache, name, cfg), options.LocalPortNumber, port, defaultRedisPort)
	if err != nil {
		return preparedTarget{}, err
	}
	sessionInfo := append([]string{
		fmt.Sprintf("host:port  = localhost:%d", localPort),
		fmt.Sprintf("remote     = %s:%d", address, port),
	}, cacheSessionInfo(localPort, target)...)
	params := map[string][]string{
		"host":            {address},
		"portNumber":      {strconv.Itoa(port)},
		"localPortNumber": {strconv.Itoa(localPort)},
	}
	return preparedTarget{name: name, display: display(localPort), info: sessionInfo, ssmParameters: params, target: *target}, nil
}

// cachemcpStart starts a cache session's MCP server; a package variable so tests can force a failure.
var cachemcpStart = cachemcp.Start

// checkCacheSignIn runs the PING check; a package variable so tests need no cache.
var checkCacheSignIn = cachemcp.CheckSignIn

// errCacheSignInRequired reports a cache with auth whose session chose no way to sign in.
var errCacheSignInRequired = errors.New("this cache requires a sign-in: pass --db-user (AWS IAM) or --db-secret, or pick one in the wizard")

// cacheCredentials is how airlock signs in to the cache of target; nil for caches without auth.
func (r *Runner) cacheCredentials(cacheName string, target *ideTarget, cfg awsConfig) (cachemcp.CredentialProvider, error) {
	aws := r.awsRunner(cfg)
	switch {
	case target.CacheAuth == CacheAuthNone || target.CacheAuth == "":
		return nil, nil
	case target.DBSecretARN != "":
		secretID, fallbackUser := target.DBSecretARN, target.DBUser
		return cachemcp.SecretCredentials(func(ctx context.Context) (string, string, error) {
			secret, err := dbmcp.ReadPasswordSecret(ctx, aws, secretID)
			if err != nil {
				return "", "", err
			}
			user := secret.Username
			if user == "" {
				user = fallbackUser
			}
			return user, secret.Password, nil
		}), nil
	case target.DBPassword != "":
		return cachemcp.PasswordCredentials(target.DBUser, target.DBPassword), nil
	case target.IAMAuth:
		if target.CacheReplicationGroup != "" {
			cacheName = target.CacheReplicationGroup
		}
		return cachemcp.IAMCredentials(aws, cacheName, target.DBUser, cfg.Region, target.CacheServerless), nil
	default:
		return nil, errCacheSignInRequired
	}
}

// cacheSignInApplies reports whether airlock checks the sign-in of a cache session: an engine it
// queries, with auth, and a sign-in chosen.
func cacheSignInApplies(target *ideTarget) bool {
	if !CacheEngineSupported(target.Engine) || target.CacheAuth == CacheAuthNone || target.CacheAuth == "" {
		return false
	}
	return target.DBSecretARN != "" || target.DBPassword != "" || target.IAMAuth
}

// startCacheMCP serves the MCP server of a cache session and returns its display line: empty for
// engines without server, "not started: …" on failure or without sign-in.
func (r *Runner) startCacheMCP(options *Options, cacheName string, localPort int, target *ideTarget, cfg awsConfig, allowWrites bool) (handle *cachemcp.Handle, display string) {
	if !CacheEngineSupported(target.Engine) {
		return nil, ""
	}
	creds, err := r.cacheCredentials(cacheName, target, cfg)
	if err != nil {
		return nil, "not started: " + err.Error()
	}
	projectDir, err := r.projectDirFor(options)
	if err != nil {
		return nil, fmt.Sprintf("not started: %v", err)
	}
	handle, err = cachemcpStart(r.mcpHost, &cachemcp.SessionConfig{
		Credentials: creds, OnRejected: r.forgetKeychainFunc(target.KeychainAccount, target.DBPassword),
		ProjectDir: projectDir, Profile: cfg.Profile, Region: cfg.Region, Target: cacheName,
		Engine: target.Engine, Endpoint: target.CacheEndpoint, ServerPrefix: r.mcpServerPrefix, LogDir: r.mcpLogDir,
		LocalPort: localPort, TLS: target.CacheTLS, ClusterMode: target.CacheClusterMode, AllowWrites: allowWrites,
		SkipRegistration: options.SkipMCPRegistration,
	})
	if err != nil {
		return nil, fmt.Sprintf("not started: %v", err)
	}
	if handle == nil {
		return nil, ""
	}
	display = handle.Display()
	return handle, display
}
