package cachemcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
)

// callTimeout bounds every tool call, so none hangs on a slow or blocked cache.
const callTimeout = 10 * time.Second

// ErrSignInRejected reports credentials the cache refused: wrong password, unknown user, expired token.
var ErrSignInRejected = errors.New("the cache rejected the credentials")

// ConnConfig describes how to reach a cache through its tunnel.
type ConnConfig struct {
	// Credentials signs every new connection in; nil for caches without auth.
	Credentials CredentialProvider
	// ServerName is the real endpoint host, checked against the certificate when TLS is on.
	ServerName string
	// Host is where LocalPort listens; empty is localhost (the tunnel).
	Host      string
	LocalPort int
	TLS       bool
}

// rootCAs verifies the cache certificates; nil uses the system roots, which trust ElastiCache.
var rootCAs *x509.CertPool

// clients holds one small go-redis client per database index used.
type clients struct {
	byDB map[int]*redis.Client
	cfg  ConnConfig
	mu   sync.Mutex
}

func newClients(cfg ConnConfig) *clients {
	return &clients{byDB: map[int]*redis.Client{}, cfg: cfg}
}

func (c *clients) get(db int) *redis.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if client, ok := c.byDB[db]; ok {
		return client
	}
	host := c.cfg.Host
	if host == "" {
		host = "localhost"
	}
	options := &redis.Options{
		Addr: net.JoinHostPort(host, strconv.Itoa(c.cfg.LocalPort)),
		DB:   db,
		// RESP2 and no CLIENT SETINFO or maintenance notifications: every ElastiCache version answers.
		Protocol:                 2,
		DisableIdentity:          true,
		MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeDisabled},
		PoolSize:                 2,
		DialTimeout:              5 * time.Second,
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

// setCredentials signs the next connections in with p and closes the open ones.
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
	return errors.Join(errs...)
}

// mapError explains the replies an assistant cannot act on as they are.
func mapError(err error) error {
	if err == nil || errors.Is(err, redis.Nil) {
		return err
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "MOVED ") || strings.HasPrefix(message, "ASK "):
		return fmt.Errorf("the key lives on another shard, not reachable through this tunnel (%s)", message)
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
