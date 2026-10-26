package jumphost

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/n2jsoft-public-org/airlock/internal/cachemcp"
	"github.com/n2jsoft-public-org/airlock/internal/dbmcp"
)

// ClientEngineRedis is the Client engine of Redis and Valkey caches.
const ClientEngineRedis = "redis"

// Client is how a program run with a session reaches its database or cache (airlock exec).
type Client struct {
	// Engine is postgres, sqlserver or ClientEngineRedis; empty for the engines airlock does not
	// sign in to, and for EKS: only Address applies.
	Engine string
	// ServerName is the host the certificate of a TLS cache names.
	ServerName string
	// User and Password sign in; both empty without credentials (a cache without auth).
	User     string
	Password string
	Database string
	// Address is where to connect, and how: TLSRequire encrypts without checking a certificate
	// that names the remote host, not the tunnel.
	Address dbmcp.Address
}

// Client returns how a program reaches the database or cache of s, signed in with c: the candidate
// whose check succeeded, nil when the session has no sign-in check. A cache requiring a sign-in
// that none was chosen for fails.
func (s *Session) Client(ctx context.Context, c *SignInCandidate) (Client, error) {
	if s.client == nil {
		return Client{Address: dbmcp.TunnelAddress(s.LocalPort)}, nil
	}
	return s.client(ctx, c)
}

// rdsCredentials is how c signs in to the RDS database of target.
func rdsCredentials(awsCfg *aws.Config, target *ideTarget, c *SignInCandidate) dbmcp.CredentialProvider {
	database := dbmcp.DefaultDatabase(target.Engine, target.Database)
	switch {
	case c.SecretARN != "":
		return dbmcp.SecretCredentials(awsCfg, c.SecretARN, c.User, database)
	case target.IAMAuth:
		return dbmcp.IAMTokenCredentials(awsCfg, target.RDSHost, target.RDSPort, c.User, database)
	default:
		return dbmcp.PasswordCredentials(c.User, target.DBPassword, database)
	}
}

// rdsClient is the Client of an RDS session through the tunnel on localPort.
func rdsClient(awsCfg *aws.Config, prepared *ideTarget, localPort int) func(context.Context, *SignInCandidate) (Client, error) {
	target := *prepared
	return func(ctx context.Context, c *SignInCandidate) (Client, error) {
		client := Client{Address: dbmcp.TunnelAddress(localPort)}
		engine, ok := dbmcp.EngineFor(target.Engine)
		if !ok {
			return client, nil
		}
		client.Engine, client.Database = string(engine), dbmcp.DefaultDatabase(target.Engine, target.Database)
		if c == nil {
			return client, nil
		}
		creds, err := rdsCredentials(awsCfg, &target, c).Credentials(ctx, "")
		if err != nil {
			return Client{}, err
		}
		client.User, client.Password, client.Database = creds.User, creds.Password, creds.Database
		return client, nil
	}
}

// cacheClient is the Client of an ElastiCache session through the tunnel on localPort.
func (r *Runner) cacheClient(cacheName string, prepared *ideTarget, localPort int, cfg awsConfig) func(context.Context, *SignInCandidate) (Client, error) {
	return func(ctx context.Context, c *SignInCandidate) (Client, error) {
		target := *prepared
		client := Client{Address: dbmcp.TunnelAddress(localPort)}
		if !CacheEngineSupported(target.Engine) {
			return client, nil
		}
		client.Engine = ClientEngineRedis
		if target.CacheTLS {
			client.ServerName = target.CacheEndpoint
		} else {
			client.Address.TLS = dbmcp.TLSDisable
		}
		if c != nil && c.SecretARN != "" {
			target.DBSecretARN, target.DBUser = c.SecretARN, c.User
		}
		creds, err := r.cacheCredentials(ctx, cacheName, &target, cfg)
		if err != nil || creds == nil {
			return client, err
		}
		client.User, client.Password, err = creds(ctx)
		return client, err
	}
}

// cacheCredentialsClient is the Client of a cache reached directly at addr, signed in with creds
// (nil without auth).
func cacheCredentialsClient(addr dbmcp.Address, creds cachemcp.CredentialProvider) func(context.Context, *SignInCandidate) (Client, error) {
	return func(ctx context.Context, _ *SignInCandidate) (Client, error) {
		client := Client{Engine: ClientEngineRedis, Address: addr}
		if creds == nil {
			return client, nil
		}
		var err error
		client.User, client.Password, err = creds(ctx)
		return client, err
	}
}
