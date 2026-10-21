package cachemcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
)

// callTimeout bounds every tool call, so none hangs on a slow or blocked cache.
const callTimeout = 10 * time.Second

// dialTimeout bounds the connection to the cache, or to a node through its tunnel once open.
const dialTimeout = 5 * time.Second

// ErrSignInRejected reports credentials the cache refused: wrong password, unknown user, expired token.
var ErrSignInRejected = errors.New("the cache rejected the credentials")

// ConnConfig describes how to reach a cache through its tunnel.
type ConnConfig struct {
	// Credentials signs every new connection in; nil for caches without auth.
	Credentials CredentialProvider
	// Forward opens the tunnels to the nodes of a cluster-mode cache; nil dials the nodes directly.
	Forward Forward
	// ServerName is the real endpoint host, checked against the certificate when TLS is on.
	ServerName string
	// Host is where LocalPort listens; empty is localhost (the tunnel).
	Host      string
	LocalPort int
	TLS       bool
}

// rootCAs verifies the cache certificates; nil uses the system roots, which trust ElastiCache.
var rootCAs *x509.CertPool

// clients holds one small go-redis client per database index used, or the cluster client of a
// cluster-mode cache.
type clients struct {
	byDB    map[int]*redis.Client
	cluster *redis.ClusterClient
	tunnels *tunnels
	cfg     ConnConfig
	mu      sync.Mutex
	// detected reports that INFO cluster was read: cluster is set in cluster mode.
	detected bool
}

func newClients(cfg ConnConfig) *clients {
	return &clients{byDB: map[int]*redis.Client{}, cfg: cfg, tunnels: newTunnels(cfg.Forward)}
}

// open returns the client of database db: the cluster client once the cache reports cluster
// mode, where only database 0 exists.
func (c *clients) open(ctx context.Context, db int) (redis.UniversalClient, error) {
	cluster, err := c.clusterClient(ctx)
	switch {
	case err != nil:
		return nil, err
	case cluster == nil:
		return c.get(db), nil
	case db != 0:
		return nil, errors.New("cluster mode is enabled: the cache has only database 0")
	default:
		return cluster, nil
	}
}

// clusterClient is the cluster client in cluster mode, nil otherwise; the mode is read once.
func (c *clients) clusterClient(ctx context.Context) (*redis.ClusterClient, error) {
	c.mu.Lock()
	detected, cluster := c.detected, c.cluster
	c.mu.Unlock()
	if detected {
		return cluster, nil
	}
	enabled, err := clusterEnabled(ctx, c.get(0))
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.detected {
		c.detected = true
		if enabled {
			c.cluster = c.newCluster()
		}
	}
	return c.cluster, nil
}

func (c *clients) get(db int) *redis.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if client, ok := c.byDB[db]; ok {
		return client
	}
	options := &redis.Options{
		Addr: c.seed(),
		DB:   db,
		// RESP2 and no CLIENT SETINFO or maintenance notifications: every ElastiCache version answers.
		Protocol:                 2,
		DisableIdentity:          true,
		MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeDisabled},
		PoolSize:                 2,
		DialTimeout:              dialTimeout,
		ReadTimeout:              callTimeout,
		WriteTimeout:             callTimeout,
	}
	if c.cfg.Credentials != nil {
		options.CredentialsProviderContext = c.cfg.Credentials
	}
	if c.cfg.TLS {
		options.TLSConfig = &tls.Config{ServerName: c.cfg.ServerName, RootCAs: rootCAs, MinVersion: tls.VersionTLS12}
	}
	client := redis.NewClient(options)
	c.byDB[db] = client
	return client
}

// setCredentials signs the next connections in with p and closes the open ones; the node tunnels
// stay open.
func (c *clients) setCredentials(p CredentialProvider) error {
	c.mu.Lock()
	c.cfg.Credentials = p
	c.mu.Unlock()
	return c.close()
}

func (c *clients) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	for db, client := range c.byDB {
		errs = append(errs, client.Close())
		delete(c.byDB, db)
	}
	if c.cluster != nil {
		errs = append(errs, c.cluster.Close())
		c.cluster = nil
	}
	c.detected = false
	return errors.Join(errs...)
}

// shutdown closes the clients and the node tunnels, once the session ends.
func (c *clients) shutdown() error {
	err := c.close()
	c.tunnels.close()
	return err
}

// mapError explains the replies an assistant cannot act on as they are.
func mapError(err error) error {
	if err == nil || errors.Is(err, redis.Nil) {
		return err
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "NOPERM"):
		return fmt.Errorf("refused by the cache user's ACL: %s", message)
	case strings.HasPrefix(message, "WRONGPASS"), strings.HasPrefix(message, "NOAUTH"):
		// Without the server's final period, callers can append to the message.
		return fmt.Errorf("%w: %s", ErrSignInRejected, strings.TrimSuffix(message, "."))
	default:
		return err
	}
}

// CheckSignIn signs in through the tunnel with cfg and runs PING; refused credentials come back
// wrapped in ErrSignInRejected.
func CheckSignIn(ctx context.Context, cfg ConnConfig) error {
	c := newClients(cfg)
	err := mapError(c.get(0).Ping(ctx).Err())
	return errors.Join(err, c.close())
}
