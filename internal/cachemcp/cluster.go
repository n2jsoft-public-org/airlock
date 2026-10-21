package cachemcp

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
)

// Forward opens a tunnel to the cluster node at addr (host:port, as the cluster announces it) and
// returns the local address the tunnel listens on, and how to close it.
type Forward func(ctx context.Context, addr string) (local string, stop func(), err error)

// tunnelOpenTimeout bounds the opening of a node tunnel, apart from the tool call that needs it:
// a call ending first leaves the tunnel to the next one.
const tunnelOpenTimeout = 30 * time.Second

var errTunnelsClosed = errors.New("the session ended")

type tunnel struct {
	ready chan struct{}
	stop  func()
	err   error
	local string
}

// tunnels maps the addresses cluster nodes announce to the local tunnels reaching them, opened on
// first use and kept until close.
type tunnels struct {
	forward Forward
	byAddr  map[string]*tunnel
	mu      sync.Mutex
	closed  bool
}

func newTunnels(forward Forward) *tunnels {
	return &tunnels{forward: forward, byAddr: map[string]*tunnel{}}
}

// local is the local address reaching addr, opening its tunnel on first use; tunnels to different
// nodes open in parallel.
func (t *tunnels) local(ctx context.Context, addr string) (string, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return "", errTunnelsClosed
	}
	tun, ok := t.byAddr[addr]
	if !ok {
		tun = &tunnel{ready: make(chan struct{})}
		t.byAddr[addr] = tun
		go t.open(addr, tun) //nolint:gosec // G118: the tunnel outlives the call opening it (see tunnelOpenTimeout)
	}
	t.mu.Unlock()
	select {
	case <-tun.ready:
		return tun.local, tun.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (t *tunnels) open(addr string, tun *tunnel) {
	ctx, cancel := context.WithTimeout(context.Background(), tunnelOpenTimeout)
	defer cancel()
	local, stop, err := t.forward(ctx, addr)
	t.mu.Lock()
	defer t.mu.Unlock()
	if err == nil && t.closed {
		stop()
		err = errTunnelsClosed
	}
	if err != nil {
		// Forgotten, so the next dial tries again.
		delete(t.byAddr, addr)
	}
	tun.local, tun.stop, tun.err = local, stop, err
	close(tun.ready)
}

// close stops the open tunnels; the ones still opening stop once open.
func (t *tunnels) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	for addr, tun := range t.byAddr {
		select {
		case <-tun.ready:
			if tun.stop != nil {
				tun.stop()
			}
		default:
		}
		delete(t.byAddr, addr)
	}
}

// seed is the address the session's tunnel listens on, where the cluster is discovered.
func (c *clients) seed() string {
	host := c.cfg.Host
	if host == "" {
		host = "localhost"
	}
	return net.JoinHostPort(host, strconv.Itoa(c.cfg.LocalPort))
}

// dialNode connects to the cluster node at addr: the seed through the session's tunnel, the other
// nodes through a tunnel of their own, or directly without Forward. TLS checks the node's host
// name, or the cache endpoint when the cluster announces IP addresses.
func (c *clients) dialNode(ctx context.Context, network, addr string) (net.Conn, error) {
	target, serverName := addr, c.cfg.ServerName
	if addr != c.seed() {
		if host, _, err := net.SplitHostPort(addr); err == nil && net.ParseIP(host) == nil {
			serverName = host
		}
		if c.tunnels.forward != nil {
			local, err := c.tunnels.local(ctx, addr)
			if err != nil {
				return nil, err
			}
			target = local
		}
	}
	dialer := &net.Dialer{Timeout: dialTimeout}
	if !c.cfg.TLS {
		return dialer.DialContext(ctx, network, target)
	}
	tlsDialer := &tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: serverName, RootCAs: rootCAs, MinVersion: tls.VersionTLS12}}
	return tlsDialer.DialContext(ctx, network, target)
}

// newCluster is the cluster client of the cache, reaching every node through dialNode.
func (c *clients) newCluster() *redis.ClusterClient {
	options := &redis.ClusterOptions{
		Addrs:  []string{c.seed()},
		Dialer: c.dialNode,
		// Same settings as the standalone clients (see get); TLS is done by dialNode.
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
	return redis.NewClusterClient(options)
}

// clusterEnabled reads cluster_enabled in INFO cluster. A cache refusing INFO cluster (an ACL, an
// old engine) is standalone; refused credentials and network errors come back.
func clusterEnabled(ctx context.Context, client *redis.Client) (bool, error) {
	text, err := client.Info(ctx, "cluster").Result()
	if err != nil {
		// An empty prefix matches every reply of the server, not network errors.
		if redis.HasErrorPrefix(err, "") && !errors.Is(mapError(err), ErrSignInRejected) {
			return false, nil
		}
		return false, err
	}
	return parseInfo(text)["cluster"]["cluster_enabled"] == "1", nil
}

// primaries are the primary nodes of cluster, sorted by address so a scan cursor can name one.
func primaries(ctx context.Context, cluster *redis.ClusterClient) ([]*redis.Client, error) {
	var mu sync.Mutex
	var nodes []*redis.Client
	err := cluster.ForEachMaster(ctx, func(_ context.Context, node *redis.Client) error {
		mu.Lock()
		defer mu.Unlock()
		nodes = append(nodes, node)
		return nil
	})
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Options().Addr < nodes[j].Options().Addr })
	return nodes, err
}
